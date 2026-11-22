import datetime as dt
from zoneinfo import ZoneInfo

from setuhaul_jobs import demo_reset, scenario, seed

IST = ZoneInfo("Asia/Kolkata")


def test_reset_restores_the_snapshot_and_keeps_accounts(seeded):
    seeded.execute("""INSERT INTO app_users (email, display_name, password_hash)
                      VALUES ('demo@setuhaul.io', 'demo', 'hash')""")
    seed.set_clock(seeded, "2026-08-04T20:40:00+05:30")
    scenario.add_now(seeded, 5, seed=1)
    seeded.execute("INSERT INTO truck_request_jobs (facility_id, trucks) VALUES ('FAC-JAI-01', 3)")
    seeded.execute("""INSERT INTO solver_run (run_id, facility_id, as_of_ts, horizon_end_ts, run_trigger, status, score,
            feasible, trucks_planned, assigned, unassigned, avg_wait_min, weighted_wait_min, priority_inversions,
            pickup_inversions, forced_moves, unforced_moves, dock_utilisation_pct, settings, solve_ms)
        VALUES ('RUN-T', 'FAC-JAI-01', setuhaul_now(), setuhaul_now() + interval '12 hours', 'MANUAL', 'SOLVED', 's',
                true, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, '{}', 0)""")
    seeded.commit()

    summary = demo_reset.reset(seeded, trucks=30, trickle=10)
    assert "1 accounts kept" in summary and "30 truck requests" in summary
    one = lambda sql: seeded.execute(sql).fetchone()[0]  # noqa: E731
    assert one("SELECT count(*) FROM app_users") == 1
    assert one("SELECT count(*) FROM solver_run") == 0
    assert one("SELECT count(*) FROM truck_request_jobs") == 0
    assert one("SELECT count(*) FROM shipments WHERE shipment_id LIKE 'SHP-G%'") == 30
    assert one("SELECT count(*) FROM shipments WHERE shipment_id NOT LIKE 'SHP-G%'") == 21
    started = one("SELECT setuhaul_now()")
    assert dt.timedelta(0) <= started - dt.datetime(2026, 8, 4, 9, 55, tzinfo=IST) < dt.timedelta(minutes=1)


def test_next_run_is_the_next_local_occurrence():
    at = dt.time(3, 0)
    before = dt.datetime(2026, 11, 22, 1, 0, tzinfo=IST)
    after = dt.datetime(2026, 11, 22, 3, 0, tzinfo=IST)
    assert demo_reset.next_run(before, at, IST) == dt.datetime(2026, 11, 22, 3, 0, tzinfo=IST)
    assert demo_reset.next_run(after, at, IST) == dt.datetime(2026, 11, 23, 3, 0, tzinfo=IST)
    utc = dt.datetime(2026, 11, 21, 21, 29, tzinfo=dt.timezone.utc)  # 02:59 IST
    assert demo_reset.next_run(utc, at, IST) == dt.datetime(2026, 11, 22, 3, 0, tzinfo=IST)


def test_reset_now_from_the_command_line(database_url, capsys):
    import psycopg

    with psycopg.connect(database_url) as conn:
        seed.load(conn, seed.open_snapshot(seed.SNAPSHOT_DB))
    assert demo_reset.main(["--database-url", database_url, "--now", "--trucks", "5", "--trickle", "0"]) == 0
    assert "reset:" in capsys.readouterr().out
