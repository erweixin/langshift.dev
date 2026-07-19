#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${root}"

preflight_fail() {
  local reason="$1"
  local detail="$2"
  echo "${detail}" >&2
  LITES_MACOS_PRODUCT_FAILURE_REASON="${reason}" \
    LITES_MACOS_PRODUCT_FAILURE_DETAIL="${detail}" \
    node scripts/write-macos-product-stack-report.mjs failed || true
  exit 2
}

[[ "$(uname -s)" == "Darwin" ]] || preflight_fail "unsupported_platform" "the macOS product stack gate must run on Darwin"
command -v docker >/dev/null || preflight_fail "docker_cli_missing" "Docker Desktop is required"
node scripts/check-playwright-browser.mjs >/dev/null || preflight_fail "playwright_browser_unavailable" "Playwright Chromium is missing or cannot launch; run: npx playwright install chromium"

# The complete first build and pull needs several GiB even when the final
# images share layers, but Docker Desktop manages that capacity separately from
# the host working directory. Keep a small host safety floor for generated
# artifacts and the database backup instead of pretending host `df` can prove
# Docker's cold-build capacity. Callers may raise it for their own allocation.
# Serial Compose work also avoids the concurrent pull map race present in
# Docker Compose 2.35.1.
required_free_gib="${LITES_MACOS_MIN_FREE_GIB:-2}"
[[ "${required_free_gib}" =~ ^[1-9][0-9]?$ ]] && (( required_free_gib <= 64 )) || preflight_fail "invalid_disk_threshold" "LITES_MACOS_MIN_FREE_GIB must be an integer from 1 through 64"
required_free_kib=$((required_free_gib * 1024 * 1024))
available_free_kib="$(df -Pk "${root}" | awk 'NR==2 {print $4}')"
[[ "${available_free_kib}" =~ ^[0-9]+$ ]] || preflight_fail "disk_space_unknown" "could not determine available disk space"
if (( available_free_kib < required_free_kib )); then
  preflight_fail "insufficient_disk_space" "at least ${required_free_gib} GiB of free disk space is required for the local product stack (available: $((available_free_kib / 1024)) MiB)"
fi
export COMPOSE_PARALLEL_LIMIT=1
node scripts/check-docker-daemon.mjs --timeout-ms 15000 >/dev/null || preflight_fail "docker_daemon_unavailable" "Docker Desktop daemon did not become ready within 15 seconds"

work="$(mktemp -d /private/tmp/lites-macos-product.XXXXXX)"
project="lites-macos-$PPID-$$"
compose=(docker compose --project-name "${project}" -f deploy/compose/foundation.compose.yaml -f deploy/compose/local-product.compose.yaml)
keep="${KEEP_MACOS_PRODUCT_STACK:-false}"
result=failed
failure_reason=stack_execution_failed
failure_detail=
current_step=initialization
started=false

cleanup_project_resources() {
  [[ "${project}" =~ ^lites-macos-[0-9]+-[0-9]+$ ]] || {
    echo "refusing to clean unexpected Compose project: ${project}" >&2
    return 1
  }
  local containers=() volumes=() networks=() images=() resource
  while IFS= read -r resource; do [[ -n "${resource}" ]] && containers+=("${resource}"); done < <(docker ps -aq --filter "label=com.docker.compose.project=${project}" 2>/dev/null)
  if (( ${#containers[@]} )); then docker rm -f "${containers[@]}" >/dev/null 2>&1 || true; fi
  while IFS= read -r resource; do [[ -n "${resource}" ]] && volumes+=("${resource}"); done < <(docker volume ls -q --filter "label=com.docker.compose.project=${project}" 2>/dev/null)
  if (( ${#volumes[@]} )); then docker volume rm "${volumes[@]}" >/dev/null 2>&1 || true; fi
  while IFS= read -r resource; do [[ -n "${resource}" ]] && networks+=("${resource}"); done < <(docker network ls -q --filter "label=com.docker.compose.project=${project}" 2>/dev/null)
  if (( ${#networks[@]} )); then docker network rm "${networks[@]}" >/dev/null 2>&1 || true; fi
  while IFS= read -r resource; do
    case "${resource}" in
      "${project}-"*:latest) images+=("${resource}") ;;
      "") ;;
      *) echo "refusing to remove image outside verification project: ${resource}" >&2 ;;
    esac
  done < <(docker image ls --filter "reference=${project}-*" --format '{{.Repository}}:{{.Tag}}' 2>/dev/null)
  if (( ${#images[@]} )); then docker image rm "${images[@]}" >/dev/null 2>&1 || true; fi
}

cleanup() {
  status=$?
  trap - EXIT INT TERM
  if [[ "${started}" == "true" ]]; then
    mkdir -p .tmp/verification
    "${compose[@]}" logs --no-color >.tmp/verification/macos-product-compose.log 2>&1 || true
    if [[ "${keep}" != "true" ]]; then
      "${compose[@]}" down --volumes --remove-orphans --rmi local >/dev/null 2>&1 || true
      # Compose can return after a partial teardown when Docker Desktop is
      # recovering. The project label is random and process-scoped, so remove
      # only resources that still belong to this exact verification project.
      cleanup_project_resources || true
    fi
  elif [[ "${keep}" != "true" ]]; then
    # A failed build can create tagged project images before any service starts.
    cleanup_project_resources || true
  fi
  if [[ "${result}" != "passed" && -z "${failure_detail}" ]]; then
    failure_detail="step ${current_step} exited with status ${status}"
  fi
  LITES_MACOS_PRODUCT_FAILURE_REASON="${failure_reason}" \
    LITES_MACOS_PRODUCT_FAILURE_DETAIL="${failure_detail}" \
    node scripts/write-macos-product-stack-report.mjs "${result}" || true
  if [[ "${keep}" == "true" ]]; then
    echo "preserved diagnostic project=${project} work=${work}" >&2
  else
    case "${work}" in
      /private/tmp/lites-macos-product.*) rm -rf -- "${work}" ;;
      *) echo "refusing to remove unexpected work directory: ${work}" >&2 ;;
    esac
  fi
  exit "${status}"
}
trap cleanup EXIT INT TERM

# Build before issuing the five-minute signed local status document. Placeholder
# values satisfy Compose interpolation only; no service is started in this pass.
mkdir -p "${work}/bootstrap"
current_step=bootstrap_compose_config
export LITES_LOCAL_ARTIFACTS_DIR="${work}/bootstrap"
export POSTGRES_PASSWORD_FILE="${work}/bootstrap/postgres" MINIO_ROOT_USER_FILE="${work}/bootstrap/minio-user" MINIO_ROOT_PASSWORD_FILE="${work}/bootstrap/minio-password" GRAFANA_ADMIN_PASSWORD_FILE="${work}/bootstrap/grafana"
export NATS_USER=unused NATS_PASSWORD=unused VALKEY_PASSWORD=unused VAULT_DEV_ROOT_TOKEN=unused POSTGRES_PASSWORD=unused MINIO_ROOT_USER=unused MINIO_ROOT_PASSWORD=unused MODEL_ADAPTER_TOKEN=unused
export POSTGRES_ADMIN_DATABASE_URL=postgresql://postgres:unused@postgres:5432/lites?sslmode=disable
export PROMPT_ARTIFACT_HASH="$(printf 'a%.0s' {1..64})" MODEL_ROUTE_ARTIFACT_HASH="$(printf 'b%.0s' {1..64})" TOOL_REGISTRY_ARTIFACT_HASH="$(printf 'c%.0s' {1..64})" PROVIDER_REGISTRY_FILE_HASH="$(printf 'd%.0s' {1..64})"
"${compose[@]}" config --quiet
current_step=compose_build
"${compose[@]}" build --quiet

artifact_directory="${work}/artifacts"
current_step=local_artifact_generation
go run ./cmd/lites-local-artifacts --output "${artifact_directory}"
read_artifact() { local value; IFS= read -r value <"${artifact_directory}/$1"; printf '%s' "${value}"; }
export LITES_LOCAL_ARTIFACTS_DIR="${artifact_directory}"
export POSTGRES_PASSWORD_FILE="${artifact_directory}/postgres-password"
export MINIO_ROOT_USER_FILE="${artifact_directory}/minio-root-user"
export MINIO_ROOT_PASSWORD_FILE="${artifact_directory}/minio-root-password"
export GRAFANA_ADMIN_PASSWORD_FILE="${artifact_directory}/grafana-admin-password"
export NATS_USER=unused NATS_PASSWORD="$(read_artifact nats-password)"
export VALKEY_PASSWORD="$(read_artifact valkey-password)"
export VAULT_DEV_ROOT_TOKEN="$(read_artifact vault-token)"
export POSTGRES_PASSWORD="$(read_artifact postgres-password)"
export MINIO_ROOT_USER="$(read_artifact minio-root-user)"
export MINIO_ROOT_PASSWORD="$(read_artifact minio-root-password)"
export MODEL_ADAPTER_TOKEN="$(read_artifact model-adapter-token)"
export POSTGRES_ADMIN_DATABASE_URL="$(read_artifact database-url-postgres)"
export PROMPT_ARTIFACT_HASH="$(read_artifact prompt-artifact-hash)"
export MODEL_ROUTE_ARTIFACT_HASH="$(read_artifact model-route-artifact-hash)"
export TOOL_REGISTRY_ARTIFACT_HASH="$(read_artifact tool-registry-artifact-hash)"
export PROVIDER_REGISTRY_FILE_HASH="$(read_artifact provider-registry-artifact-hash)"

started=true
current_step=runtime_compose_config
"${compose[@]}" config --quiet
current_step=compose_up_wait
if ! "${compose[@]}" up -d --no-build --wait --wait-timeout 240 2>"${work}/compose-up.stderr"; then
  failure_detail="$(tail -c 4000 "${work}/compose-up.stderr")"
  cat "${work}/compose-up.stderr" >&2
  exit 1
fi

current_step=web_readiness
ready=false
for _ in {1..120}; do
  if curl --fail --silent --show-error http://127.0.0.1:3118/en/onboarding >/dev/null; then
    ready=true
    break
  fi
  sleep 1
done
[[ "${ready}" == "true" ]] || { failure_detail="web product route did not become ready within 120 seconds"; echo "${failure_detail}" >&2; exit 1; }

current_step=playwright_no_demo_journey
LITES_E2E_REUSE_PRODUCT_STACK=true LITES_E2E_MAIL_API=http://127.0.0.1:8025/api/v1 npm --prefix apps/web run test:e2e

current_step=database_backup_restore
backup="${work}/lites.dump"
"${compose[@]}" exec -T postgres pg_dump -U postgres --format=custom lites >"${backup}"
[[ -s "${backup}" ]] || { echo "database backup is empty" >&2; exit 1; }
"${compose[@]}" exec -T postgres createdb -U postgres lites_restore_smoke
"${compose[@]}" exec -T postgres pg_restore -U postgres --dbname=lites_restore_smoke --no-owner --exit-on-error <"${backup}"
source_counts="$("${compose[@]}" exec -T postgres psql -U postgres -d lites -Atc "SELECT (SELECT count(*) FROM identity.users)||':'||(SELECT count(*) FROM product.missions)||':'||(SELECT count(*) FROM product.daily_tasks)||':'||(SELECT count(*) FROM product.evidence)")"
restored_counts="$("${compose[@]}" exec -T postgres psql -U postgres -d lites_restore_smoke -Atc "SELECT (SELECT count(*) FROM identity.users)||':'||(SELECT count(*) FROM product.missions)||':'||(SELECT count(*) FROM product.daily_tasks)||':'||(SELECT count(*) FROM product.evidence)")"
[[ "${source_counts}" == "${restored_counts}" && "${restored_counts}" != "0:0:0:0" ]] || { echo "database restore did not preserve the product journey" >&2; exit 1; }

result=passed
failure_reason=
failure_detail=
current_step=completed
