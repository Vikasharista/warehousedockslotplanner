# SetuHaul warehouse dock slot planner

A dock-sequencing solver, capacity layer and planning frontend for SetuHaul Logistics' receiving
warehouses. When a truck runs late or early, the solver re-plans dock slots for the next 12 hours so
that no two trucks are ever promised the same dock at the same time, and higher-priority loads wait least.

The design, constraints, objective and decisions are in the plan document:
[SetuHaul Dock Sequencing Solver: Plan](https://claude.ai/artifact/7F818ubM84vjsf6qZ4DowC).

## The six deliverables

| # | Deliverable (FDE brief) | Where |
| --- | --- | --- |
| 1 | Problem Definition Sheet and data audit (14 findings, each proved by a query) | [docs/problem-definition.md](docs/problem-definition.md), [docs/data-audit.md](docs/data-audit.md) |
| 2 | Decision register: the brief's 13 decisions, each with an answer, a reason and evidence | [docs/decision-register.md](docs/decision-register.md) |
| 3 | Working prototype: conversation layer, tools and state; library versions [below](#library-versions) | This repository: `docker compose up --build`; [architecture](docs/architecture.md) |
| 4 | Scenario run, ending in one escalation: scripted, every planner call recorded, scored by evals | [docs/scenario-run/](docs/scenario-run/README.md), `./scripts/scenario-run.sh` |
| 5 | Integrity proof: ten drivers on one slot, 20 rounds, always exactly one winner, 0 double bookings; the race harness | [docs/integrity-proof.md](docs/integrity-proof.md), `./scripts/integrity-proof.sh` |
| 6 | Client readout (one page): policy, limits, risks | [docs/client-readout.md](docs/client-readout.md) |
| + | Optional scheduling tool, compared with simple dispatch rules | [solver/](solver/README.md), [baseline comparison](docs/results/phase6/baseline.md) |

The filled-in submission template is [docs/submission.md](docs/submission.md).

## Run it on your machine

Needs Docker with Compose v2.

```bash
git clone https://github.com/Vikasharista/warehousedockslotplanner.git
cd warehousedockslotplanner
docker compose up --build
```

Then open **http://localhost:5173** and sign in with the local demo account **demo@setuhaul.local** /
**setuhaul-demo-2026** (local only). The first build downloads the Maven, Go, npm and Python dependencies and takes
a few minutes; later starts are quick. What starts:

| Service | Port (localhost only) | What it does |
| --- | --- | --- |
| `db` | 55432 | Postgres 16 |
| `seed` | | Loads the snapshot and starts the shared clock at 4 Aug 2026 09:55 IST, once; later starts keep your data |
| `gps` | | Simulated GPS on the three lanes and the ETA service, every 30 s |
| `solver` | 8081 | Plans, validates, commits; re-plans on ETA shifts, check-ins and every 15 minutes |
| `api` | 8082 | dock-api: the screens' API, driver holds and the [driver agent API](docs/agent-api.md) |
| `web` | 5173 | The plan view and the Shipments, Capacity, Driver cases and Driver chat screens |
| `agent` | | The driver chat agent ([agent/](agent/README.md)): Telegram with `TELEGRAM_TOKEN` in `.env`, idle without it |

The first start also loads a **load test**: 70 generated truck requests on top of the snapshot's 20. Fifty are known
at 09:55 and twenty arrive between 10:00 and 17:00, with delays, a dock breakdown and coordinator cases. That fills
the docks and leaves some trucks for a coordinator. "Add requests" in the plan view adds more at any time.
`SETUHAUL_SCENARIO_TRUCKS=0 docker compose up --build` on the first start gives the snapshot alone (see
`jobs/README.md`).

Screens, across the top of the page:

- **Plan**: the dock sequence, exceptions, change log, runs and driver holds.
- **Shipments**: every open truck request with its ETA, slot and plan status. Add a request, edit one, record a
  driver ETA or an operations override, or check a truck in at the gate.
- **Capacity**: take docks in or out of service, record breakdowns or maintenance, block or reopen single slots.
- **Driver cases**: what the driver agent hands to a person. HIGH and CRITICAL slot changes wait here for approval
  (the old booking stands and the new slot is reserved for 60 minutes). Escalated cases can be answered, and the reply
  reaches the driver. Open driver exceptions each open their conversation.
- **Driver chat**: a test console that plays a driver and calls the agent API a WhatsApp bot would use (local agent key
  `local-agent-key-for-development-only`, header `X-Agent-Key`).

Every change re-plans; the plan view shows the result within a few seconds.

`docker compose down -v` throws the data away; the next `up` starts again from the snapshot at 09:55.

**Deploying for reviewers:** [docs/deploy.md](docs/deploy.md) covers any Linux VM. It adds HTTPS, the demo accounts
you choose, and a nightly reset to the start of the demo day.

![The plan view](docs/plan-view.png)

## Layout

| Folder | What lives there | Stack |
| --- | --- | --- |
| [`data/routes/`](data/routes/) | The three fixed truck routes into the Jaipur DC (OpenRouteService, OpenStreetMap data) | GeoJSON |
| [`data/setuhaul/`](data/setuhaul/) | The supplied SQLite snapshot (4 August 2026) and its schema, dictionary and guide. Read-only seed | SQLite |
| [`db/`](db/) | Postgres schema ported from the snapshot, and the data-audit queries | Postgres 16 |
| [`jobs/`](jobs/) | Python jobs: seed loader, data audit, plan validator, GPS simulator and ETA service, load test, race harness | Python 3.12 |
| [`solver/`](solver/) | The dock-sequencing solver | Java 21, Maven, Timefold Solver |
| [`services/`](services/) | [`dock-api`](services/dock-api/): the plan view's API and driver holds (hold, confirm, release). [`core-lib`](services/core-lib/): the Go services' shared library | Go 1.24+ |
| [`web/`](web/) | The plan view (dock sequence, exceptions, change log, runs, driver holds) and the Shipments, Capacity, Driver cases and Driver chat screens | React, TypeScript, Vite |
| [`agent/`](agent/) | The driver chat agent: Telegram (WhatsApp optional), talking to the planner only through its agent API | Python 3.12 |
| [`evals/`](evals/) | The scenario run, the deterministic trace checks, the golden message set; run in CI ([`.github/workflows/evals.yml`](.github/workflows/evals.yml)) | Python 3.12 |
| [`docs/`](docs/) | The deliverables above, data audit report, [driver agent API](docs/agent-api.md) and its [Telegram bot guide](docs/telegram-agent.md), [deployment](docs/deploy.md), [verification matrix](docs/verification.md), [phase 6 results](docs/results/phase6/) ([readout](https://claude.ai/artifact/5Hawn7Pmg39pbhSTFByMFs)) | Markdown |
| [`scripts/`](scripts/) | `scenario-run.sh`: the scenario run (deliverable 4). `integrity-proof.sh`: ten drivers on one slot, then the race harness (deliverable 5). `agent-suite.sh`: every agent test on a fresh stack. `break-and-defend.sh`: rebuilds the baseline comparison and scale tables | Bash |

Postgres is the runtime database. The SQLite file is kept unchanged as the seed source.

## Library versions

Every version is pinned in the build files named below. Docker builds use exactly these.

| Part | Runtime | Libraries |
| --- | --- | --- |
| Database | PostgreSQL 16.6 (`postgres:16.6`) | |
| Solver (`solver/pom.xml`) | Java 21 (Eclipse Temurin 21), Maven 3.9.11 | Timefold Solver 2.8.0, Quarkus 3.40.1.1 (with Timefold's Quarkus extension), PostgreSQL JDBC 42.7.14 |
| dock-api, core-lib (`services/*/go.mod`) | Go 1.24.7 | GORM 1.25.10 with the Postgres driver 1.5.7 (pgx 5.5.4), golang-jwt 5.3.1, x/crypto 0.46.0 (bcrypt), zap 1.27.0, google/uuid 1.6.0, testify 1.11.1 |
| Web (`web/package.json`) | Node 22.12 (build), nginx 1.27 (serve) | React 18.3.1, TypeScript 5.6.3, Vite 5.4.11, @vitejs/plugin-react 4.3.4, Vitest 2.1.8 |
| Driver agent (`agent/requirements*.txt`) | Python 3.12 | httpx 0.28.1, anthropic 0.125.0 (optional message reading; model `claude-sonnet-5-5`), pytest 8.4.2 |
| Jobs (`jobs/requirements.txt`) | Python 3.12 | psycopg 3.3.9 (binary), pytest 8.4.2 |
| Evals (`evals/requirements.txt`) | Python 3.12 | httpx 0.28.1, psycopg 3.3.9 (binary), pytest 8.4.2 |
| Deployment (`docker-compose.deploy.yml`) | Docker Compose v2 | Caddy 2.8.4 (HTTPS) |

## Delivery phases

1. Data audit and Postgres seed
2. Solver model and constraints
3. Snapshot service, plan tables, GPS lanes, simulator and ETA service, core library for the Go services
4. Commit path, re-plan triggers, driver holds and the plan view (the plan view moved up from phase 5)
5. Agent hooks and the rest of the frontend
6. Integrity, scale and baseline tests; client readout
7. The driver chat agent moved into this repository, coordinator approvals for priority changes, the Driver cases
   screen ([plan](https://claude.ai/artifact/CEhBRBoLMYFSsm8Yce24ny))
8. The submission: Problem Definition Sheet, decision register, scripted scenario run with evals and the brief's
   measures, guardrails (money, contradictory data, PII gate, hostile messages), four more audit findings, the one-page
   client readout, CI

Each phase lands as a branch and a pull request.
