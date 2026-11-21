# db

- `schema.sql` — Postgres 16 port of `data/setuhaul/setuhaul_schema_and_seed.sql`: the same 18 tables,
  CHECK constraints, foreign keys, partial unique indexes (double-booking protection) and four views.
  Timestamps become `timestamptz`, facility hours `time`, rule dates `date`; 0/1 flags stay integers so
  queries written against the snapshot run unchanged.
- `audit/` — the data-audit queries A1 to A10. `jobs/` runs them and writes `docs/data-audit.md`.
