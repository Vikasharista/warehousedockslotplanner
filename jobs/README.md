# jobs

Python jobs for the SetuHaul planner. Phase 1: the Postgres seed loader and the data audit.
Phase 3 adds the GPS simulator and the ETA service; phase 4 the shared clock and the live GPS loop.

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

`--init` is for container start-up: it creates the schema and loads the snapshot only when the database has no
shipments yet, and otherwise changes nothing.

### The shared clock

Every service reads "now" from `setuhaul_now()` in Postgres, so the solver, the API, the GPS feed and the plan view
agree on it. The seed starts it at the snapshot's moment, 4 August 2026 09:55 IST, running at real speed:

```bash
../.venv/bin/python -m setuhaul_jobs.seed --clock-start 2026-08-04T09:55:00+05:30 --clock-rate 1   # the default
../.venv/bin/python -m setuhaul_jobs.seed --clock-start real                                      # real time
```

The clock is one row in `app_clock` (`sim_anchor`, `real_anchor`, `rate`). To jump it forward by hand:
`UPDATE app_clock SET sim_anchor = sim_anchor + interval '30 minutes';`

## Run the data audit

```bash
cd jobs
../.venv/bin/python -m setuhaul_jobs.audit                  # writes ../docs/data-audit.md
```

Each finding is one query in `db/audit/`, with `-- id`, `-- title` and `-- impact` header lines.

## Simulated GPS (test server)

Three fixed lanes into the Jaipur DC: Delhi (286 km), Ahmedabad (651 km) and Mumbai (1,120 km). Their truck
routes were fetched once from OpenRouteService (driving-hgv) into `data/routes/` and load with no network
(`python -m setuhaul_jobs.gps.lanes`; `--fetch` re-fetches and needs the API key). Seeding reloads them.
The snapshot has no coordinates for the DC, so central Jaipur (26.9124 N, 75.7873 E) stands in for it.
Route data (c) openrouteservice.org | OpenStreetMap contributors.

```bash
cd jobs
../.venv/bin/python -m setuhaul_jobs.gps.simulate --until 2026-08-04T09:55:00+05:30 \
    [--scenario SHP1016=feed_loss:08:00] [--scenario SHP1009=early:30]
../.venv/bin/python -m setuhaul_jobs.gps.eta --as-of 2026-08-04T09:55:00+05:30
```

- The simulator moves every truck that is on a lane and not yet at the gate (towns within 15 km of a route join
  it part-way: Manesar, Bhiwadi and Neemrana on Delhi; Udaipur and Ajmer on Ahmedabad), one ping per minute.
  The default scenario reproduces the snapshot: the truck arrives at its latest declared ETA, and a declared
  BREAKDOWN becomes a stop. Others: `on_time`, `early:<min>`, `slowdown:<min>`, `breakdown`, `feed_loss:<HH:MM>`.
  Runs are repeatable and replace their own earlier pings.
- The ETA service replays a 5-minute cycle: ETA = ping time + remaining route km / the lane's average speed
  (`route_lanes.avg_speed_kmph`, 45 by default). It writes an estimate only when the ETA moves 10 minutes or
  more; confidence is HIGH up to 10 minutes of ping age and MEDIUM up to 30; after 30 minutes without a ping the
  last ETA is kept once more, marked stale with LOW confidence.
- `v_effective_eta` (and the solver, as of its run time): an OPERATIONS_OVERRIDE wins; otherwise the GPS ETA,
  or the driver's later ETA while their exception is open; otherwise the latest ETA update.

### Live feed

```bash
../.venv/bin/python -m setuhaul_jobs.gps.live --every-seconds 30 [--scenario SHP1012=breakdown]
```

Each tick writes the simulated pings up to `setuhaul_now()` and recomputes the GPS ETAs, so the solver's ETA trigger
sees trucks drift as the simulated day goes on. Lane speed for the ETA is a fixed 45 km/h (a decision); the simulated
trucks move at 35 to 76 km/h, so GPS ETAs can be up to about an hour off the declared ones.

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
- the validator accepts a correct run and catches each kind of broken run with the right check;
- GPS: route geometry, lane matching, repeatable simulation and its scenarios, the ETA service's write,
  confidence and stale rules, the v_effective_eta rules, and a V5 violation caused by a later GPS ETA.
