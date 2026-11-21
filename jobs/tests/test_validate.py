"""The validator accepts a correct run and catches each kind of broken one.

Runs are written by hand here, so the validator is tested without the Java solver. The valid run is
built from facts in the 4 August snapshot at 09:55 (the guide's morning wave):
- SHP1010 reefer, ready 11:20  -> D5 12:00
- SHP1012 standard, ready 11:40 -> D4 13:00
- SHP1016 heavy, ready 12:30   -> D6 12:30
"""

import json

import pytest

from setuhaul_jobs import validate

AS_OF = "2026-08-04T09:55:00+05:30"
SETTINGS = json.dumps({"gateBufferMin": 30, "lowConfidenceMarginMin": 15})


def slot(conn, dock_code, hhmm):
    row = conn.execute(
        """SELECT sl.slot_id, sl.dock_id, sl.slot_start_ts, sl.slot_end_ts
           FROM appointment_slots sl JOIN docks d ON d.dock_id = sl.dock_id
           WHERE d.facility_id = 'FAC-JAI-01' AND d.dock_code = %s
             AND to_char(sl.slot_start_ts AT TIME ZONE 'Asia/Kolkata', 'HH24:MI') = %s""",
        (dock_code, hhmm),
    ).fetchone()
    assert row, f"no slot {dock_code} {hhmm}"
    return row


def store_run(conn, run_id, allocations, assigned=None):
    assigned = len(allocations) if assigned is None else assigned
    """allocations: (shipment_id, dock_code, 'HH:MM', end_override_minutes_or_None)."""
    conn.execute(
        """INSERT INTO solver_run (run_id, facility_id, as_of_ts, horizon_end_ts, run_trigger, status, score, feasible,
               trucks_planned, assigned, unassigned, avg_wait_min, weighted_wait_min, priority_inversions,
               pickup_inversions, forced_moves, unforced_moves, dock_utilisation_pct, settings, solve_ms)
           VALUES (%s, 'FAC-JAI-01', %s, %s::timestamptz + interval '12 hours', 'MANUAL', 'SOLVED', 'test', true,
                   %s, %s, 0, 0, 0, 0, 0, 0, 0, 0, %s::jsonb, 0)""",
        (run_id, AS_OF, AS_OF, assigned, assigned, SETTINGS),
    )
    for shipment_id, dock_code, hhmm, minutes in allocations:
        slot_id, dock_id, start, end = slot(conn, dock_code, hhmm)
        (unload,) = conn.execute(
            "SELECT expected_unload_min FROM shipments WHERE shipment_id = %s", (shipment_id,)).fetchone()
        length = int((end - start).total_seconds() // 60)
        duration = minutes if minutes is not None else max(unload, length)
        conn.execute(
            """INSERT INTO solver_plan_item (run_id, shipment_id, allocation_status, dock_id, slot_id,
                   planned_start_ts, planned_end_ts, booking_action)
               VALUES (%s, %s, 'ALLOCATED', %s, %s, %s, %s + make_interval(mins => %s), 'NEW')""",
            (run_id, shipment_id, dock_id, slot_id, start, start, duration),
        )
    return run_id


VALID = [("SHP1010", "D5", "12:00", None), ("SHP1012", "D4", "13:00", None), ("SHP1016", "D6", "12:30", None)]


def check_ids(conn, run_id):
    return sorted({v.check_id for v in validate.validate(conn, run_id)})


def test_every_check_has_a_header():
    assert [c.id for c in validate.load_checks()] == [f"V{n}" for n in range(1, 9)]


def test_a_correct_run_is_valid(seeded):
    run = store_run(seeded, "RUN-T-VALID", VALID)
    assert validate.validate(seeded, run) == []


@pytest.mark.parametrize(
    ("name", "allocations", "expected"),
    [
        # SHP1009 unloads 75 min from 12:00 on D4, running into SHP1012 at 13:00.
        ("overlap", VALID + [("SHP1009", "D4", "12:00", None)], ["V1"]),
        # Two trucks in the same slot also overlap.
        ("reuse", [("SHP1012", "D4", "13:00", None), ("SHP1007", "D4", "13:00", None)], ["V1", "V2"]),
        # D1 11:00 is held by SHP1014's driver hold, which this run does not replace.
        ("fixed booking", [("SHP1007", "D1", "11:00", None)], ["V3"]),
        # A standard load on the reefer dock, and a reefer load on a standard dock.
        ("standard on reefer", [("SHP1012", "D5", "13:00", None)], ["V4"]),
        ("reefer on standard", [("SHP1010", "D4", "13:00", None)], ["V4"]),
        # SHP1012 cannot be at the dock before 11:40 (ETA 11:10 + 30 min).
        ("before ready", [("SHP1012", "D2", "10:00", None)], ["V5"]),
        # D3 12:00 is BLOCKED by the leveller breakdown.
        ("blocked dock", [("SHP1012", "D3", "12:00", None)], ["V6"]),
        # 45 minutes is shorter than the 60-minute slot the truck holds.
        ("wrong duration", [("SHP1012", "D4", "13:00", 45)], ["V7"]),
    ],
)
def test_each_kind_of_broken_run_is_caught(seeded, name, allocations, expected):
    run = store_run(seeded, f"RUN-T-{name.replace(' ', '-')}", allocations)
    assert check_ids(seeded, run) == expected


def test_a_header_that_disagrees_with_its_items_is_caught(seeded):
    run = store_run(seeded, "RUN-T-HEADER", VALID, assigned=2)
    assert check_ids(seeded, run) == ["V8"]


def test_an_unknown_run_is_an_error_not_a_pass(seeded):
    with pytest.raises(LookupError):
        validate.validate(seeded, "RUN-NOPE")


def test_cli_exit_codes(seeded, database_url, capsys):
    store_run(seeded, "RUN-T-CLI-OK", VALID)
    store_run(seeded, "RUN-T-CLI-BAD", [("SHP1012", "D2", "10:00", None)])
    seeded.commit()
    assert validate.main(["--database-url", database_url, "--run-id", "RUN-T-CLI-OK"]) == 0
    assert validate.main(["--database-url", database_url, "--run-id", "RUN-T-CLI-BAD"]) == 1
    assert "V5" in capsys.readouterr().out
    assert validate.main(["--database-url", database_url, "--run-id", "RUN-NOPE"]) == 2

def test_missing_buffer_settings_fail_closed(seeded):
    run = store_run(seeded, "RUN-T-NOSETTINGS", VALID)
    seeded.execute("UPDATE solver_run SET settings = '{}'::jsonb WHERE run_id = %s", (run,))
    assert check_ids(seeded, run) == ["V5"]
