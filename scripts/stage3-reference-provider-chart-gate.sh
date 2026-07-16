#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
runtime="$(mktemp -d "${TMPDIR:-/tmp}/lites-reference-provider-chart.XXXXXX")"
trap 'rm -rf "${runtime}"' EXIT

helm_image="alpine/helm:3.19.0@sha256:aef9b56f64e866207d9591d0abd8f6d767b36aadd12edf68f8a719716d9d29c9"
kubeconform_image="ghcr.io/yannh/kubeconform:v0.7.0@sha256:85dbef6b4b312b99133decc9c6fc9495e9fc5f92293d4ff3b7e1b30f5611823c"
trivy_image="aquasec/trivy:0.72.0@sha256:cffe3f5161a47a6823fbd23d985795b3ed72a4c806da4c4df16266c02accdd6f"
chart="deploy/helm/reference-provider"
fixture="${chart}/values.schema-test.yaml"

docker run --rm --volume "${root}:/workspace:ro" --workdir /workspace "${helm_image}" lint "${chart}" --strict --values "${fixture}"
docker run --rm --volume "${root}:/workspace:ro" --volume "${runtime}:/output" --workdir /workspace "${helm_image}" template reference-provider "${chart}" --namespace reference-provider --values "${fixture}" --output-dir /output >/dev/null
rendered="${runtime}/lites-reference-provider/templates/resources.yaml"
docker run --rm --volume "${rendered}:/manifest.yaml:ro" "${kubeconform_image}" -strict -summary -ignore-missing-schemas -kubernetes-version 1.30.0 /manifest.yaml
node "${root}/scripts/validate-stage3-reference-provider-render.mjs" "${rendered}"
mkdir -p "${runtime}/trivy-cache"
docker run --rm --volume "${root}:/workspace:ro" --volume "${runtime}:/output" --volume "${runtime}/trivy-cache:/root/.cache/trivy" --workdir /workspace "${trivy_image}" config --helm-values "${fixture}" --helm-kube-version 1.30.0 --severity HIGH,CRITICAL --exit-code 1 --format json --output /output/trivy.json "${chart}"
printf 'stage-3 reference provider chart security: high_critical_misconfigurations=0 status=passed\n'
