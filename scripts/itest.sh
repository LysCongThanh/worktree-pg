#!/usr/bin/env bash
# Run integration tests against local docker Postgres 15-18.
# Containers: wtpg-test-pg<major> on 127.0.0.1:154<major> (outside the ephemeral range).
# They are reused when already running and left running for fast re-runs;
# remove with: docker rm -f wtpg-test-pg15 wtpg-test-pg16 wtpg-test-pg17 wtpg-test-pg18
set -euo pipefail
export POSTGRES_PASSWORD=${WTPG_ITEST_PASSWORD:-postgres} # via env, never argv
for v in ${PG_VERSIONS:-15 16 17 18}; do
  name=wtpg-test-pg$v port=154$v
  if ! docker inspect -f '{{.State.Running}}' "$name" 2>/dev/null | grep -q true; then
    docker rm -f "$name" >/dev/null 2>&1 || true
    docker run -d --name "$name" --label worktree-pg=itest -e POSTGRES_PASSWORD \
      -p "127.0.0.1:$port:5432" "postgres:$v-alpine" >/dev/null
  fi
  # readiness: TCP query from the host, not pg_isready on the socket (spike S6: the
  # init-time temporary server answers on the socket before the final restart)
  for _ in $(seq 1 120); do
    PGPASSWORD=$POSTGRES_PASSWORD psql -X -h 127.0.0.1 -p "$port" -U postgres -Atc 'select 1' postgres >/dev/null 2>&1 && break
    sleep 0.5
  done
  echo "== integration tests on postgres:$v ($port)"
  WTPG_TEST_ADMIN_DSN="postgres://postgres:$POSTGRES_PASSWORD@127.0.0.1:$port/postgres?sslmode=disable" \
    go test -race -count=1 -tags integration ./...
done
