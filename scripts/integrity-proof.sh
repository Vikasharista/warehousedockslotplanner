#!/usr/bin/env bash
# Deliverable 5, the integrity proof, in one command: ./scripts/integrity-proof.sh
#
# Each part on a fresh stack (docker compose down -v throws local demo data away):
#   1. "Ten drivers, one slot", on the snapshot plus 12 generated trucks known at 09:55 (the snapshot alone has 13 open
#      trucks and at most 9 can use any one slot; the full 70-truck day leaves no slot free for two): evals/ten_drivers.py sends ten simultaneous hold requests for one free slot, round after round, and
#      checks that exactly one wins and that no slot or dock is ever double-booked.
#   2. The race harness, on the supplied snapshot: 60 seconds of drivers holding, confirming and releasing, same-slot bursts, the solver
#      re-planning in a loop and a coordinator blocking slots, all at once; then invariants I1-I8.
# Results: docs/results/phase8/ten-drivers.json and race-phase8.json. Exits non-zero if either fails.
set -euo pipefail
cd "$(dirname "$0")/.."
DB=postgresql://postgres:postgres@db:5432/setuhaul
fresh() {  # $1: generated trucks on top of the snapshot
  docker compose down -v >/dev/null
  SETUHAUL_SCENARIO_TRUCKS="$1" SETUHAUL_SCENARIO_TRICKLE=0 docker compose up --build -d >/dev/null
  for _ in $(seq 1 90); do
    curl -sf http://localhost:8082/api/health >/dev/null && curl -sf http://localhost:8081/q/health/ready >/dev/null && break
    sleep 2
  done
  sleep 25  # the first plan is committed
}
mkdir -p docs/results/phase8
fresh 12
NET=$(docker inspect -f '{{range $k, $v := .NetworkSettings.Networks}}{{$k}}{{end}}' "$(docker compose ps -q api)")
docker build -q -f evals/Dockerfile -t setuhaul/evals . >/dev/null
echo "== ten drivers, one slot"
docker run --rm --network "$NET" -v "$PWD:/repo" -w /repo setuhaul/evals \
  python evals/ten_drivers.py --api http://api:8082 --database-url "$DB" --json docs/results/phase8/ten-drivers.json
fresh 0
echo "== race harness"
docker run --rm --network "$NET" -v "$PWD:/repo" -w /repo/jobs setuhaul/jobs:local \
  python -m setuhaul_jobs.race --database-url "$DB" --api http://api:8082 --solver http://solver:8081 \
    --seconds 60 --drivers 8 --burst 6 --seed 3 --json /repo/docs/results/phase8/race-phase8.json | tail -12
