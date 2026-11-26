#!/usr/bin/env bash
# Deliverable 4, the scenario run, in one command: ./scripts/scenario-run.sh
#
# 1. Resets the local stack to the supplied snapshot (docker compose down -v: local demo data is thrown away) and
#    starts it without generated trucks, like scripts/agent-suite.sh. KEEP_DATA=1 skips the reset.
# 2. Waits for the planner and its first committed plan.
# 3. Runs evals/scenario_run.py in a Python 3.12 container on the stack's network. It writes docs/scenario-run/
#    (storyline, every conversation, evals, the brief's measures, the two-layer review) and exits non-zero if an
#    eval, an invariant or a conversation's expected outcome fails.
set -euo pipefail
cd "$(dirname "$0")/.."
if [ "${KEEP_DATA:-0}" != "1" ]; then
  docker compose down -v
  SETUHAUL_SCENARIO_TRUCKS=0 docker compose up --build -d
fi
echo "waiting for the planner..."
for _ in $(seq 1 90); do
  curl -sf http://localhost:8082/api/health >/dev/null && curl -sf http://localhost:8081/q/health/ready >/dev/null && break
  sleep 2
done
NET=$(docker inspect -f '{{range $k, $v := .NetworkSettings.Networks}}{{$k}}{{end}}' "$(docker compose ps -q api)")
docker build -q -f evals/Dockerfile -t setuhaul/evals . >/dev/null
docker run --rm --network "$NET" -v "$PWD:/repo" -w /repo setuhaul/evals \
  python evals/scenario_run.py --api http://api:8082 --solver http://solver:8081 \
    --database-url postgresql://postgres:postgres@db:5432/setuhaul
