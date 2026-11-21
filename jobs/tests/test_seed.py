import datetime as dt

import psycopg
import pytest

from setuhaul_jobs import seed

EXPECTED_ROWS = {
    "vehicle_types": 5, "carriers": 4, "drivers": 15, "vehicles": 15, "facilities": 2, "docks": 9,
    "facility_contacts": 5, "facility_rules": 6, "shipments": 21, "eta_updates": 12,
    "appointment_slots": 106, "appointments": 20, "dock_status_events": 3, "facility_checkins": 5,
    "chat_threads": 12, "chat_messages": 20, "driver_exceptions": 10, "operational_messages": 5,
}

VIEWS = ("v_latest_eta", "v_slot_availability", "v_inbound_operational_state", "v_current_facility_queue")


def test_order_self_referencing_puts_parents_first():
    rows = [
        {"id": "C", "parent": "B"},
        {"id": "B", "parent": "A"},
        {"id": "A", "parent": None},
        {"id": "X", "parent": "missing"},
    ]
    ordered = [r["id"] for r in seed.order_self_referencing(rows, "id", "parent")]
    assert ordered.index("A") < ordered.index("B") < ordered.index("C")
    assert sorted(ordered) == ["A", "B", "C", "X"]


def test_order_self_referencing_rejects_cycles():
    rows = [{"id": "A", "parent": "B"}, {"id": "B", "parent": "A"}]
    with pytest.raises(seed.SeedError, match="cycle"):
        seed.order_self_referencing(rows, "id", "parent")


def test_load_copies_every_row_and_matches_the_guide(database_url, snapshot):
    with psycopg.connect(database_url) as conn:
        counts = seed.load(conn, snapshot)
    assert counts == EXPECTED_ROWS
    assert sum(counts.values()) == 275


def test_load_is_repeatable(database_url, snapshot):
    with psycopg.connect(database_url) as conn:
        first = seed.load(conn, snapshot)
        second = seed.load(conn, snapshot)
        (n,) = conn.execute("SELECT count(*) FROM appointments").fetchone()
    assert first == second
    assert n == EXPECTED_ROWS["appointments"]


def test_verify_counts_detects_a_missing_row(seeded, snapshot):
    seeded.execute("DELETE FROM operational_messages WHERE operational_message_id = 'OM005'")
    with pytest.raises(seed.SeedError, match="operational_messages: SQLite 5, Postgres 4"):
        seed.verify_counts(seeded.cursor(), snapshot)


def test_timestamps_keep_their_instant(seeded):
    (eta,) = seeded.execute(
        "SELECT effective_eta_ts FROM v_latest_eta WHERE shipment_id = 'SHP1006'"
    ).fetchone()
    assert eta == dt.datetime(2026, 8, 4, 11, 20, tzinfo=dt.timezone(dt.timedelta(hours=5, minutes=30)))


def _normalise(value):
    if isinstance(value, dt.datetime):
        return value.astimezone(dt.timezone(dt.timedelta(hours=5, minutes=30))).isoformat()
    if isinstance(value, dt.time):
        return value.strftime("%H:%M")
    return value


@pytest.mark.parametrize("view", VIEWS)
def test_views_return_the_same_rows_as_sqlite(seeded, snapshot, view):
    lite = snapshot.execute(f"SELECT * FROM {view}")
    lite_cols = [d[0] for d in lite.description]
    lite_rows = sorted(tuple(r) for r in lite.fetchall())

    cur = seeded.execute(f"SELECT * FROM {view}")
    pg_cols = [d.name for d in cur.description]
    pg_rows = sorted(tuple(_normalise(v) for v in r) for r in cur.fetchall())

    assert pg_cols == lite_cols
    assert pg_rows == lite_rows


def test_seed_starts_the_shared_clock_at_the_snapshot(database_url, snapshot):
    with psycopg.connect(database_url) as conn:
        seed.load(conn, snapshot)
        seed.set_clock(conn, seed.SNAPSHOT_MOMENT)
        (now,) = conn.execute("SELECT setuhaul_now() - '2026-08-04T09:55:00+05:30'::timestamptz").fetchone()
        assert 0 <= now.total_seconds() < 60
        seed.set_clock(conn, None)
        (real,) = conn.execute("SELECT abs(extract(epoch FROM setuhaul_now() - now()))").fetchone()
        assert real == 0


def test_init_seeds_an_empty_database_once(admin_url, capsys):
    from psycopg.conninfo import conninfo_to_dict, make_conninfo

    admin = admin_url
    name = "setuhaul_init_test"
    with psycopg.connect(admin, autocommit=True) as conn:
        conn.execute(f"DROP DATABASE IF EXISTS {name} WITH (FORCE)")
        conn.execute(f"CREATE DATABASE {name}")
    url = make_conninfo(**{**conninfo_to_dict(admin_url), "dbname": name})
    try:
        assert seed.main(["--database-url", url, "--init"]) == 0
        with psycopg.connect(url) as conn:
            conn.execute("UPDATE app_clock SET sim_anchor = sim_anchor + interval '1 hour'")
            conn.commit()
        assert seed.main(["--database-url", url, "--init"]) == 0
        assert "already seeded" in capsys.readouterr().out
        with psycopg.connect(url) as conn:
            (moved,) = conn.execute(
                "SELECT setuhaul_now() >= '2026-08-04T10:55:00+05:30'::timestamptz").fetchone()
            assert moved, "a second --init keeps the clock"
    finally:
        with psycopg.connect(admin, autocommit=True) as conn:
            conn.execute(f"DROP DATABASE IF EXISTS {name} WITH (FORCE)")
