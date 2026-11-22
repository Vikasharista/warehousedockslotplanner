"""Simulated GPS pings for trucks travelling the fixed lanes to FAC-JAI-01.

    python -m setuhaul_jobs.gps.simulate --until 2026-08-04T09:55:00+05:30 [--scenario SHP1012=breakdown] ...

Every truck not yet at the gate whose origin lies on a lane moves along the stored route from its departure
time and reports one ping per simulated minute up to --until. Runs are deterministic: the same snapshot,
scenarios and --until always write the same pings. Earlier SIMULATOR pings of the simulated shipments are
replaced, so re-running is safe.

Scenarios (default `as_declared`):
  as_declared        arrive at the latest ETA the snapshot holds; a BREAKDOWN delay becomes a stop
  on_time            arrive at the original ETA
  early:<min>        arrive <min> minutes before the original ETA
  breakdown          stop for the declared delay (latest ETA - original ETA) at 60% of the remaining route
  slowdown:<min>     arrive <min> minutes after the original ETA, moving slower the whole way
  feed_loss:<HH:MM>  as_declared, but the device stops reporting at that local time
"""

from __future__ import annotations

import argparse
import datetime as dt
import os
import sys
from dataclasses import dataclass

import psycopg

from .geo import Route
from .lanes import DESTINATION_FACILITY, all_routes, match_origin

PING_INTERVAL_MIN = 1
BREAKDOWN_AT_FRACTION = 0.6


@dataclass(frozen=True)
class Trip:
    shipment_id: str
    vehicle_id: str
    lane_id: str
    start_km: float
    depart: dt.datetime
    arrive: dt.datetime
    stop_at_km: float | None = None
    stop_minutes: float = 0.0
    feed_lost_at: dt.datetime | None = None

    @property
    def moving_minutes(self) -> float:
        return max(1.0, (self.arrive - self.depart).total_seconds() / 60 - self.stop_minutes)


def plan_trip(row: dict, scenario: str, routes: dict[str, Route], zone: dt.tzinfo) -> Trip | None:
    match = match_origin(row["origin_city"], routes)
    if match is None:
        return None
    depart = row["actual_departure_ts"] or row["planned_departure_ts"]
    original, latest = row["original_eta_ts"], row["latest_eta_ts"] or row["original_eta_ts"]
    remaining = routes[match.lane_id].length_km - match.start_km
    kind, _, arg = scenario.partition(":")
    stop_at, stop_min, arrive, feed_lost = None, 0.0, latest, None

    if kind in ("as_declared", "feed_loss", "breakdown"):
        delay = (latest - original).total_seconds() / 60
        breakdown = kind == "breakdown" or row["delay_reason_code"] == "BREAKDOWN"
        if breakdown and delay > 0:
            stop_at = match.start_km + remaining * BREAKDOWN_AT_FRACTION
            stop_min = delay
        if kind == "feed_loss":
            h, m = (int(x) for x in arg.split(":"))
            local = depart.astimezone(zone)
            feed_lost = local.replace(hour=h, minute=m, second=0, microsecond=0)
            if feed_lost < local:
                feed_lost += dt.timedelta(days=1)
    elif kind == "on_time":
        arrive = original
    elif kind == "early":
        arrive = original - dt.timedelta(minutes=int(arg))
    elif kind == "slowdown":
        arrive = original + dt.timedelta(minutes=int(arg))
    else:
        raise ValueError(f"unknown scenario {scenario!r}")
    if arrive <= depart:
        raise ValueError(f"{row['shipment_id']}: arrival {arrive} is not after departure {depart}")
    return Trip(row["shipment_id"], row["vehicle_id"], match.lane_id, match.start_km, depart, arrive,
                stop_at, stop_min, feed_lost)


def position(trip: Trip, route: Route, at: dt.datetime) -> tuple[float, float]:
    """(km along the route, speed km/h) at time `at`."""
    speed = (route.length_km - trip.start_km) / (trip.moving_minutes / 60)
    elapsed = (at - trip.depart).total_seconds() / 60
    if trip.stop_at_km is not None:
        reach_stop = (trip.stop_at_km - trip.start_km) / speed * 60
        if elapsed < reach_stop:
            return trip.start_km + speed * elapsed / 60, speed
        if elapsed < reach_stop + trip.stop_minutes:
            return trip.stop_at_km, 0.0
        elapsed -= trip.stop_minutes
    km = trip.start_km + speed * elapsed / 60
    if km >= route.length_km:
        return route.length_km, 0.0
    return km, speed


def pings(trip: Trip, route: Route, until: dt.datetime) -> list[tuple]:
    end = min(until, trip.arrive)
    if trip.feed_lost_at is not None:
        end = min(end, trip.feed_lost_at)
    out = []
    t = trip.depart
    while t <= end:
        km, speed = position(trip, route, t)
        (lat, lon), heading = route.position_at(km)
        out.append((trip.vehicle_id, trip.shipment_id, trip.lane_id, t, round(lat, 6), round(lon, 6),
                    round(speed, 1), round(heading, 1) % 360))
        t += dt.timedelta(minutes=PING_INTERVAL_MIN)
    return out


SHIPMENTS_SQL = """
SELECT s.shipment_id, s.vehicle_id, s.origin_city, s.planned_departure_ts, s.actual_departure_ts,
       s.original_eta_ts, le.effective_eta_ts AS latest_eta_ts, le.delay_reason_code
FROM shipments s
JOIN v_latest_eta le ON le.shipment_id = s.shipment_id
LEFT JOIN facility_checkins fc ON fc.shipment_id = s.shipment_id
WHERE s.destination_facility_id = %s
  AND s.current_status IN ('PLANNED', 'ASSIGNED', 'IN_TRANSIT')
  AND fc.gate_in_ts IS NULL
  AND s.created_at <= %s
ORDER BY s.shipment_id
"""


def simulate(conn: psycopg.Connection, until: dt.datetime, scenarios: dict[str, str] | None = None) -> dict[str, int]:
    """Write pings up to `until`. Returns pings written per shipment."""
    scenarios = scenarios or {}
    routes = all_routes()
    zone = dt.timezone(dt.timedelta(hours=5, minutes=30))
    written: dict[str, int] = {}
    with conn.transaction():
        cur = conn.cursor(row_factory=psycopg.rows.dict_row)
        rows = cur.execute(SHIPMENTS_SQL, (DESTINATION_FACILITY, until)).fetchall()
        trips = [t for t in (plan_trip(r, scenarios.get(r["shipment_id"], "as_declared"), routes, zone)
                             for r in rows) if t is not None]
        unknown = set(scenarios) - {t.shipment_id for t in trips}
        if unknown:
            raise ValueError(f"no simulated trip for {sorted(unknown)} (not in transit to the DC on a lane)")
        for trip in trips:
            conn.execute("DELETE FROM vehicle_gps_pings WHERE shipment_id = %s AND source = 'SIMULATOR'",
                         (trip.shipment_id,))
            batch = pings(trip, routes[trip.lane_id], until)
            if batch:
                cur.executemany(
                    """INSERT INTO vehicle_gps_pings (vehicle_id, shipment_id, lane_id, recorded_at, lat, lon,
                           speed_kmph, heading_deg, source, received_at)
                       VALUES (%s, %s, %s, %s, %s, %s, %s, %s, 'SIMULATOR', %s)""",
                    [p + (p[3],) for p in batch],
                )
            written[trip.shipment_id] = len(batch)
    return written


def parse_scenarios(values: list[str]) -> dict[str, str]:
    out = {}
    for v in values:
        shipment, sep, scenario = v.partition("=")
        if not sep:
            raise ValueError(f"--scenario must be SHIPMENT=SCENARIO, got {v!r}")
        out[shipment] = scenario
    return out


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--database-url", default=os.environ.get("DATABASE_URL"))
    parser.add_argument("--until", required=True, type=dt.datetime.fromisoformat,
                        help="simulate up to this time, e.g. 2026-08-04T09:55:00+05:30")
    parser.add_argument("--scenario", action="append", default=[], metavar="SHIPMENT=SCENARIO")
    args = parser.parse_args(argv)
    if not args.database_url:
        parser.error("pass --database-url or set DATABASE_URL")
    if args.until.tzinfo is None:
        parser.error("--until needs an offset, e.g. +05:30")
    try:
        with psycopg.connect(args.database_url) as conn:
            written = simulate(conn, args.until, parse_scenarios(args.scenario))
    except (ValueError, psycopg.Error) as exc:
        print(f"simulation failed: {exc}", file=sys.stderr)
        return 1
    for shipment, n in written.items():
        print(f"{shipment}  {n:>5} pings")
    print(f"total {sum(written.values())} pings up to {args.until.isoformat()}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
