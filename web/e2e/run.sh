#!/usr/bin/env bash
# Boots serve, worker, and runner against the Compose stack, bootstraps a
# fresh org and the platform problem bank, then runs the Playwright suite
# headless. Every process is torn down on the way out, whatever the outcome.
#
# The ports are deliberately not the defaults so a developer's own
# `make dev-serve` can keep running while this does.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
root="$(cd "$here/../.." && pwd)"
work="$here/.run"
mkdir -p "$work"

: "${E2E_LISTEN_PORT:=8090}"
: "${E2E_RUNNER_PORT:=8091}"
: "${E2E_DATABASE_NAME:=recruiting_e2e}"
: "${E2E_ADMIN_DATABASE_URL:=postgres://recruiting:recruiting@localhost:5433/recruiting?sslmode=disable}"

if [ ! -f "$root/.env" ]; then
  echo "web/e2e/run.sh: $root/.env is missing; copy .env.example to .env first" >&2
  exit 1
fi
set -a
# shellcheck disable=SC1091
. "$root/.env"
set +a

export BASE_URL="http://localhost:$E2E_LISTEN_PORT"
export LISTEN_ADDR=":$E2E_LISTEN_PORT"
export RUNNER_LISTEN=":$E2E_RUNNER_PORT"
export RUNNER_URL="http://localhost:$E2E_RUNNER_PORT"
export E2E_BASE_URL="$BASE_URL"

pids=()
cleanup() {
  local status=$?
  for pid in "${pids[@]:-}"; do
    [ -n "$pid" ] && kill "$pid" 2>/dev/null || true
  done
  wait 2>/dev/null || true
  # The database goes too: app_rw is cluster-wide, and grants on this
  # database's tables would block `make test-integration`'s down-migration
  # test from dropping that role. Logs stay in .run/ for post-mortems.
  drop_e2e_database || true
  exit "$status"
}
trap cleanup EXIT INT TERM

# wait_http polls a URL until it answers or the deadline passes; the second
# argument names the process for the failure message, the third its log.
wait_http() {
  local url=$1 name=$2 log=$3 i
  for i in $(seq 1 120); do
    if curl -fsS -o /dev/null "$url" 2>/dev/null; then return 0; fi
    sleep 0.5
  done
  echo "web/e2e/run.sh: $name never answered $url" >&2
  tail -20 "$log" >&2 || true
  return 1
}

echo "==> building"
(cd "$root" && go build -o bin/recruiting ./cmd/recruiting)
bin="$root/bin/recruiting"

# The suite seeds the problem bank and sits real attempts, which would break
# `make test-integration`'s assertions if they shared a database. It gets its
# own, recreated on every run so each run starts from an empty schema.
psql_admin() { docker exec -i recruiting-postgres-1 psql -q -U recruiting -d recruiting -v ON_ERROR_STOP=1 "$@"; }
drop_e2e_database() {
  psql_admin -c "drop database if exists $E2E_DATABASE_NAME with (force)" >>"$work/db.log" 2>&1
}

echo "==> recreating database $E2E_DATABASE_NAME"
psql_admin -c "drop database if exists $E2E_DATABASE_NAME with (force)" >"$work/db.log" 2>&1 \
  || { tail -20 "$work/db.log" >&2; exit 1; }
psql_admin -c "create database $E2E_DATABASE_NAME owner recruiting" >>"$work/db.log" 2>&1 \
  || { tail -20 "$work/db.log" >&2; exit 1; }

E2E_DATABASE_URL="${E2E_ADMIN_DATABASE_URL%/*}/$E2E_DATABASE_NAME?sslmode=disable"
export DATABASE_URL="$E2E_DATABASE_URL"
export DATABASE_URL_APP="postgres://app_rw:app_rw@localhost:5433/$E2E_DATABASE_NAME?sslmode=disable"

echo "==> migrating"
# The binary's own migrate mode, not `make migrate`: that target is pinned to
# the development database, and DATABASE_URL is already this run's database.
("$bin" migrate >"$work/migrate.log" 2>&1) \
  || { tail -20 "$work/migrate.log" >&2; exit 1; }

echo "==> runner on $RUNNER_LISTEN"
# The host has no gVisor and each metrics listener needs a port of its own,
# so both are set here rather than left at their production defaults.
RUNNER_ALLOW_INSECURE_RUNTIME=1 RUNNER_SQL_URL="$E2E_DATABASE_URL" METRICS_ADDR=":$((E2E_LISTEN_PORT + 10))" \
  "$bin" runner >"$work/runner.log" 2>&1 &
pids+=($!)
wait_http "$RUNNER_URL/healthz" runner "$work/runner.log"

echo "==> seeding the problem bank"
"$bin" admin seed-problems >"$work/seed.log" 2>&1 || { tail -20 "$work/seed.log" >&2; exit 1; }

org="E2E $(date +%s)"
echo "==> creating org \"$org\""
"$bin" admin create-org --name "$org" --admin-email "e2e-admin@example.test" >"$work/org.log" 2>&1 \
  || { tail -20 "$work/org.log" >&2; exit 1; }
E2E_PASSWORD_SET_URL="$(grep -oE "$BASE_URL/app/reset/[A-Za-z0-9_-]+" "$work/org.log" | head -1)"
if [ -z "$E2E_PASSWORD_SET_URL" ]; then
  echo "web/e2e/run.sh: create-org printed no password-set link" >&2
  cat "$work/org.log" >&2
  exit 1
fi
export E2E_PASSWORD_SET_URL
export E2E_ADMIN_EMAIL="e2e-admin@example.test"
export E2E_ADMIN_PASSWORD="e2e-admin-password"

echo "==> serve on $LISTEN_ADDR"
METRICS_ADDR=":$((E2E_LISTEN_PORT + 11))" "$bin" serve >"$work/serve.log" 2>&1 &
pids+=($!)
wait_http "$BASE_URL/app/login" serve "$work/serve.log"

echo "==> worker"
METRICS_ADDR=":$((E2E_LISTEN_PORT + 12))" "$bin" worker >"$work/worker.log" 2>&1 &
pids+=($!)

echo "==> playwright"
cd "$here"
[ -d node_modules ] || npm install --no-audit --no-fund
npx playwright install chromium >"$work/browsers.log" 2>&1
npx playwright test "$@"
