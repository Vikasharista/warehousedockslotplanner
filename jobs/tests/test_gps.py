import datetime as dt
import json

import pytest

from setuhaul_jobs import validate
from setuhaul_jobs.gps import eta, lanes, simulate
from setuhaul_jobs.gps.geo import Route, haversine_km

IST = dt.timezone(dt.timedelta(hours=5, minutes=30))
MORNING = dt.datetime(2026, 8, 4, 9, 55, tzinfo=IST)


@pytest.fixture(scope="module")
def routes():
    return lanes.all_routes()


# --- geometry and lanes (no database) ---

def test_haversine_delhi_to_jaipur_is_about_235_km_as_the_crow_flies():
    assert 230 < haversine_km(lanes.TOWNS["Delhi"], lanes.DESTINATION) < 240


def test_route_lengths_match_what_openrouteservice_reported(routes):
    for lane in lanes.LANES:
        reported = json.loads(lane.path.read_text())["features"][0]["properties"]["summary"]["distance"] / 1000
        assert routes[lane.lane_id].length_km == pytest.approx(reported, rel=0.01)


def test_route_ends_and_positions(routes):
    r = routes["L1-DEL-JAI"]
    start, _ = r.position_at(0)
    end, _ = r.position_at(r.length_km + 50)  # clamped
    assert haversine_km(start, lanes.TOWNS["Delhi"]) < 2
    assert haversine_km(end, lanes.DESTINATION) < 5
    mid, heading = r.position_at(r.length_km / 2)
    km, off, _ = r.nearest_km(mid)
    assert km == pytest.approx(r.length_km / 2, abs=1.0)
    assert off < 0.5
    assert 0 <= heading < 360


@pytest.mark.parametrize(("town", "lane"), [
    ("Delhi", "L1-DEL-JAI"), ("Manesar", "L1-DEL-JAI"), ("Neemrana", "L1-DEL-JAI"),
    ("Ahmedabad", "L2-AMD-JAI"), ("Udaipur", "L2-AMD-JAI"), ("Ajmer", "L2-AMD-JAI"), ("Mumbai", "L3-BOM-JAI"),
])
def test_towns_on_a_lane_join_it(routes, town, lane):
    assert lanes.match_origin(town, routes).lane_id == lane


@pytest.mark.parametrize("town", ["Kota", "Dausa", "Jodhpur", "Ludhiana", "Atlantis"])
def test_towns_off_the_lanes_get_no_gps(routes, town):
    assert lanes.match_origin(town, routes) is None


def test_eta_confidence_follows_ping_age():
    s = eta.EtaSettings()
    assert [eta.confidence(a, s) for a in (0, 10, 11, 30, 31)] == ["HIGH", "HIGH", "MEDIUM", "MEDIUM", "LOW"]


def _pings(route, start, minutes, km_per_min, gap=None):
    """Synthetic pings, one per minute; `gap` = (from_min, to_min) with no pings."""
    out = []
    for m in range(minutes):
        if gap and gap[0] <= m < gap[1]:
            continue
        (lat, lon), _ = route.position_at(m * km_per_min)
        out.append({"ping_id": m, "shipment_id": "S", "vehicle_id": "V", "lane_id": "L",
                    "recorded_at": start + dt.timedelta(minutes=m), "lat": lat, "lon": lon})
    return out


def test_eta_service_writes_only_on_a_10_minute_change(routes):
    r = routes["L1-DEL-JAI"]
    start = dt.datetime(2026, 8, 4, 6, 0, tzinfo=IST)
    # Moving at exactly the lane speed: the ETA never changes, so one estimate is written.
    steady = eta.estimates_for(_pings(r, start, 120, 45 / 60), r, 45, start + dt.timedelta(minutes=120), eta.EtaSettings())
    assert len(steady) == 1
    # Stopped: every tick pushes the ETA 5 minutes later, so an estimate is written every other tick.
    stopped = eta.estimates_for(_pings(r, start, 60, 0), r, 45, start + dt.timedelta(minutes=60), eta.EtaSettings())
    gaps = [(b["eta_ts"] - a["eta_ts"]).total_seconds() / 60 for a, b in zip(stopped, stopped[1:])]
    assert gaps and all(g >= 10 for g in gaps)


def test_a_silent_device_is_marked_stale_once_and_keeps_its_last_eta(routes):
    r = routes["L1-DEL-JAI"]
    start = dt.datetime(2026, 8, 4, 6, 0, tzinfo=IST)
    pings = _pings(r, start, 120, 45 / 60, gap=(20, 120))
    out = eta.estimates_for(pings, r, 45, start + dt.timedelta(minutes=120), eta.EtaSettings())
    stale = [e for e in out if e["stale"]]
    assert len(stale) == 1
    assert stale[0]["confidence_code"] == "LOW"
    assert stale[0]["eta_ts"] == out[-2]["eta_ts"]
    assert (stale[0]["computed_at"] - start).total_seconds() / 60 > 19 + 30


# --- simulator, ETA service and view (seeded database) ---

def _simulate(conn, until=MORNING, scenarios=None):
    written = simulate.simulate(conn, until, scenarios)
    eta.run(conn, until)
    return written


def _rows(conn, sql, *args):
    return conn.execute(sql, args).fetchall()


def test_simulation_is_repeatable_and_replaces_its_own_pings(seeded):
    first = _simulate(seeded)
    pings_1 = _rows(seeded, "SELECT shipment_id, recorded_at, lat, lon FROM vehicle_gps_pings ORDER BY 1, 2")
    second = _simulate(seeded)
    pings_2 = _rows(seeded, "SELECT shipment_id, recorded_at, lat, lon FROM vehicle_gps_pings ORDER BY 1, 2")
    assert first == second
    assert pings_1 == pings_2
    # Only trucks on a lane that are not yet at the gate are simulated.
    assert set(first) == {"SHP1007", "SHP1009", "SHP1010", "SHP1011", "SHP1012", "SHP1015", "SHP1016",
                          "SHP1017", "SHP1018"}
    assert first["SHP1015"] == 0  # departs after 09:55


def test_on_time_trucks_reach_the_dc_at_their_original_eta(seeded):
    late = dt.datetime(2026, 8, 4, 23, 0, tzinfo=IST)
    simulate.simulate(seeded, late, {"SHP1009": "on_time"})
    last = seeded.execute(
        """SELECT p.recorded_at, p.lat, p.lon, s.original_eta_ts FROM vehicle_gps_pings p
           JOIN shipments s USING (shipment_id) WHERE shipment_id = 'SHP1009' ORDER BY recorded_at DESC LIMIT 1"""
    ).fetchone()
    assert abs((last[0] - last[3]).total_seconds()) <= 60
    assert haversine_km((last[1], last[2]), lanes.DESTINATION) < 5


def test_a_declared_breakdown_becomes_a_stop(seeded):
    late = dt.datetime(2026, 8, 4, 23, 0, tzinfo=IST)
    simulate.simulate(seeded, late)
    # SHP1012 declared a BREAKDOWN: 11:10 against an original 09:45, so 85 minutes standing still.
    (stopped,) = seeded.execute(
        "SELECT count(*) FROM vehicle_gps_pings WHERE shipment_id = 'SHP1012' AND speed_kmph = 0"
    ).fetchone()
    assert 84 <= stopped <= 87


def test_feed_loss_stops_the_pings(seeded):
    simulate.simulate(seeded, MORNING, {"SHP1016": "feed_loss:08:00"})
    (last,) = seeded.execute(
        "SELECT max(recorded_at) FROM vehicle_gps_pings WHERE shipment_id = 'SHP1016'").fetchone()
    assert last.astimezone(IST).strftime("%H:%M") == "08:00"
    eta.run(seeded, MORNING)
    (stale,) = seeded.execute(
        "SELECT bool_or(stale) FROM gps_eta_estimates WHERE shipment_id = 'SHP1016'").fetchone()
    assert stale


def test_an_unknown_scenario_target_is_rejected(seeded):
    with pytest.raises(ValueError, match="SHP1020"):
        simulate.simulate(seeded, MORNING, {"SHP1020": "on_time"})  # from Kota: on no lane


def test_effective_eta_prefers_gps_but_respects_the_rules(seeded):
    _simulate(seeded)
    eff = dict(_rows(seeded, "SELECT shipment_id, eta_source FROM v_effective_eta"))
    assert eff["SHP1009"] == "GPS"
    assert eff["SHP1020"] == "ORIGINAL_PLAN"  # Kota: no lane, so no GPS

    # An open driver exception with a later declared ETA beats GPS.
    gps_eta = seeded.execute(
        "SELECT eta_ts FROM gps_eta_estimates WHERE shipment_id = 'SHP1009' ORDER BY computed_at DESC LIMIT 1"
    ).fetchone()[0]
    seeded.execute(
        """INSERT INTO eta_updates (eta_update_id, shipment_id, source_type, declared_eta_ts, confidence_code, created_at)
           VALUES ('ETA-T-DRV', 'SHP1009', 'DRIVER_DECLARED', %s, 'HIGH', '2026-08-04T09:30:00+05:30')""",
        (gps_eta + dt.timedelta(hours=2),),
    )
    seeded.execute(
        """INSERT INTO driver_exceptions (exception_id, shipment_id, driver_id, thread_id, exception_type, reported_at,
               exception_status, description)
           SELECT 'EXC-T', 'SHP1009', s.driver_id, (SELECT min(thread_id) FROM chat_threads), 'DELAY',
                  '2026-08-04T09:30:00+05:30', 'OPEN', 'test' FROM shipments s WHERE s.shipment_id = 'SHP1009'"""
    )
    row = seeded.execute(
        "SELECT eta_source, effective_eta_ts FROM v_effective_eta WHERE shipment_id = 'SHP1009'").fetchone()
    assert row == ("DRIVER_DECLARED", gps_eta + dt.timedelta(hours=2))

    # Once the exception is resolved, GPS alone applies.
    seeded.execute("UPDATE driver_exceptions SET exception_status = 'RESOLVED' WHERE exception_id = 'EXC-T'")
    assert seeded.execute(
        "SELECT eta_source FROM v_effective_eta WHERE shipment_id = 'SHP1009'").fetchone()[0] == "GPS"

    # An operations override always wins.
    seeded.execute(
        """INSERT INTO eta_updates (eta_update_id, shipment_id, source_type, declared_eta_ts, confidence_code, created_at)
           VALUES ('ETA-T-OVR', 'SHP1009', 'OPERATIONS_OVERRIDE', '2026-08-04T16:00:00+05:30', 'HIGH',
                   '2026-08-04T09:40:00+05:30')"""
    )
    assert seeded.execute(
        "SELECT eta_source FROM v_effective_eta WHERE shipment_id = 'SHP1009'").fetchone()[0] == "OPERATIONS_OVERRIDE"


def test_validator_uses_gps_etas(seeded):
    """A slot that fits the declared ETA but not the later GPS ETA is caught by V5."""
    from test_validate import store_run

    seeded.execute(
        """INSERT INTO gps_eta_estimates (shipment_id, vehicle_id, lane_id, remaining_km, eta_ts, confidence_code,
               computed_at)
           SELECT 'SHP1012', vehicle_id, 'L1-DEL-JAI', 100, '2026-08-04T13:00:00+05:30', 'HIGH',
                  '2026-08-04T09:50:00+05:30' FROM shipments WHERE shipment_id = 'SHP1012'"""
    )
    # Declared ETA 11:10 would allow 13:00; the GPS ETA 13:00 + 30 min does not.
    run = store_run(seeded, "RUN-T-GPS", [("SHP1012", "D4", "13:00", None)])
    assert sorted({v.check_id for v in validate.validate(seeded, run)}) == ["V5"]
