"""Reset the demo every night: reload the snapshot, restart the clock at 09:55 IST and load the load test again.

    python -m setuhaul_jobs.demo_reset [--at 03:00] [--tz Asia/Kolkata]     # waits, resets every night
    python -m setuhaul_jobs.demo_reset --now                                # one reset, then exit

The demo's data covers one day (4 Aug 2026, slots 08:00-21:00), so the shared clock runs out of slots about 11 hours
after a start. Each reset makes the next review day start the same way. Demo accounts (app_users) are kept; solver
runs, bookings made since, GPS pings and queued truck requests go with the reloaded snapshot.
"""

from __future__ import annotations

import argparse
import datetime as dt
import os
import sys
import time
from pathlib import Path
from zoneinfo import ZoneInfo

import psycopg

from . import scenario, seed
from .paths import SNAPSHOT_DB


def reset(conn: psycopg.Connection, snapshot_path: Path = SNAPSHOT_DB, *, trucks: int = 70, trickle: int = 20,
          seed_value: int = 42) -> str:
    """Reload the snapshot, restart the clock and generate the load test. Returns a one-line summary."""
    snapshot = seed.open_snapshot(snapshot_path)
    try:
        counts = seed.load(conn, snapshot)
    finally:
        snapshot.close()
    seed.set_clock(conn, seed.SNAPSHOT_MOMENT)
    summary = ""
    if trucks > 0:
        summary = "; " + str(scenario.generate(conn, trucks=trucks, trickle=min(trickle, trucks), seed=seed_value))
    (accounts,) = conn.execute("SELECT count(*) FROM app_users").fetchone()
    return (f"reset: {sum(counts.values())} snapshot rows, clock {seed.SNAPSHOT_MOMENT}{summary}; "
            f"{accounts} accounts kept")


def next_run(now: dt.datetime, at: dt.time, zone: ZoneInfo) -> dt.datetime:
    """The next moment the wall clock in `zone` reads `at`, strictly after `now`."""
    local = now.astimezone(zone)
    candidate = dt.datetime.combine(local.date(), at, tzinfo=zone)
    if candidate <= local:
        candidate = dt.datetime.combine(local.date() + dt.timedelta(days=1), at, tzinfo=zone)
    return candidate


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--database-url", default=os.environ.get("DATABASE_URL"))
    parser.add_argument("--at", type=dt.time.fromisoformat, default=dt.time(3, 0), help="local time, default 03:00")
    parser.add_argument("--tz", default="Asia/Kolkata")
    parser.add_argument("--now", action="store_true", help="reset once now, then exit")
    parser.add_argument("--trucks", type=int, default=int(os.environ.get("SETUHAUL_SCENARIO_TRUCKS", "70")))
    parser.add_argument("--trickle", type=int, default=int(os.environ.get("SETUHAUL_SCENARIO_TRICKLE", "20")))
    args = parser.parse_args(argv)
    if not args.database_url:
        parser.error("pass --database-url or set DATABASE_URL")
    zone = ZoneInfo(args.tz)

    def run_once() -> bool:
        try:
            with psycopg.connect(args.database_url) as conn:
                print(reset(conn, trucks=args.trucks, trickle=args.trickle), flush=True)
            return True
        except (ValueError, psycopg.Error, seed.SeedError) as exc:
            print(f"demo reset failed: {exc}", file=sys.stderr, flush=True)
            return False

    if args.now:
        return 0 if run_once() else 1
    while True:
        due = next_run(dt.datetime.now(dt.timezone.utc), args.at, zone)
        print(f"next demo reset {due.isoformat(timespec='minutes')}", flush=True)
        while dt.datetime.now(dt.timezone.utc) < due:
            time.sleep(min(300.0, max(1.0, (due - dt.datetime.now(dt.timezone.utc)).total_seconds())))
        run_once()


if __name__ == "__main__":
    sys.exit(main())
