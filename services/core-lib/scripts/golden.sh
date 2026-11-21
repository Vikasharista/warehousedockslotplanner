#!/usr/bin/env bash
# Proves core-lib serves the devkit bootstrap templates unchanged: renders the devkit's widget-service example
# against this checkout of core-lib, then generates, builds, vets, runs the 100% coverage gate and the e2e suite.
#
#   services/core-lib/scripts/golden.sh <devkit checkout> <postgres server URL, no database> [out dir]
#   services/core-lib/scripts/golden.sh ~/developmentkit postgres://postgres@localhost:55432
#
# Creates (and drops first) the database widget_service_test on that server. Needs Go 1.24+ and psql.
set -euo pipefail

core="$(cd "$(dirname "$0")/.." && pwd)"
devkit="$(cd "${1:?devkit checkout}" && pwd)"
server="${2:?postgres server URL, e.g. postgres://postgres@localhost:55432}"
server="${server%/}"
out="${3:-$(mktemp -d)/widget-service}"
module="$(go -C "$core" list -m)"
export GOTOOLCHAIN=local CI=1
export TEST_DATABASE_DSN="$server/widget_service_test?sslmode=disable"

spec="$(mktemp)"
python3 - "$devkit/library/bootstrap/examples/widget-service.json" "$spec" "$module" <<'PY'
import json, sys
s = json.load(open(sys.argv[1]))
s["service"].update(core_module=sys.argv[3], go_version="1.24.7", module_path="example.com/widget-service")
json.dump(s, open(sys.argv[2], "w"))
PY

rm -rf "$out" && mkdir -p "$out"
(cd "$devkit/library/bootstrap/render" && go run . -spec "$spec" -out "$out" >/dev/null)
cd "$out"
go mod edit -replace "$module=$core"
git init -q
go mod tidy -e >/dev/null 2>&1 || true
for config in internal/app/*/gqlgen.yml; do
  (cd "$(dirname "$config")" && go run github.com/99designs/gqlgen generate)
done
go mod tidy
go build ./...
go vet ./... && go vet -tags integration ./...
psql "$server/postgres?sslmode=disable" -qc 'DROP DATABASE IF EXISTS widget_service_test' -c 'CREATE DATABASE widget_service_test'
make cover
make e2e
echo "golden run passed: $out"
