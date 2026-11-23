# Project instructions

SetuHaul warehouse dock slot planner. Plan of record:
https://claude.ai/artifact/7F818ubM84vjsf6qZ4DowC — read it before changing solver rules.

## Ways of working

- Follow the devkit skills (`devkit:ship`, `devkit:review`, `devkit:bootstrap`, ...). Skip their Jira and
  Confluence gates: this project has no Atlassian access.
- One branch and one pull request per delivery phase. Never push straight to `main` after the initial scaffold.
- Every acceptance criterion needs evidence: a test or command output.

## Decisions already made (do not reopen without the user)

- Postgres 16 is the runtime database; `data/setuhaul/` holds the supplied SQLite snapshot as the
  read-only seed. Never edit files in `data/setuhaul/`.
- Timestamps are `timestamptz`; the snapshot's zone is Asia/Kolkata (+05:30).
- Solver: Timefold Solver (Java 17+, Maven). Go services, Python jobs, React + TypeScript + Vite frontend.
- The solver writes appointments as `CONFIRMED` with `booking_source = SCHEDULING_TOOL`;
  `PENDING_CONFIRMATION` is only a driver's hold, which expires after 15 minutes.
- Weight breaches and starts after the facility's last allowed start time are flagged to a coordinator,
  never moved automatically.
- One 30-minute gate buffer for every facility. Any OPEN slot overlapping a dock event
  (including CAPACITY_REDUCTION) is unavailable.
- GPS is simulated on three fixed lanes (Delhi, Ahmedabad, Mumbai to FAC-JAI-01); GPS ETAs live in their
  own table, not in `eta_updates`.
- All weights, buffers and limits are configuration, not constants.
- The GPS ETA service uses one fixed lane speed (45 km/h, configurable). Simulated trucks move at 35-76 km/h, so
  GPS ETAs can be up to about an hour off; a rolling observed speed is the upgrade if that becomes a problem.
- Every service reads "now" from `setuhaul_now()` (the `app_clock` row). The seed starts it at the snapshot's
  4 Aug 2026 09:55 IST running at real speed; never use the wall clock for planning decisions.
- A valid plan is committed automatically (independent validator, stale check, one transaction). Re-plan triggers:
  gate check-in, a new truck request (NEW_REQUEST), effective ETA moved more than 30 minutes from the ETA the slot
  was planned on, and every 15 minutes.
- A shipment exists for planning only from its `created_at`; every reader filters on it, which is how generated
  requests arrive during the simulated day. Generated load-test data (`SHP-G…`) lives in Postgres only.
- Feasible CONFIRMED bookings stay pinned, except that a CRITICAL truck may bump one LOW or NORMAL booking
  starting more than 2 hours after the plan (never a truck at the gate, a flagged booking or a driver hold). The
  bump costs `bump_penalty` (10) on the unassigned level, between HIGH's weight (9) and CRITICAL's (27); only a
  CRITICAL truck may use a bumped slot, at most one each; the bumped truck is re-planned or goes to the exceptions
  list (`BUMPED_FOR_CRITICAL`). All of it is configuration (`bump_*` in solver-defaults.properties).
- The demo is deployed as the same Docker Compose stack on one Linux VM (`docker-compose.deploy.yml`): Caddy
  HTTPS in front, no public ports for the services, secrets only from `.env`. Reviewers sign in with seeded
  email + password demo accounts (bcrypt in `app_users`, session cookie), all with full access. A nightly reset
  (03:00 IST) reloads the snapshot at 09:55 with the load test and keeps the accounts. Hosting provider undecided.
- Driver holds belong to dock-api (Go). A truck with a booking holds a second slot beside it (`is_current = 0`) until
  the driver confirms. Holds and solver commits share one per-facility advisory lock
  (`setuhaul.bookings:<facility>`).
- Driver agents use dock-api's `/api/agent/*` with an `X-Agent-Key` (`APP_AGENT_KEYS`, `name:key`, keys 32+
  characters); a key opens only those routes, and signed-in users may call them too (the Driver chat console). Agents
  learn of plan changes by polling a cursor feed (`booking_changes`, written by the commit). A driver's ETA report
  does not beat GPS: the effective ETA rule stays override, then GPS (later of GPS and driver while an exception is
  open), then the latest report; the agent tells the driver which ETA the plan uses.
- Coordinator inputs (new request, edit, ETA override, check-in, dock status, dock events, slot blocks) are dock-api
  routes behind the Shipments and Capacity screens; each one asks the solver for a MANUAL re-plan in the background.
- The solver's search starts from the priority-first plan (`warm_start=PRIORITY_FIRST`, chosen in phase 6 over the
  cold start, best-of-both and a longer limit). Scoring rules are unchanged.
- A confirmed truck whose effective ETA is 30+ minutes earlier than the ETA its slot was planned on is released
  (`early_release_min`, reason `ARRIVING_EARLY`): it may take a free earlier slot; nobody else moves for it.
- Every availability check (driver options and holds, the solver's view of side holds, validator V3, the commit's
  stale check) counts a booking's whole unload, not only its slot.
- The driver chat agent lives in `agent/` (Python) and talks to the planner only through dock-api's `/api/agent/*`:
  no SQL in `agent/`. Chat threads and messages, driver exceptions, coordinator cases (`coordinator_cases`) and
  approval requests (`slot_change_requests`) are planner data; the agent keeps only its sessions and duplicate-message
  log in its own SQLite file. Phase 7 plan: https://claude.ai/artifact/CEhBRBoLMYFSsm8Yce24ny
- A driver's slot change for a HIGH or CRITICAL load (`approval_priorities`) needs a coordinator: confirm answers
  202 `AWAITING_APPROVAL`, the old booking stays current and the held slot is reserved for 60 minutes
  (`approval_reserve_min`). Approve swaps the bookings under the booking lock; reject or expiry releases the
  reservation and keeps the old booking. The sweeper, the solver loader and validator V3 read a reservation's expiry
  from its request. Outcomes and coordinator replies reach the driver on the change feed (`kind` APPROVAL, REPLY).
- A truck may change its slot by chat at most `max_chat_reschedules` (2) times; dock-api enforces it on hold and
  confirm. Coordinators work approvals, cases and open exceptions on the Driver cases screen.
