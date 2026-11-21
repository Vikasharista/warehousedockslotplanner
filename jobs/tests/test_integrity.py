"""The database itself must refuse a double booking, whatever the application does."""

import threading

import psycopg
import pytest
from psycopg import errors

INSERT = """
INSERT INTO appointments (appointment_id, shipment_id, slot_id, appointment_status, booking_source,
                          is_current, booked_at, updated_at)
VALUES (%s, %s, %s, %s, 'SCHEDULING_TOOL', 1, now(), now())
"""

FREE_SLOT = "SLOT-JAI-047"  # D4 12:00, OPEN and unbooked in the snapshot


def test_free_slot_is_free(seeded):
    (status,) = seeded.execute(
        "SELECT availability_status FROM v_slot_availability WHERE slot_id = %s", (FREE_SLOT,)
    ).fetchone()
    assert status == "AVAILABLE"


def test_second_active_booking_on_an_occupied_slot_is_rejected(seeded):
    # SLOT-JAI-046 already holds SHP1009 (CONFIRMED). SHP1012 has no current booking.
    with pytest.raises(errors.UniqueViolation, match="ux_active_appointment_per_slot"):
        seeded.execute(INSERT, ("APT-T1", "SHP1012", "SLOT-JAI-046", "CONFIRMED"))


def test_a_pending_hold_also_blocks_the_slot(seeded):
    # SLOT-JAI-018 holds SHP1013's PENDING_CONFIRMATION from driver chat.
    with pytest.raises(errors.UniqueViolation, match="ux_active_appointment_per_slot"):
        seeded.execute(INSERT, ("APT-T2", "SHP1012", "SLOT-JAI-018", "CONFIRMED"))


def test_second_current_booking_for_one_shipment_is_rejected(seeded):
    # SHP1009 is already CONFIRMED on SLOT-JAI-046; a second current active booking must fail.
    with pytest.raises(errors.UniqueViolation, match="ux_current_active_appointment_per_shipment"):
        seeded.execute(INSERT, ("APT-T3", "SHP1009", FREE_SLOT, "CONFIRMED"))


def test_inactive_history_does_not_block_the_slot(seeded):
    seeded.execute(INSERT, ("APT-T4", "SHP1012", FREE_SLOT, "CANCELLED"))
    seeded.execute(INSERT, ("APT-T5", "SHP1012", FREE_SLOT, "CONFIRMED"))
    (n,) = seeded.execute(
        "SELECT count(*) FROM appointments WHERE slot_id = %s", (FREE_SLOT,)
    ).fetchone()
    assert n == 2


def test_competing_bookings_for_one_free_slot_leave_exactly_one_winner(seeded, database_url):
    """Ten drivers try to take the same free slot at once; the database lets exactly one through."""
    seeded.commit()
    trucks = ["SHP1012", "SHP1007", "SHP1017", "SHP1018", "SHP1020", "SHP1013", "SHP1016", "SHP1011",
              "SHP1006", "SHP1014"]
    # Free every competitor's current booking so only the slot rule can stop them.
    seeded.execute(
        "UPDATE appointments SET appointment_status = 'CANCELLED', is_current = 0 WHERE shipment_id = ANY(%s)",
        (trucks,),
    )
    seeded.commit()

    barrier = threading.Barrier(len(trucks))
    outcomes: dict[str, str] = {}

    def book(shipment: str) -> None:
        with psycopg.connect(database_url) as conn:
            barrier.wait()
            try:
                conn.execute(INSERT, (f"APT-RACE-{shipment}", shipment, FREE_SLOT, "CONFIRMED"))
                conn.commit()
                outcomes[shipment] = "won"
            except errors.UniqueViolation:
                conn.rollback()
                outcomes[shipment] = "rejected"

    threads = [threading.Thread(target=book, args=(s,)) for s in trucks]
    for t in threads:
        t.start()
    for t in threads:
        t.join()

    assert sorted(outcomes.values()).count("won") == 1
    assert sorted(outcomes.values()).count("rejected") == len(trucks) - 1
    (active,) = seeded.execute(
        """SELECT count(*) FROM appointments WHERE slot_id = %s
           AND appointment_status IN ('PENDING_CONFIRMATION','CONFIRMED','IN_PROGRESS')""",
        (FREE_SLOT,),
    ).fetchone()
    assert active == 1
