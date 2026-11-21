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
