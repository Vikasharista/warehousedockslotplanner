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

## Sign-in

Every `/api` route needs a session except `/api/health`, `/api/auth/login` and `/api/auth/logout`.
- `POST /api/auth/login` `{"email","password"}` checks a demo account and sets the `setuhaul_session` cookie. The
  cookie is HttpOnly, SameSite=Strict, and Secure when `auth.secure_cookie` is on; it holds an HS256 JWT verified
  with core-lib's `authdirective.Verify`.
- `GET /api/auth/me` returns the signed-in user, and `POST /api/auth/logout` clears the cookie.
- API clients may send the token as `Authorization: Bearer …`.
- Wrong email and wrong password give the same 401. Five failures per email or per client address within 15 minutes
  give 429.

Accounts come from `APP_AUTH_DEMO_ACCOUNTS` (`email:password` pairs separated by commas, passwords at least 10
characters). They are created or updated at start, with only a bcrypt hash stored in `app_users`.
`APP_AUTH_JWT_SECRET` (at least 32 characters) signs the sessions; dock-api refuses to start without it.

## API

| Route | Result |
| --- | --- |
| `GET /api/clock` | The shared clock's now |
| `GET /api/facilities/{id}/board` | Docks, active bookings and holds from two hours ago to the end of the 12-hour horizon (with ETA, predicted wait, hold expiry), dock blocks, the last committed run and the newest run |
| `GET /api/facilities/{id}/exceptions` | The last committed run's unassigned, flagged, in-dock and beyond-horizon trucks, with reasons |
| `GET /api/facilities/{id}/changes?limit=` | Booking changes committed runs made, newest first |
| `GET /api/facilities/{id}/runs?limit=` | Newest solver runs |
| `POST /api/facilities/{id}/replan` | Asks the solver to re-plan and commit now; 502 if it does not answer |
| `POST /api/facilities/{id}/truck-requests` `{"trucks": n}` | Queues 1 to 50 generated truck requests; 202 with the job. The jobs' live loop creates them |
| `GET /api/truck-requests/{job}` | The job: PENDING, DONE (with the shipment ids) or FAILED |
| `GET /api/shipments/{id}/options?limit=` | Free slots the truck can hold now (only once its request has been made) |
| `POST /api/holds` `{"shipmentId","slotId"}` | 201 with the hold; 409 `SLOT_UNAVAILABLE` or `HOLD_CONFLICT` |
| `GET /api/holds/{id}` | The hold |
| `POST /api/holds/{id}/confirm` | The hold becomes the truck's CONFIRMED booking; 409 `HOLD_EXPIRED` after 15 minutes |
| `POST /api/holds/{id}/release` | The hold is cancelled |
| `GET /api/facilities/{id}/shipments` | Open trucks known as of the shared clock, with ETA, gate-in, current slot and plan status |
| `POST /api/facilities/{id}/shipments` `{"priorityCode","requiredDockType","loadWeightKg","unloadMin","originCity","eta"}` | A truck request made now (`SHP-M0001`, …, with its own driver and vehicle); the NEW_REQUEST trigger plans it |
| `PATCH /api/shipments/{id}` `{"priorityCode"?,"requiredDockType"?,"loadWeightKg"?,"unloadMin"?}` | Edits a request; re-plans |
| `POST /api/shipments/{id}/eta` `{"eta","source","confidence"?,"reason"?,"note"?}` | Records a `DRIVER_DECLARED` ETA or an `OPERATIONS_OVERRIDE` (which re-plans at once) |
| `POST /api/shipments/{id}/checkin` | Puts the truck at the gate now (EARLY / ON_TIME / LATE at ±30 min of its ETA); 409 if already in. The CHECKIN trigger re-plans |
| `GET /api/facilities/{id}/capacity` | Docks, dock events from two hours ago on, and slots with the truck booked in each |
| `POST /api/facilities/{id}/dock-events` `{"dockId","type","start","end"?,"reason"}` | BREAKDOWN, MAINTENANCE, CAPACITY_REDUCTION or MANUAL_BLOCK; re-plans |
| `PATCH /api/facilities/{id}/docks/{dock}` `{"status"}` | ACTIVE, MAINTENANCE or OUT_OF_SERVICE; re-plans |
| `POST /api/slots/{id}/status` `{"status","reason"?}` | Blocks (with a reason) or reopens a slot; re-plans |
| `/api/agent/*` | The driver agent API, with an `X-Agent-Key` or a session: see [docs/agent-api.md](../../docs/agent-api.md) |

Changes marked "re-plans" ask the solver for a MANUAL re-plan in the background (detail such as "dock D1 set to
OUT_OF_SERVICE") and answer at once; the plan view shows the result within a few seconds. If two changes overlap, a
run solved on the older data is rejected by the validator or the stale check and the next run commits.

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
