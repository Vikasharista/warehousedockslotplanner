# solver

The SetuHaul dock-sequencing solver: Java 21, Maven, [Timefold Solver](https://timefold.ai) 2.8.0.
It reads one facility from Postgres as it stood at an explicit as-of time and chooses a start slot for every
truck that still needs a dock.

| Module | What it is |
| --- | --- |
| `core/` | Model, constraints, Postgres loader, KPIs, and the `dock-solver.jar` dry run (prints a plan, writes nothing) |
| `service/` | Quarkus 3.40 service with Timefold's Quarkus extension: stores runs, commits valid runs to `appointments`, and re-plans on its triggers |

## Build and test

```bash
cd solver
mvn install                                  # unit tests; database tests skip without SETUHAUL_TEST_JDBC_URL
SETUHAUL_TEST_JDBC_URL='jdbc:postgresql://localhost:55432/setuhaul?user=postgres&password=postgres' mvn install
```

The database must be seeded first: `cd jobs && python -m setuhaul_jobs.seed --apply-schema` (see `jobs/README.md`).
`CommitIT` writes bookings, so it runs on its own copy of that database (`<db>_commit_it`, made from it as a template
and dropped afterwards); nothing else may be connected to the seeded database while it is copied.

## Run the service

```bash
SETUHAUL_JDBC_URL=jdbc:postgresql://localhost:55432/setuhaul java -jar service/target/quarkus-app/quarkus-run.jar

curl -X POST localhost:8081/solve -H 'Content-Type: application/json' \
     -d '{"facilityId":"FAC-JAI-01","asOf":"2026-08-04T09:55:00+05:30","trigger":"ETA_DELAY","solveSeconds":5}'
curl localhost:8081/runs/RUN-...             # a stored run
curl localhost:8081/q/health/ready           # readiness, including the database
```

| Endpoint | Result |
| --- | --- |
| `POST /solve` | 201 with the run (score, KPIs, one item per truck) and `Location: /runs/{id}`; 400 with a message for bad input. Plans only: books nothing |
| `POST /replan` | `{"facilityId", "trigger"?, "detail"?}`: plan as of the shared clock and commit. 200 with each attempt's outcome |
| `POST /runs/{id}/commit` | Commit a stored SOLVED run. 200 with COMMITTED or REJECTED (and why); 404 unknown; 409 not SOLVED |
| `GET /runs/{id}` | 200 with the stored run (including outcome and validator findings), 404 if unknown |
| `GET /facilities/{id}/runs` | The facility's newest runs, without items |
| `GET /facilities/{id}/triggers` | The trigger that would fire now, if any |
| `GET /clock` | The shared clock's now |
| `GET /q/health/live`, `/q/health/ready` | Quarkus health checks |

`trigger` is SCHEDULED, ETA_DELAY, CHECKIN or MANUAL (default). `asOf` defaults to the shared clock's now
(`setuhaul_now()`). `solveSeconds` (1-60) overrides the configured search time. Each stored run keeps the exact settings it was solved with. An infeasible plan is stored with
status REJECTED. Settings come from `solver-defaults.properties`; point `setuhaul.solver.settings-file` at a
properties file to override any of them. Every stored run should pass the independent validator:
`python -m setuhaul_jobs.validate --run-id RUN-...` (see `jobs/README.md`).

## Commit path (phase 4)

A run is written to `appointments` only after it passes three gates, in order:

1. **Independent validator.** The service runs the SQL checks in `db/validate/` (V1 to V8, the files the Python
   validator runs, copied onto the classpath at build time). Any finding: the run is REJECTED as `INVALID: V…` with
   the findings stored on the run, and nothing is written.
2. **Stale check.** In one transaction, holding the facility's booking lock (`pg_advisory_xact_lock`, shared with
   dock-api's holds), the run is REJECTED as `STALE: …` when:
   - any truck's current booking is no longer the one the run was planned on;
   - a target slot is taken by a booking the commit would not cancel, or is no longer OPEN;
   - an ETA update, GPS estimate, check-in, dock event or driver exception arrived after the run's as-of time.
3. **Write.** Bookings the run moves or releases are cancelled (`is_current = 0`, reason on the row). New bookings
   are inserted as `CONFIRMED` / `SCHEDULING_TOOL`, pointing back at the booking they replace, with
   `warehouse_confirmation_ref` = the run id. A unique-index violation means another writer took a slot: the
   transaction rolls back and the run is REJECTED as `SLOT_TAKEN`.

STALE and SLOT_TAKEN ask for a fresh re-plan, which `POST /replan` and the triggers do straight away (two attempts in
total). Kept bookings, flagged weight breaches, trucks in the dock and driver holds the plan keeps are never written.
Every committed change appears in the `v_change_log` view.

## Triggers

A timer (`setuhaul.triggers.scan-every`, 60 s) checks each facility in `setuhaul.triggers.facilities` against the
shared clock and re-plans when one is due, in this order:

| Trigger | When |
| --- | --- |
| `CHECKIN` | A truck reached the gate since the last run |
| `NEW_REQUEST` | A truck request was made since the last run (a shipment's `created_at`; requests do not exist before it) |
| `ETA_DELAY` | A booked truck's effective ETA (GPS included) is more than `eta-shift-min` (30) from the ETA its slot was planned on, later or earlier |
| `SCHEDULED` | No run for `schedule-min` (15) minutes, or no run yet |

Event triggers wait `min-gap-min` (2) after the last run, so a plan that cannot commit does not re-run every scan.
All of these are configuration (`application.properties`); `SETUHAUL_TRIGGERS=false` turns the timer off.

## Dry run

```bash
java -jar core/target/dock-solver.jar \
  --jdbc-url 'jdbc:postgresql://localhost:55432/setuhaul?user=postgres&password=postgres' \
  --facility FAC-JAI-01 --as-of 2026-08-04T09:55:00+05:30 [--config site.properties] [--seconds 10]
```

## Model

| Piece | Class | What it is |
| --- | --- | --- |
| Planning entity | `ShipmentAssignment` | One truck. Its planning variable is the start `Slot`; it may stay unassigned, which sends it to a coordinator |
| Value range | per entity | Only slots that pass H1, H3, H4, H5, H10 and H11 and do not run into a dock block |
| Problem facts | `Dock`, `Slot`, `DockBlock`, `PlanWeights` | Blocks are dock events, BLOCKED/CLOSED slots, closing hours, trucks in a dock and flagged bookings |
| Score | `BendableScore`, 1 hard + 4 soft levels | hard = H1-H12; soft 0-3 = L1 unassigned, L2 priority inversions, L3 next-pickup inversions, L4 weighted wait + moves |
| Constraints | `DockConstraints` | One method per rule, each unit-tested with `ConstraintVerifier` |
| Settings | `solver-defaults.properties` | Every weight, buffer and limit; override any key with `--config` |

A truck occupies its dock from the slot start for max(unload minutes, slot length), so a 75-minute unload on
60-minute slots blocks the next slot too.

## Loading rules (as of a time)

- One `REPEATABLE READ, READ ONLY` transaction, so the plan comes from one consistent snapshot and nothing is written.
- ETA updates and dock events count only if `created_at <= as_of`; bookings only if `booked_at <= as_of`; a truck
  is at the gate only if `gate_in_ts <= as_of`. The ETA rule is the same as `v_latest_eta` (latest update wins),
  filtered by time. Shipment and slot statuses are taken as stored, because the snapshot has no history for them
  (data-audit finding A8).
- Earliest ready = as_of or gate-in time for trucks at the gate; otherwise ETA + 30-minute gate buffer
  (+15 minutes for LOW-confidence ETAs).
- A CONFIRMED booking that is still valid is pinned (H12). It is released, with a reason, when its slot has
  already started, the truck cannot reach it, the slot is blocked, or it overlaps an earlier kept booking.
- A driver's PENDING_CONFIRMATION hold is pinned for 15 minutes after `booked_at`, then released (`HOLD_EXPIRED`).
- A booking above its dock's weight limit is flagged to a coordinator and left exactly where it is; its slot stays
  occupied.
- A truck still in a dock past its expected unload end is assumed to need `in_dock_overrun_min` (15) more minutes.

## Timefold 2.x notes

The plan was written against the 1.x API. In 2.8.0: score classes live in `ai.timefold.solver.core.api.score`
(`BendableScore` is long-based), `ConstraintVerifier` ships in `timefold-solver-core` (there is no separate test
artifact), `PlanningId` is in `api.domain.common`, and constraint names may not contain `/` or `,`.
