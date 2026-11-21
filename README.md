# SetuHaul warehouse dock slot planner

A dock-sequencing solver, capacity layer and planning frontend for SetuHaul Logistics' receiving
warehouses. When a truck runs late or early, the solver re-plans dock slots for the next 12 hours so
that no two trucks are ever promised the same dock at the same time, and higher-priority loads wait least.

The design, constraints, objective and decisions are in the plan document:
[SetuHaul Dock Sequencing Solver: Plan](https://claude.ai/artifact/7F818ubM84vjsf6qZ4DowC).

## Layout

| Folder | What lives there | Stack |
| --- | --- | --- |
| [`data/setuhaul/`](data/setuhaul/) | The supplied SQLite snapshot (4 August 2026) and its schema, dictionary and guide. Read-only seed | SQLite |
| [`db/`](db/) | Postgres schema ported from the snapshot, and the data-audit queries | Postgres 16 |
| [`jobs/`](jobs/) | Python jobs: seed loader, data audit, later the GPS simulator and ETA service | Python 3.12+ |
| [`solver/`](solver/) | The dock-sequencing solver | Java 17+, Maven, Timefold Solver |
| [`services/`](services/) | Go services: capacity layer (hold, confirm, release), plan API | Go |
| [`web/`](web/) | Planning frontend: plan view, input data, capacity inputs | React, TypeScript, Vite |
| [`docs/`](docs/) | Data audit report and other project documents | Markdown |

Postgres is the runtime database. The SQLite file is kept unchanged as the seed source.

## Delivery phases

1. Data audit and Postgres seed
2. Solver model and constraints
3. Snapshot service, plan tables, GPS lanes, simulator and ETA service, core library for the Go services
4. Commit path and re-plan triggers
5. Plan view frontend and agent hooks
6. Integrity, scale and baseline tests; client readout

Each phase lands as a branch and a pull request.
