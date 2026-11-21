# solver

The SetuHaul dock-sequencing solver: Java 21, Maven, [Timefold Solver](https://timefold.ai) 2.8.0.
It reads one facility from Postgres as it stood at an explicit as-of time, chooses a start slot for every truck
that still needs a dock, and prints the plan. Phase 2 is a dry run: it writes nothing to the database.

## Build, test, run

```bash
cd solver
mvn verify                                   # unit tests; snapshot tests run when SETUHAUL_TEST_JDBC_URL is set
SETUHAUL_TEST_JDBC_URL='jdbc:postgresql://localhost:55432/setuhaul?user=postgres&password=postgres' mvn verify

java -jar target/dock-solver.jar \
  --jdbc-url 'jdbc:postgresql://localhost:55432/setuhaul?user=postgres&password=postgres' \
  --facility FAC-JAI-01 --as-of 2026-08-04T09:55:00+05:30 [--config site.properties] [--seconds 10]
```

The database must be seeded first: `cd jobs && python -m setuhaul_jobs.seed --apply-schema` (see `jobs/README.md`).

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
