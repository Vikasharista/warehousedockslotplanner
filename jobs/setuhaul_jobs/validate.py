"""Validate a stored solver run with checks that share no code with the solver.

    python -m setuhaul_jobs.validate --run-id RUN-...        # one run
    python -m setuhaul_jobs.validate --latest FAC-JAI-01     # the facility's newest run

Each check is one query in db/validate/ that returns the violating rows. The run passes when every query
returns nothing. Exit status: 0 = valid, 1 = violations found, 2 = run not found or database error.
Checks run in one read-only transaction; the validator never writes.
"""

from __future__ import annotations

import argparse
import os
import re
import sys
from dataclasses import dataclass
from pathlib import Path

import psycopg

from .paths import VALIDATE_DIR

HEADER = re.compile(r"^--\s*(id|title):\s*(.+?)\s*$")


@dataclass(frozen=True)
class Check:
    id: str
    title: str
    sql: str


@dataclass(frozen=True)
class Violation:
    check_id: str
    title: str
    shipment_id: str | None
    detail: str


def load_checks(directory: Path = VALIDATE_DIR) -> list[Check]:
    checks = []
    for path in sorted(directory.glob("*.sql")):
        meta = {}
        for line in path.read_text().splitlines():
            m = HEADER.match(line)
            if m:
                meta[m.group(1)] = m.group(2)
        if {"id", "title"} - meta.keys():
            raise ValueError(f"{path.name}: needs '-- id:' and '-- title:' header lines")
        checks.append(Check(meta["id"], meta["title"], path.read_text()))
    return checks


def validate(conn: psycopg.Connection, run_id: str, checks: list[Check] | None = None) -> list[Violation]:
    """All violations for one run; an empty list means the run is valid."""
    checks = checks if checks is not None else load_checks()
    found: list[Violation] = []
    with conn.transaction():
        conn.execute("SET TRANSACTION READ ONLY")
        if conn.execute("SELECT 1 FROM solver_run WHERE run_id = %s", (run_id,)).fetchone() is None:
            raise LookupError(f"no run {run_id}")
        for check in checks:
            for shipment_id, detail in conn.execute(check.sql, {"run_id": run_id}).fetchall():
                found.append(Violation(check.id, check.title, shipment_id, detail))
    return found


def latest_run(conn: psycopg.Connection, facility_id: str) -> str:
    row = conn.execute(
        "SELECT run_id FROM solver_run WHERE facility_id = %s ORDER BY created_at DESC LIMIT 1", (facility_id,)
    ).fetchone()
    if row is None:
        raise LookupError(f"no runs for {facility_id}")
    return row[0]


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--database-url", default=os.environ.get("DATABASE_URL"))
    target = parser.add_mutually_exclusive_group(required=True)
    target.add_argument("--run-id")
    target.add_argument("--latest", metavar="FACILITY_ID")
    args = parser.parse_args(argv)
    if not args.database_url:
        parser.error("pass --database-url or set DATABASE_URL")
    try:
        with psycopg.connect(args.database_url) as conn:
            run_id = args.run_id or latest_run(conn, args.latest)
            violations = validate(conn, run_id)
    except (LookupError, psycopg.Error) as exc:
        print(f"validation could not run: {exc}", file=sys.stderr)
        return 2
    if not violations:
        print(f"{run_id}: valid ({len(load_checks())} checks passed)")
        return 0
    print(f"{run_id}: {len(violations)} violation(s)")
    for v in violations:
        print(f"  {v.check_id}  {v.shipment_id or '-':<8}  {v.detail}   [{v.title}]")
    return 1


if __name__ == "__main__":
    raise SystemExit(main())
