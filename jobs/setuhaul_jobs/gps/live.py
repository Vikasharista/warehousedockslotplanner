"""Keep the simulated GPS feed and the ETA service running against the shared clock.

    python -m setuhaul_jobs.gps.live [--every-seconds 30] [--scenario SHP1012=breakdown] ...

Each tick reads setuhaul_now(), writes the simulated pings up to that moment and recomputes the GPS ETAs, so the
solver's ETA trigger sees trucks drift as the simulated day goes on. Scenarios are the simulator's.
"""

from __future__ import annotations

import argparse
import os
import sys
import time

import psycopg

from . import eta, simulate


def tick(conn: psycopg.Connection, scenarios: dict[str, str]) -> tuple[str, int, int]:
    """One update. Returns (clock time, pings written, estimates written)."""
    (now,) = conn.execute("SELECT setuhaul_now()").fetchone()
    conn.commit()
    pings = simulate.simulate(conn, now, scenarios)
    estimates = eta.run(conn, now)
    return now.isoformat(timespec="seconds"), sum(pings.values()), sum(estimates.values())


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--database-url", default=os.environ.get("DATABASE_URL"))
    parser.add_argument("--every-seconds", type=float, default=30.0)
    parser.add_argument("--scenario", action="append", default=[], metavar="SHIPMENT=SCENARIO")
    parser.add_argument("--once", action="store_true", help="one tick, then exit")
    args = parser.parse_args(argv)
    if not args.database_url:
        parser.error("pass --database-url or set DATABASE_URL")
    scenarios = simulate.parse_scenarios(args.scenario)
    while True:
        try:
            with psycopg.connect(args.database_url) as conn:
                now, pings, estimates = tick(conn, scenarios)
            print(f"{now}  {pings} pings, {estimates} new ETA estimates", flush=True)
        except (ValueError, psycopg.Error) as exc:
            print(f"GPS tick failed: {exc}", file=sys.stderr, flush=True)
            if args.once:
                return 1
        if args.once:
            return 0
        time.sleep(args.every_seconds)


if __name__ == "__main__":
    sys.exit(main())
