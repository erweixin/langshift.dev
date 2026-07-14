#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
runtime="$(mktemp -d "${TMPDIR:-/tmp}/lites-tofu.XXXXXX")"
trap 'rm -rf "${runtime}"' EXIT

tofu_image="ghcr.io/opentofu/opentofu:1.11.7@sha256:6166f12d09520dbbb431a13951973c5b1046c01f801fa8c7b73e89511a0fff34"
trivy_image="aquasec/trivy:0.72.0@sha256:cffe3f5161a47a6823fbd23d985795b3ed72a4c806da4c4df16266c02accdd6f"
tofu_root="/workspace/deploy/tofu/official-cloud"
mkdir -p "${runtime}/data" "${runtime}/trivy-cache"

docker run --rm --volume "${root}:/workspace:ro" --workdir /workspace "${tofu_image}" \
  fmt -check -recursive deploy/tofu
docker run --rm --volume "${root}:/workspace:ro" --volume "${runtime}/data:/tofu-data" \
  --env TF_DATA_DIR=/tofu-data --workdir "${tofu_root}" "${tofu_image}" \
  init -backend=false -lockfile=readonly
docker run --rm --volume "${root}:/workspace:ro" --volume "${runtime}/data:/tofu-data" \
  --env TF_DATA_DIR=/tofu-data --workdir "${tofu_root}" "${tofu_image}" validate
node "${root}/scripts/validate-stage2-tofu-contract.mjs" "${root}/deploy/tofu"
docker run --rm --volume "${root}:/workspace:ro" --volume "${runtime}:/output" \
  --volume "${runtime}/trivy-cache:/root/.cache/trivy" --workdir /workspace "${trivy_image}" \
  config --severity HIGH,CRITICAL --exit-code 1 --format json \
  --output /output/tofu-trivy.json deploy/tofu
printf 'stage-2 tofu security: high_critical_misconfigurations=0 status=passed\n'
