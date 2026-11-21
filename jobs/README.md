# jobs

Python jobs for the SetuHaul planner. Phase 1: the Postgres seed loader and the data audit.
Phase 3 adds the GPS simulator and the ETA service.

## Setup

```bash
python3 -m venv .venv
.venv/bin/pip install -r jobs/requirements.txt
docker compose up -d db
export DATABASE_URL=postgresql://postgres:postgres@localhost:55432/setuhaul
```

## Seed Postgres from the snapshot

```bash
cd jobs
../.venv/bin/python -m setuhaul_jobs.seed --apply-schema   # first time, empty database
../.venv/bin/python -m setuhaul_jobs.seed                  # later: replace all rows
```

The loader copies all 18 tables in one transaction, in foreign-key order, and fails if any table's
row count differs from the SQLite file. The SQLite file is opened read-only.

## Run the data audit

```bash
cd jobs
../.venv/bin/python -m setuhaul_jobs.audit                  # writes ../docs/data-audit.md
```

Each finding is one query in `db/audit/`, with `-- id`, `-- title` and `-- impact` header lines.

## Validate a solver run

```bash
cd jobs
../.venv/bin/python -m setuhaul_jobs.validate --run-id RUN-...        # or --latest FAC-JAI-01
```

Eight checks in `db/validate/`, written without any solver code: no dock overlap (V1), no slot used twice (V2),
no clash with bookings the run does not replace or with trucks in a dock (V3), dock fits the truck (V4), no start
before the truck can be there, recomputed from the snapshot with the run's own buffers (V5), slot open and dock
not blocked, closed or past its last start (V6), times match the slot and unload length (V7), run header matches
its items (V8). Exit code 0 = valid, 1 = violations, 2 = run not found or database error.

## Tests

```bash
cd jobs
TEST_DATABASE_URL=postgresql://postgres:postgres@localhost:55432/postgres ../.venv/bin/python -m pytest
```

Each test session creates and drops its own database. The suite checks:
- row counts per table, and that loading twice gives the same result;
- the four views return the same rows on Postgres as on SQLite;
- every audit finding reproduces, and `docs/data-audit.md` matches a fresh run;
- the database rejects a second active booking for a slot or for a shipment, and when ten
  connections race for one free slot, exactly one wins;
- the validator accepts a correct run and catches each kind of broken run with the right check.
