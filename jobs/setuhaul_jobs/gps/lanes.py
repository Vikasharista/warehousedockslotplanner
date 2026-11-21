"""The three fixed lanes into FAC-JAI-01 and the towns that join them.

Route geometry comes from data/routes/<lane_id>.geojson (OpenRouteService driving-hgv, fetched once with
`python -m setuhaul_jobs.gps.lanes --fetch`); loading it needs no network.
"""

from __future__ import annotations

import argparse
import datetime as dt
import json
import os
import sys
import urllib.request
from dataclasses import dataclass
from pathlib import Path

import psycopg
from psycopg.types.json import Jsonb

from ..paths import REPO_ROOT
from .geo import Route, haversine_km

ROUTES_DIR = REPO_ROOT / "data" / "routes"
DESTINATION_FACILITY = "FAC-JAI-01"
# The snapshot gives no coordinates for the Jaipur DC; central Jaipur stands in for it.
DESTINATION = (26.9124, 75.7873)
DEFAULT_AVG_SPEED_KMPH = 45.0
# A town within this distance of a lane's route joins that lane part-way.
JOIN_RADIUS_KM = 15.0


@dataclass(frozen=True)
class Lane:
    lane_id: str
    name: str
    origin_city: str
    origin: tuple[float, float]

    @property
    def path(self) -> Path:
        return ROUTES_DIR / f"{self.lane_id}.geojson"


LANES = (
    Lane("L1-DEL-JAI", "Delhi to Jaipur DC", "Delhi", (28.6139, 77.2090)),
    Lane("L2-AMD-JAI", "Ahmedabad to Jaipur DC", "Ahmedabad", (23.0225, 72.5714)),
    Lane("L3-BOM-JAI", "Mumbai to Jaipur DC", "Mumbai", (19.0760, 72.8777)),
)

# Approximate town centres for shipment origins that may lie on a lane.
TOWNS: dict[str, tuple[float, float]] = {
    "Delhi": (28.6139, 77.2090),
    "Ahmedabad": (23.0225, 72.5714),
    "Mumbai": (19.0760, 72.8777),
    "Manesar": (28.3540, 76.9440),
    "Neemrana": (27.9870, 76.3860),
    "Bhiwadi": (28.2100, 76.8600),
    "Ghaziabad": (28.6692, 77.4538),
    "Noida": (28.5355, 77.3910),
    "Udaipur": (24.5854, 73.7125),
    "Ajmer": (26.4499, 74.6399),
    "Dausa": (26.8930, 76.3360),
    "Jodhpur": (26.2389, 73.0243),
    "Kota": (25.2138, 75.8648),
    "Jhunjhunu": (28.1289, 75.3995),
    "Ludhiana": (30.9010, 75.8573),
}


def load_route(lane: Lane) -> tuple[Route, dict]:
    data = json.loads(lane.path.read_text())
    feature = data["features"][0]
    return Route.from_lonlat(feature["geometry"]["coordinates"]), data


def all_routes() -> dict[str, Route]:
    return {lane.lane_id: load_route(lane)[0] for lane in LANES}


@dataclass(frozen=True)
class LaneMatch:
    lane_id: str
    start_km: float
    off_route_km: float


def match_origin(city: str, routes: dict[str, Route]) -> LaneMatch | None:
    """The lane a shipment from `city` travels on, and where it joins; None when no lane passes the town."""
    for lane in LANES:
        if lane.origin_city == city:
            return LaneMatch(lane.lane_id, 0.0, 0.0)
    town = TOWNS.get(city)
    if town is None:
        return None
    best: LaneMatch | None = None
    for lane in LANES:
        km, off, _ = routes[lane.lane_id].nearest_km(town)
        if off <= JOIN_RADIUS_KM and (best is None or off < best.off_route_km):
            best = LaneMatch(lane.lane_id, km, off)
    return best


def load_lanes(conn: psycopg.Connection) -> int:
    """Insert or refresh route_lanes from data/routes/. Returns the number of lanes."""
    n = 0
    for lane in LANES:
        route, data = load_route(lane)
        fetched = data.get("metadata", {}).get("timestamp")
        fetched_at = (dt.datetime.fromtimestamp(fetched / 1000, dt.timezone.utc) if fetched
                      else dt.datetime.fromtimestamp(lane.path.stat().st_mtime, dt.timezone.utc))
        conn.execute(
            """INSERT INTO route_lanes (lane_id, lane_name, origin_city, destination_facility_id, origin_lat, origin_lon,
                   destination_lat, destination_lon, distance_km, avg_speed_kmph, geometry, source, fetched_at)
               VALUES (%s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s)
               ON CONFLICT (lane_id) DO UPDATE SET lane_name = EXCLUDED.lane_name, origin_city = EXCLUDED.origin_city,
                   origin_lat = EXCLUDED.origin_lat, origin_lon = EXCLUDED.origin_lon,
                   destination_lat = EXCLUDED.destination_lat, destination_lon = EXCLUDED.destination_lon,
                   distance_km = EXCLUDED.distance_km, geometry = EXCLUDED.geometry, source = EXCLUDED.source,
                   fetched_at = EXCLUDED.fetched_at""",
            (lane.lane_id, lane.name, lane.origin_city, DESTINATION_FACILITY, lane.origin[0], lane.origin[1],
             DESTINATION[0], DESTINATION[1], round(route.length_km, 2), DEFAULT_AVG_SPEED_KMPH,
             Jsonb(data["features"][0]["geometry"]), "openrouteservice driving-hgv", fetched_at),
        )
        n += 1
    return n


def fetch_routes() -> None:
    """Fetch the three routes from OpenRouteService into data/routes/. Needs network access to the API and a key,
    which the session's proxy injects; ORS_API_KEY is sent when set (for use outside that setup)."""
    ROUTES_DIR.mkdir(parents=True, exist_ok=True)
    for lane in LANES:
        body = json.dumps({"coordinates": [[lane.origin[1], lane.origin[0]], [DESTINATION[1], DESTINATION[0]]],
                           "instructions": False}).encode()
        req = urllib.request.Request("https://api.openrouteservice.org/v2/directions/driving-hgv/geojson",
                                     data=body, method="POST", headers={"Content-Type": "application/json"})
        if os.environ.get("ORS_API_KEY"):
            req.add_header("Authorization", os.environ["ORS_API_KEY"])
        with urllib.request.urlopen(req, timeout=60) as resp:
            lane.path.write_bytes(resp.read())
        print(f"fetched {lane.lane_id}: {load_route(lane)[0].length_km:.1f} km")


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Load (or fetch) the fixed GPS lanes")
    parser.add_argument("--database-url", default=os.environ.get("DATABASE_URL"))
    parser.add_argument("--fetch", action="store_true", help="re-fetch routes from OpenRouteService first")
    args = parser.parse_args(argv)
    if args.fetch:
        fetch_routes()
    if not args.database_url:
        parser.error("pass --database-url or set DATABASE_URL")
    routes = all_routes()
    with psycopg.connect(args.database_url) as conn:
        n = load_lanes(conn)
    print(f"loaded {n} lanes")
    for city in sorted(TOWNS):
        m = match_origin(city, routes)
        if m:
            print(f"  {city:<10} -> {m.lane_id} from km {m.start_km:6.1f} ({m.off_route_km:.1f} km off route)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
