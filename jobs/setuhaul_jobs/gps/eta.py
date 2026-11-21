"""ETA service: turns GPS pings into ETA estimates (gps_eta_estimates).

    python -m setuhaul_jobs.gps.eta --as-of 2026-08-04T09:55:00+05:30

Replays the service's 5-minute cycle from each truck's first ping up to --as-of. At each tick it takes the
latest ping, measures the remaining route distance, and estimates ETA = ping time + remaining km / the lane's
average speed. An estimate is written only when it moved by 10 minutes or more since the last one written.
Confidence follows ping age: up to 10 minutes HIGH, up to 30 MEDIUM. When no ping arrives for 30 minutes the
last ETA is kept, marked stale with LOW confidence, once. All limits are the confirmed defaults and can be
changed with flags. Earlier estimates of the processed shipments are replaced, so re-running is safe.
"""

from __future__ import annotations

import argparse
import datetime as dt
import os
import sys
from dataclasses import dataclass

import psycopg

from .geo import Route

LANES_SQL = "SELECT lane_id, geometry, avg_speed_kmph FROM route_lanes"
PINGS_SQL = """
SELECT ping_id, shipment_id, vehicle_id, lane_id, recorded_at, lat, lon
FROM vehicle_gps_pings
WHERE shipment_id IS NOT NULL AND lane_id IS NOT NULL AND recorded_at <= %s
ORDER BY shipment_id, recorded_at, ping_id
"""


@dataclass(frozen=True)
class EtaSettings:
    interval_min: int = 5
    write_threshold_min: int = 10
    stale_min: int = 30
    high_confidence_max_age_min: int = 10


def confidence(age_min: float, s: EtaSettings) -> str:
    if age_min <= s.high_confidence_max_age_min:
        return "HIGH"
    if age_min <= s.stale_min:
        return "MEDIUM"
    return "LOW"


def estimates_for(pings: list[dict], route: Route, speed_kmph: float, as_of: dt.datetime,
                  s: EtaSettings) -> list[dict]:
    """The estimates the service would have written for one shipment, tick by tick, up to `as_of`."""
    out: list[dict] = []
    if not pings:
        return out
    tick = pings[0]["recorded_at"]
    i, hint, last_written, stale_written = 0, None, None, False
    km_cache: dict[int, float] = {}
    while tick <= as_of:
        while i + 1 < len(pings) and pings[i + 1]["recorded_at"] <= tick:
            i += 1
        ping = pings[i]
        age = (tick - ping["recorded_at"]).total_seconds() / 60
        if age > s.stale_min:
            if last_written is not None and not stale_written:
                out.append({**last_written, "confidence_code": "LOW", "stale": True, "computed_at": tick})
                stale_written = True
        else:
            stale_written = False
            if ping["ping_id"] not in km_cache:
                km, _, hint = route.nearest_km((ping["lat"], ping["lon"]), hint_index=hint)
                km_cache[ping["ping_id"]] = km
            remaining = max(0.0, route.length_km - km_cache[ping["ping_id"]])
            eta = ping["recorded_at"] + dt.timedelta(hours=remaining / speed_kmph)
            moved = (last_written is None
                     or abs((eta - last_written["eta_ts"]).total_seconds()) >= s.write_threshold_min * 60)
            if moved:
                last_written = {"shipment_id": ping["shipment_id"], "vehicle_id": ping["vehicle_id"],
                                "lane_id": ping["lane_id"], "ping_id": ping["ping_id"],
                                "remaining_km": round(remaining, 2), "eta_ts": eta,
                                "confidence_code": confidence(age, s), "stale": False, "computed_at": tick}
                out.append(last_written)
        tick += dt.timedelta(minutes=s.interval_min)
    return out


def run(conn: psycopg.Connection, as_of: dt.datetime, s: EtaSettings = EtaSettings()) -> dict[str, int]:
    """Recompute and store estimates for every shipment with pings. Returns estimates written per shipment."""
    written: dict[str, int] = {}
    with conn.transaction():
        cur = conn.cursor(row_factory=psycopg.rows.dict_row)
        lanes = {r["lane_id"]: (Route.from_lonlat(r["geometry"]["coordinates"]), float(r["avg_speed_kmph"]))
                 for r in cur.execute(LANES_SQL).fetchall()}
        by_shipment: dict[str, list[dict]] = {}
        for p in cur.execute(PINGS_SQL, (as_of,)).fetchall():
            by_shipment.setdefault(p["shipment_id"], []).append(p)
        for shipment, pings in by_shipment.items():
            route, speed = lanes[pings[0]["lane_id"]]
            rows = estimates_for(pings, route, speed, as_of, s)
            conn.execute("DELETE FROM gps_eta_estimates WHERE shipment_id = %s", (shipment,))
            cur.executemany(
                """INSERT INTO gps_eta_estimates (shipment_id, vehicle_id, lane_id, ping_id, remaining_km, eta_ts,
                       confidence_code, stale, computed_at)
                   VALUES (%(shipment_id)s, %(vehicle_id)s, %(lane_id)s, %(ping_id)s, %(remaining_km)s, %(eta_ts)s,
                           %(confidence_code)s, %(stale)s, %(computed_at)s)""",
                rows,
            )
            written[shipment] = len(rows)
    return written


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--database-url", default=os.environ.get("DATABASE_URL"))
    parser.add_argument("--as-of", required=True, type=dt.datetime.fromisoformat)
    parser.add_argument("--interval-min", type=int, default=EtaSettings.interval_min)
    parser.add_argument("--write-threshold-min", type=int, default=EtaSettings.write_threshold_min)
    parser.add_argument("--stale-min", type=int, default=EtaSettings.stale_min)
    args = parser.parse_args(argv)
    if not args.database_url:
        parser.error("pass --database-url or set DATABASE_URL")
    if args.as_of.tzinfo is None:
        parser.error("--as-of needs an offset, e.g. +05:30")
    s = EtaSettings(args.interval_min, args.write_threshold_min, args.stale_min)
    with psycopg.connect(args.database_url) as conn:
        written = run(conn, args.as_of, s)
    for shipment, n in written.items():
        print(f"{shipment}  {n:>3} estimates")
    return 0


if __name__ == "__main__":
    sys.exit(main())
