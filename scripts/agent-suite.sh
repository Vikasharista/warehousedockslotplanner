#!/usr/bin/env bash
# The driver agent's suite against the planner on the real snapshot (review finding W12): a fresh Compose stack with
# no generated trucks, then every agent test, unit and live. Run from the repository root. It throws away the local
# demo data (docker compose down -v), like any fresh start.
set -euo pipefail
docker compose down -v
SETUHAUL_SCENARIO_TRUCKS=0 docker compose up --build -d
echo "waiting for the planner..."
for _ in $(seq 1 90); do
  curl -sf http://localhost:8082/api/health >/dev/null && curl -sf http://localhost:8081/q/health >/dev/null && break
  sleep 2
done
sleep 20  # the first plan is committed
cd agent
[ -d .venv ] || python3 -m venv .venv
.venv/bin/pip install -q -r requirements-dev.txt
.venv/bin/python -m pytest
