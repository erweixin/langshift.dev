#!/usr/bin/env bash
set -euo pipefail

container_name="lites-foundation-pg-${RANDOM}-${RANDOM}"
cleanup() { docker rm -f "${container_name}" >/dev/null 2>&1 || true; }
trap cleanup EXIT

docker run -d \
  --name "${container_name}" \
  -p 127.0.0.1::5432 \
  --health-cmd="pg_isready -U postgres -d lites_foundation" \
  --health-interval=1s --health-timeout=3s --health-retries=30 \
  -e POSTGRES_PASSWORD=foundation_admin \
  -e POSTGRES_DB=lites_foundation \
  -v "$(pwd)/contracts/database:/docker-entrypoint-initdb.d:ro" \
  postgres:16-alpine >/dev/null

for _ in $(seq 1 40); do
  status="$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "${container_name}")"
  [[ "${status}" == "healthy" ]] && break
  if [[ "${status}" == "exited" || "${status}" == "dead" || "${status}" == "unhealthy" ]]; then docker logs "${container_name}"; exit 1; fi
  sleep 1
done
[[ "$(docker inspect --format '{{.State.Health.Status}}' "${container_name}")" == "healthy" ]] || { docker logs "${container_name}"; exit 1; }

docker exec "${container_name}" psql -v ON_ERROR_STOP=1 -U postgres -d lites_foundation -c \
  "CREATE ROLE lites_identity_service LOGIN PASSWORD 'foundation_service' NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOBYPASSRLS; GRANT CONNECT ON DATABASE lites_foundation TO lites_identity_service; GRANT USAGE ON SCHEMA identity, agent TO lites_identity_service; GRANT SELECT ON identity.users, identity.memberships TO lites_identity_service; GRANT SELECT, UPDATE ON identity.sessions TO lites_identity_service; GRANT INSERT ON identity.security_events TO lites_identity_service; GRANT SELECT, INSERT, UPDATE ON agent.idempotency_responses, agent.event_cursors TO lites_identity_service; GRANT SELECT, INSERT ON agent.events, agent.outbox TO lites_identity_service;" >/dev/null

container_port="$(docker port "${container_name}" 5432/tcp | head -n 1 | sed 's/.*://')"

LITES_TEST_ADMIN_DATABASE_URL="postgres://postgres:foundation_admin@127.0.0.1:${container_port}/lites_foundation?sslmode=disable" \
LITES_TEST_IDENTITY_DATABASE_URL="postgres://lites_identity_service:foundation_service@127.0.0.1:${container_port}/lites_foundation?sslmode=disable" \
GOCACHE=/tmp/lites-go-build GOMODCACHE=/tmp/lites-go-mod go test -count=1 -tags=integration ./internal/identity/postgres ./internal/eventstore/postgres
