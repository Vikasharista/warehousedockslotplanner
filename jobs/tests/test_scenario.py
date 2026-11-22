import datetime as dt

import pytest

from setuhaul_jobs import scenario, seed

IST = dt.timezone(dt.timedelta(hours=5, minutes=30))
START = dt.datetime(2026, 8, 4, 9, 55, tzinfo=IST)


@pytest.fixture
def at_start(seeded):
    seed.set_clock(seeded, seed.SNAPSHOT_MOMENT)
    return seeded


def _generated(conn, sql, *args):
    return conn.execute(sql.replace("{G}", "shipment_id LIKE 'SHP-G%%'"), args).fetchall()


def test_scenario_splits_known_and_trickle_requests(at_start):
    s = scenario.generate(at_start, trucks=70, trickle=20, seed=42, now=START)
    assert len(s.created) == 70 and s.at_start == 50 and s.trickle == 20
    (known, later, first, last) = _generated(at_start, """
        SELECT count(*) FILTER (WHERE created_at <= %s), count(*) FILTER (WHERE created_at > %s),
               min(created_at) FILTER (WHERE created_at > %s), max(created_at)
        FROM shipments WHERE {G}""", START, START, START)[0]
    assert (known, later) == (50, 20)
    assert first >= dt.datetime(2026, 8, 4, 10, 0, tzinfo=IST)
    assert last <= dt.datetime(2026, 8, 4, 17, 0, tzinfo=IST)
    # Every truck is due after its request and leaves before it arrives.
    assert _generated(at_start, """SELECT count(*) FROM shipments WHERE {G}
        AND (original_eta_ts <= created_at OR planned_departure_ts >= original_eta_ts)""")[0][0] == 0


def test_the_mix_looks_like_the_snapshot(at_start):
    scenario.generate(at_start, trucks=70, trickle=20, seed=42, now=START)
    rows = dict(_generated(at_start, "SELECT required_dock_type, count(*) FROM shipments WHERE {G} GROUP BY 1"))
    assert 5 <= rows.get("REEFER", 0) <= 17 and 5 <= rows.get("HEAVY", 0) <= 17
    priorities = dict(_generated(at_start, "SELECT priority_code, count(*) FROM shipments WHERE {G} GROUP BY 1"))
    assert set(priorities) <= {"LOW", "NORMAL", "HIGH", "CRITICAL"} and priorities["NORMAL"] > priorities["LOW"]
    # Reefer loads ride reefer trucks; heavy ones are over the facility's 25 t heavy-dock rule.
    assert _generated(at_start, """SELECT count(*) FROM shipments s JOIN vehicles v USING (vehicle_id) WHERE {G}
        AND required_dock_type = 'REEFER' AND v.vehicle_type_code <> 'REEFER_32'""")[0][0] == 0
    assert _generated(at_start, """SELECT min(load_weight_kg) FROM shipments WHERE {G}
        AND required_dock_type = 'HEAVY'""")[0][0] > 25000
    lanes = {"Delhi", "Manesar", "Neemrana", "Ajmer", "Udaipur", "Ahmedabad", "Mumbai"}
    origins = {r[0] for r in _generated(at_start, "SELECT DISTINCT origin_city FROM shipments WHERE {G}")}
    assert origins & lanes and origins - lanes


def test_disruptions_and_coordinator_cases(at_start):
    s = scenario.generate(at_start, trucks=70, trickle=20, seed=42, now=START, coordinator_cases=4)
    assert s.eta_updates >= 8 and s.dock_events == 1
    # ETA reports are made after the request, during the day.
    assert _generated(at_start, """SELECT count(*) FROM eta_updates e JOIN shipments s USING (shipment_id)
        WHERE s.{G} AND (e.created_at < s.created_at OR e.created_at <= %s)""".replace("s.{G}", "s.shipment_id LIKE 'SHP-G%%'"),
        START)[0][0] == 0
    (event,) = at_start.execute("""SELECT event_type FROM dock_status_events WHERE dock_event_id LIKE 'EVT-G-%%'
        AND created_at > %s""", (START,)).fetchone()
    assert event == "BREAKDOWN"
    # Weight breaches: booked on a dock lighter than the load.
    breaches = at_start.execute("""SELECT count(*) FROM appointments a JOIN appointment_slots sl USING (slot_id)
        JOIN docks d USING (dock_id) JOIN shipments s USING (shipment_id)
        WHERE a.appointment_id LIKE 'APT-G%%' AND s.load_weight_kg > d.max_vehicle_weight_kg""").fetchone()[0]
    assert breaches == 2
    late = _generated(at_start, """SELECT count(*) FROM shipments WHERE {G}
        AND original_eta_ts >= '2026-08-04T20:45:00+05:30'""")[0][0]
    assert late >= 2


def test_same_seed_same_trucks(database_url, snapshot):
    import psycopg

    def run():
        with psycopg.connect(database_url) as conn:
            seed.load(conn, snapshot)
            scenario.generate(conn, trucks=30, trickle=10, seed=7, now=START)
            return conn.execute("""SELECT shipment_id, origin_city, priority_code, load_weight_kg, original_eta_ts,
                created_at FROM shipments WHERE shipment_id LIKE 'SHP-G%%' ORDER BY 1""").fetchall()

    assert run() == run()


def test_once_and_bad_arguments(at_start, database_url, capsys):
    with pytest.raises(ValueError):
        scenario.generate(at_start, trucks=10, trickle=11)
    with pytest.raises(ValueError):
        scenario.generate(at_start, trucks=10, trickle=8, coordinator_cases=4)
    at_start.commit()
    assert scenario.main(["--database-url", database_url, "--trucks", "12", "--trickle", "4", "--once"]) == 0
    assert scenario.main(["--database-url", database_url, "--once"]) == 0
    assert "already loaded (12" in capsys.readouterr().out
    assert scenario.main(["--database-url", database_url, "--trucks", "0"]) == 0


def test_add_trucks_jobs_are_fulfilled_once(at_start):
    at_start.execute("INSERT INTO truck_request_jobs (facility_id, trucks) VALUES ('FAC-JAI-01', 5), ('FAC-JAI-01', 3)")
    at_start.commit()
    done = scenario.process_jobs(at_start)
    assert [r.split(":")[0] for _, r in done] == ["5 trucks", "3 trucks"]
    assert scenario.process_jobs(at_start) == []
    rows = at_start.execute("SELECT status, count(*) FROM truck_request_jobs GROUP BY 1").fetchall()
    assert rows == [("DONE", 2)]
    (n, made_now) = at_start.execute("""SELECT count(*), bool_and(created_at <= setuhaul_now())
        FROM shipments WHERE shipment_id LIKE 'SHP-G%%'""").fetchone()
    assert n == 8 and made_now


def test_a_request_made_later_is_invisible_to_the_gps_feed_until_then(at_start):
    from setuhaul_jobs.gps import simulate

    scenario.generate(at_start, trucks=20, trickle=20, seed=3, now=START, coordinator_cases=0, disruptions=False)
    written = simulate.simulate(at_start, START)
    assert not any(k.startswith("SHP-G") for k in written)
