#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
runtime="$(mktemp -d "${TMPDIR:-/tmp}/lites-tofu.XXXXXX")"
trap 'rm -rf "${runtime}"' EXIT

tofu_image="ghcr.io/opentofu/opentofu:1.11.7@sha256:6166f12d09520dbbb431a13951973c5b1046c01f801fa8c7b73e89511a0fff34"
trivy_image="aquasec/trivy:0.72.0@sha256:cffe3f5161a47a6823fbd23d985795b3ed72a4c806da4c4df16266c02accdd6f"
tofu_root="/tofu-work/official-cloud"
provider_cache="${root}/.tmp/cache/opentofu-providers"
mkdir -p "${runtime}/data" "${runtime}/trivy-cache" "${runtime}/tofu"
mkdir -p "${provider_cache}"
[[ -s "${root}/deploy/tofu/official-cloud/.terraform.lock.hcl" ]] || { echo "committed OpenTofu provider lock is missing" >&2; exit 1; }
cp -R "${root}/deploy/tofu/." "${runtime}/tofu/"

docker run --rm --volume "${root}:/workspace:ro" --workdir /workspace "${tofu_image}" \
  fmt -check -recursive deploy/tofu
init_passed=false
for attempt in 1 2 3; do
  # OpenTofu adds the current Linux container package hash after verifying it
  # against the signed zh hashes already committed in the lock file. Perform
  # that architecture-specific normalization only in the temporary copy so
  # Intel and Apple Silicon Docker Desktop hosts share one gate.
  if docker run --rm --volume "${runtime}/tofu:/tofu-work" --volume "${runtime}/data:/tofu-data" \
    --volume "${provider_cache}:/tofu-plugin-cache" --env TF_DATA_DIR=/tofu-data \
    --env TF_PLUGIN_CACHE_DIR=/tofu-plugin-cache --workdir "${tofu_root}" "${tofu_image}" \
    init -backend=false; then
    init_passed=true
    break
  fi
  if [[ "${attempt}" -lt 3 ]]; then
    printf 'OpenTofu provider initialization attempt %s failed; retrying\n' "${attempt}" >&2
    sleep "$((attempt * 2))"
  fi
done
[[ "${init_passed}" == "true" ]] || { echo "OpenTofu provider initialization failed after 3 attempts" >&2; exit 1; }
docker run --rm --volume "${runtime}/tofu:/tofu-work" --volume "${runtime}/data:/tofu-data" \
  --volume "${provider_cache}:/tofu-plugin-cache" --env TF_DATA_DIR=/tofu-data \
  --env TF_PLUGIN_CACHE_DIR=/tofu-plugin-cache --workdir "${tofu_root}" "${tofu_image}" validate
node "${root}/scripts/validate-stage2-tofu-contract.mjs" "${root}/deploy/tofu"
docker run --rm --volume "${root}:/workspace:ro" --volume "${runtime}:/output" \
  --volume "${runtime}/trivy-cache:/root/.cache/trivy" --workdir /workspace "${trivy_image}" \
  config --severity HIGH,CRITICAL --exit-code 1 --format json \
  --output /output/tofu-trivy.json deploy/tofu
printf 'stage-2 tofu security: high_critical_misconfigurations=0 status=passed\n'
