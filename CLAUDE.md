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
- Driver holds belong to dock-api (Go). A truck with a booking holds a second slot beside it (`is_current = 0`) until
  the driver confirms. Holds and solver commits share one per-facility advisory lock
  (`setuhaul.bookings:<facility>`).
