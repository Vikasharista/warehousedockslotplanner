# db

- `schema.sql` — Postgres 16 port of `data/setuhaul/setuhaul_schema_and_seed.sql`: the same 18 tables,
  CHECK constraints, foreign keys, partial unique indexes (double-booking protection) and four views.
  Timestamps become `timestamptz`, facility hours `time`, rule dates `date`; 0/1 flags stay integers so
  queries written against the snapshot run unchanged.
- `audit/` — the data-audit queries A1 to A10. `jobs/` runs them and writes `docs/data-audit.md`.
- `plan_tables.sql` — the solver's own tables, `solver_run` and `solver_plan_item`, applied after `schema.sql`.
  A run and its items are written together; reseeding the snapshot empties them.
- `validate/` — the independent plan checks V1 to V8 that `jobs/` runs against a stored run.
- `gps_tables.sql` — `route_lanes`, `vehicle_gps_pings`, `gps_eta_estimates` and the `v_effective_eta` view,
  applied after `plan_tables.sql`.
