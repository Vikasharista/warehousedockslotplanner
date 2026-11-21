# dock-api

The Go service behind the plan view, built on [core-lib](../core-lib/): read endpoints for the dock sequence,
exceptions, change log and runs, and the driver hold flow (hold, confirm, release, 15-minute expiry). It never plans:
"Re-plan now" is passed to the solver service.

## Run

```bash
cd services/dock-api
go run . serve        # config/default.yaml; override any key with APP_*, e.g. APP_DATABASE_POSTGRES_DSN
```

Defaults: listens on 127.0.0.1:8082, Postgres on localhost:55432, solver on localhost:8081. In Docker Compose it
listens on :8082 inside its container and is published on 127.0.0.1 only. There is no login yet: keep it local.

## API

| Route | Result |
| --- | --- |
| `GET /api/clock` | The shared clock's now |
| `GET /api/facilities/{id}/board` | Docks, active bookings and holds from two hours ago to the end of the 12-hour horizon (with ETA, predicted wait, hold expiry), dock blocks, the last committed run and the newest run |
| `GET /api/facilities/{id}/exceptions` | The last committed run's unassigned, flagged, in-dock and beyond-horizon trucks, with reasons |
| `GET /api/facilities/{id}/changes?limit=` | Booking changes committed runs made, newest first |
| `GET /api/facilities/{id}/runs?limit=` | Newest solver runs |
| `POST /api/facilities/{id}/replan` | Asks the solver to re-plan and commit now; 502 if it does not answer |
| `GET /api/shipments/{id}/options?limit=` | Free slots the truck can hold now |
| `POST /api/holds` `{"shipmentId","slotId"}` | 201 with the hold; 409 `SLOT_UNAVAILABLE` or `HOLD_CONFLICT` |
| `GET /api/holds/{id}` | The hold |
| `POST /api/holds/{id}/confirm` | The hold becomes the truck's CONFIRMED booking; 409 `HOLD_EXPIRED` after 15 minutes |
| `POST /api/holds/{id}/release` | The hold is cancelled |

Errors are `{"code", "error"}`: 400 bad input, 404 unknown, 409 conflicts, 500 with no detail for anything else.

## Holds

- A hold is an `appointments` row with `PENDING_CONFIRMATION` / `DRIVER_CHAT`, one per truck.
- A truck without a current booking gets a current hold (`is_current = 1`).
- A truck that has a booking keeps it, and the hold sits beside it (`is_current = 0`, `replaced_appointment_id` =
  the booking) until the driver confirms: the old booking is then cancelled and the hold becomes current.
- While the hold lasts, the solver treats its slot as taken, and validator V3 checks that.
- Options follow the solver's hard rules: OPEN slot, active dock, no dock event over the unload, not before the
  truck can be ready (gate-in, or effective ETA + 30 min, + 15 min for a LOW-confidence ETA), not after the last
  start time, inside 12 hours, dock type (reefer, heavy) and weight. These settings are in `config/default.yaml`
  (`planning.*`) and must match the solver's.
- Every hold write takes the same per-facility advisory lock as the solver's commit, and the unique index on active
  bookings per slot is the final guard.
- A sweeper cancels expired holds every 30 seconds (`holds.sweep_every`).

## Tests

```bash
go test ./...                                         # handlers and solver client
TEST_DATABASE_URL='postgres://postgres@localhost:55432/setuhaul?sslmode=disable' go test ./...   # + store
```

Store tests copy the seeded database given by `TEST_DATABASE_URL` (as a template) for each test and drop the copy.
