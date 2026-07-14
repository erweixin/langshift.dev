#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
runtime="$(mktemp -d "${TMPDIR:-/tmp}/lites-nats-gate.XXXXXX")"
trap 'rm -rf "${runtime}"' EXIT
trivy_cache="${STAGE2_TRIVY_CACHE_DIR:-${runtime}/trivy-cache}"

helm_image="alpine/helm:3.19.0@sha256:aef9b56f64e866207d9591d0abd8f6d767b36aadd12edf68f8a719716d9d29c9"
kubeconform_image="ghcr.io/yannh/kubeconform:v0.7.0@sha256:85dbef6b4b312b99133decc9c6fc9495e9fc5f92293d4ff3b7e1b30f5611823c"
trivy_image="aquasec/trivy:0.72.0@sha256:cffe3f5161a47a6823fbd23d985795b3ed72a4c806da4c4df16266c02accdd6f"
chart_root="deploy/helm/nats-cell"

declare chart repository version sha256
while IFS='=' read -r key value; do printf -v "${key}" '%s' "${value}"; done < "${root}/${chart_root}/upstream.lock"

if docker run --rm --volume "${root}:/workspace:ro" --workdir /workspace "${helm_image}" lint --strict "${chart_root}" >/dev/null 2>&1; then
  printf 'NATS cell chart unexpectedly accepted empty production values\n' >&2
  exit 1
fi

docker run --rm --volume "${runtime}:/output" "${helm_image}" pull "${chart}" --repo "${repository}" --version "${version}" --destination /output
archive="${runtime}/${chart}-${version}.tgz"
actual_sha256="$(shasum -a 256 "${archive}" | awk '{print $1}')"
test "${actual_sha256}" = "${sha256}"

docker run --rm --volume "${root}:/workspace:ro" --workdir /workspace "${helm_image}" lint --strict "${chart_root}" -f "${chart_root}/values.schema-test.yaml"
docker run --rm --volume "${root}:/workspace:ro" --volume "${runtime}:/output" --workdir /workspace "${helm_image}" template lites-nats-cell "${chart_root}" --namespace data-system -f "${chart_root}/values.schema-test.yaml" --output-dir /output/render/cell
docker run --rm --volume "${root}:/workspace:ro" --volume "${runtime}:/output" --workdir /workspace "${helm_image}" template lites-nats "/output/${chart}-${version}.tgz" --namespace data-system -f "${chart_root}/upstream-values.yaml" --output-dir /output/render/upstream

docker run --rm --volume "${runtime}/render:/render:ro" "${kubeconform_image}" -strict -summary -kubernetes-version 1.30.0 -ignore-missing-schemas /render
node "${root}/scripts/validate-stage2-nats-render.mjs" "${runtime}/render"
mkdir -p "${trivy_cache}"
docker run --rm --volume "${runtime}:/output" --volume "${trivy_cache}:/root/.cache/trivy" "${trivy_image}" config --severity HIGH,CRITICAL --exit-code 1 --format json --output /output/nats-trivy.json /output/render
docker run --rm --volume "${root}:/workspace:ro" --volume "${trivy_cache}:/root/.cache/trivy" "${trivy_image}" image --severity HIGH,CRITICAL --ignore-unfixed --ignorefile /workspace/${chart_root}/.trivyignore.yaml --exit-code 1 --scanners vuln nats@sha256:1b5a0a665cbe50a4ea28e8a82cf809b26cee5027d1fcaf8682fadf8f385fdf29
printf 'stage-2 nats security: upstream_sha256=%s high_critical_misconfigurations=0 reachable_fixed_high_critical_vulnerabilities=0 temporary_unreachable_exceptions=1 status=passed\n' "${actual_sha256}"
