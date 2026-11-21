"""Seed Postgres from the supplied SQLite snapshot.

Copies every row of every table in one transaction, then checks that each Postgres table holds exactly
as many rows as its SQLite source. Running it again replaces the data, so it is safe to repeat.

    python -m setuhaul_jobs.seed --database-url postgresql://... [--apply-schema]

``DATABASE_URL`` is used when ``--database-url`` is not given.
"""

from __future__ import annotations

import argparse
import os
import sqlite3
import sys
from pathlib import Path

import psycopg
from psycopg import sql

from .paths import SCHEMA_SQL, SNAPSHOT_DB

SNAPSHOT_TIMEZONE = "Asia/Kolkata"

# Parents before children, so every foreign key already has its target when a row arrives.
TABLE_ORDER: tuple[str, ...] = (
    "vehicle_types",
    "carriers",
    "drivers",
    "vehicles",
    "facilities",
    "docks",
    "facility_contacts",
    "facility_rules",
    "shipments",
    "eta_updates",
    "appointment_slots",
    "appointments",
    "dock_status_events",
    "facility_checkins",
    "chat_threads",
    "chat_messages",
    "driver_exceptions",
    "operational_messages",
)

# Tables whose rows point at another row of the same table: (primary key, self-referencing column).
SELF_REFERENCES: dict[str, tuple[str, str]] = {
    "appointments": ("appointment_id", "replaced_appointment_id"),
    "operational_messages": ("operational_message_id", "reply_to_message_id"),
}


class SeedError(RuntimeError):
    """The snapshot and the Postgres schema disagree, or the copy is incomplete."""


def open_snapshot(path: Path = SNAPSHOT_DB) -> sqlite3.Connection:
    """Open the SQLite snapshot read-only, so the seed file can never be modified."""
    if not path.is_file():
        raise SeedError(f"snapshot not found: {path}")
    conn = sqlite3.connect(f"file:{path}?mode=ro", uri=True)
    conn.row_factory = sqlite3.Row
    return conn


def sqlite_tables(snapshot: sqlite3.Connection) -> set[str]:
    rows = snapshot.execute(
        "SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'"
    )
    return {r[0] for r in rows}


def postgres_columns(cur: psycopg.Cursor, table: str) -> list[str]:
    cur.execute(
        """
        SELECT column_name FROM information_schema.columns
        WHERE table_schema = current_schema() AND table_name = %s
        ORDER BY ordinal_position
        """,
        (table,),
    )
    return [r[0] for r in cur.fetchall()]


def order_self_referencing(rows: list[dict], key: str, parent: str) -> list[dict]:
    """Return rows so that a row always comes after the row its ``parent`` column points at."""
    by_key = {r[key]: r for r in rows}
    placed: set = set()
    ordered: list[dict] = []

    def place(row: dict, trail: frozenset) -> None:
        if row[key] in placed:
            return
        target = row[parent]
        if target is not None and target in by_key:
            if target in trail:
                raise SeedError(f"cycle in {parent} at {row[key]}")
            place(by_key[target], trail | {row[key]})
        placed.add(row[key])
        ordered.append(row)

    for row in rows:
        place(row, frozenset())
    return ordered


def apply_schema(conn: psycopg.Connection, schema_path: Path = SCHEMA_SQL) -> None:
    """Create tables, indexes and views in an empty database."""
    conn.execute(schema_path.read_text())


def load(conn: psycopg.Connection, snapshot: sqlite3.Connection) -> dict[str, int]:
    """Replace all Postgres rows with the snapshot's, in one transaction. Returns rows per table."""
    missing = set(TABLE_ORDER) - sqlite_tables(snapshot)
    if missing:
        raise SeedError(f"snapshot lacks tables: {sorted(missing)}")

    counts: dict[str, int] = {}
    with conn.transaction():
        cur = conn.cursor()
        cur.execute(sql.SQL("SET LOCAL TIME ZONE {}").format(sql.Literal(SNAPSHOT_TIMEZONE)))
        cur.execute(
            sql.SQL("TRUNCATE {} RESTART IDENTITY").format(
                sql.SQL(", ").join(sql.Identifier(t) for t in TABLE_ORDER)
            )
        )
        for table in TABLE_ORDER:
            pg_cols = postgres_columns(cur, table)
            if not pg_cols:
                raise SeedError(f"table {table} does not exist in Postgres; run with --apply-schema")
            lite_cols = [r["name"] for r in snapshot.execute(f'PRAGMA table_info("{table}")')]
            if set(pg_cols) != set(lite_cols):
                raise SeedError(
                    f"{table}: column mismatch, only in Postgres {sorted(set(pg_cols) - set(lite_cols))}, "
                    f"only in SQLite {sorted(set(lite_cols) - set(pg_cols))}"
                )
            rows = [dict(r) for r in snapshot.execute(f'SELECT * FROM "{table}"')]
            if table in SELF_REFERENCES:
                rows = order_self_referencing(rows, *SELF_REFERENCES[table])
            if rows:
                stmt = sql.SQL("INSERT INTO {} ({}) VALUES ({})").format(
                    sql.Identifier(table),
                    sql.SQL(", ").join(sql.Identifier(c) for c in pg_cols),
                    sql.SQL(", ").join(sql.Placeholder() for _ in pg_cols),
                )
                cur.executemany(stmt, [tuple(r[c] for c in pg_cols) for r in rows])
            counts[table] = len(rows)
        verify_counts(cur, snapshot)
    return counts


def verify_counts(cur: psycopg.Cursor, snapshot: sqlite3.Connection) -> None:
    """Fail when any Postgres table's row count differs from its SQLite source."""
    problems = []
    for table in TABLE_ORDER:
        (expected,) = snapshot.execute(f'SELECT count(*) FROM "{table}"').fetchone()
        cur.execute(sql.SQL("SELECT count(*) FROM {}").format(sql.Identifier(table)))
        (actual,) = cur.fetchone()
        if actual != expected:
            problems.append(f"{table}: SQLite {expected}, Postgres {actual}")
    if problems:
        raise SeedError("row counts differ: " + "; ".join(problems))


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--database-url", default=os.environ.get("DATABASE_URL"))
    parser.add_argument("--snapshot", type=Path, default=SNAPSHOT_DB)
    parser.add_argument("--apply-schema", action="store_true", help="create the schema first (empty database)")
    args = parser.parse_args(argv)
    if not args.database_url:
        parser.error("pass --database-url or set DATABASE_URL")

    snapshot = open_snapshot(args.snapshot)
    try:
        with psycopg.connect(args.database_url) as conn:
            if args.apply_schema:
                apply_schema(conn)
            counts = load(conn, snapshot)
    except (SeedError, psycopg.Error) as exc:
        print(f"seed failed: {exc}", file=sys.stderr)
        return 1
    finally:
        snapshot.close()

    width = max(len(t) for t in counts)
    for table, n in counts.items():
        print(f"{table:<{width}}  {n:>4}")
    print(f"{'total':<{width}}  {sum(counts.values()):>4}  rows, counts verified against the snapshot")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
