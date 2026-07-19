#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
bundle="${1:-}"
certificate_identity="${2:-}"
[[ -d "${bundle}" && -n "${certificate_identity}" ]] || { echo "usage: $0 <extracted-rc-directory> <certificate-identity>" >&2; exit 2; }
bundle="$(cd "${bundle}" && pwd)"
command -v cosign >/dev/null || { echo "cosign is required" >&2; exit 2; }
[[ -z "$(git -C "${root}" status --porcelain=v1)" ]] || { echo "source worktree must be clean before installing an RC" >&2; exit 2; }
policy_values="$(node - "${root}/deploy/private-delivery/manifest.json" <<'NODE'
const fs = require("node:fs");
const value = JSON.parse(fs.readFileSync(process.argv[2], "utf8")).releaseEvidence;
const fields = [value?.cosignCertificateIdentity, value?.cosignCertificateOIDCIssuer];
if (fields.some((item) => typeof item !== "string" || item.length < 1 || /[\t\r\n]/.test(item))) process.exit(1);
process.stdout.write(fields.join("\t"));
NODE
)"
IFS=$'\t' read -r expected_identity issuer <<<"${policy_values}"
[[ "${certificate_identity}" == "${expected_identity}" ]] || { echo "certificate identity does not match the source-bound release policy" >&2; exit 2; }

for file in \
  ga-release-candidate.json ga-release-candidate.sigstore.json \
  account-erasure-100.json account-erasure-100.sigstore.json \
  product-behavior-manifest.json \
  evidence/account-erasure-integration.json evidence/account-erasure-dependencies.json; do
  [[ -f "${bundle}/${file}" ]] || { echo "missing RC artifact: ${file}" >&2; exit 1; }
done

cosign verify-blob --bundle "${bundle}/ga-release-candidate.sigstore.json" \
  --certificate-identity "${certificate_identity}" --certificate-oidc-issuer "${issuer}" \
  "${bundle}/ga-release-candidate.json" >/dev/null
cosign verify-blob --bundle "${bundle}/account-erasure-100.sigstore.json" \
  --certificate-identity "${certificate_identity}" --certificate-oidc-issuer "${issuer}" \
  "${bundle}/account-erasure-100.json" >/dev/null
bundle_values="$(node - "${root}/deploy/private-delivery/manifest.json" "${bundle}/ga-release-candidate.json" "${bundle}/account-erasure-100.json" "${bundle}/product-behavior-manifest.json" <<'NODE'
const fs = require("node:fs");
const { createHash } = require("node:crypto");
const delivery = JSON.parse(fs.readFileSync(process.argv[2], "utf8"));
const rc = JSON.parse(fs.readFileSync(process.argv[3], "utf8"));
const erasure = JSON.parse(fs.readFileSync(process.argv[4], "utf8"));
const behavior = JSON.parse(fs.readFileSync(process.argv[5], "utf8"));
const digest = (value) => createHash("sha256").update(JSON.stringify(value)).digest("hex");
const { releaseCandidateHash, ...rcBase } = rc;
const { reportHash, ...erasureBase } = erasure;
const { manifestHash, ...behaviorBase } = behavior;
const expected = [...delivery.releaseImages, "reference-tool-runtime"].sort();
const actual = rc.images.map((item) => item.service).sort();
const expectedRCArtifacts = [...delivery.requiredSourceArtifacts, "deploy/private-delivery/manifest.json", "behavior-manifests/schema.json", "behavior-manifests/impact-matrix.json"].sort();
const actualRCArtifacts = rc.artifacts.map((item) => item.path).sort();
const requiredErasureArtifacts = ["cmd/account-erasure-worker/main.go", "internal/identity/erasure/service.go", "internal/identity/postgres/account_erasure_surface.go", "deploy/migrations/000084_account_erasure_receipts.up.sql", "scripts/install-ga-release-candidate.sh", "scripts/stage6-account-erasure-gate.sh", "scripts/write-stage6-account-erasure-report.mjs", ".github/workflows/supply-chain.yml"];
const hex40 = /^[0-9a-f]{40}$/;
const hex64 = /^[0-9a-f]{64}$/;
const runtimeDigest = /@sha256:[0-9a-f]{64}$/;
const validRC = rc.schemaVersion === "1.0.0" && rc.releaseKind === "ga_release_candidate" && rc.immutable === true && hex40.test(rc.sourceCommit ?? "") && hex64.test(releaseCandidateHash ?? "") && Array.isArray(rc.images) && rc.images.length > 0 && rc.images.every((item) => item.signatureVerified === true && item.sbomAttested === true && item.provenanceMode === "max" && typeof item.image === "string" && item.image.length > 0 && /^sha256:[0-9a-f]{64}$/.test(item.digest ?? ""));
const validErasure = erasure.reportVersion === "1.0.0" && erasure.kind === "account-erasure-100" && erasure.status === "passed" && erasure.worktreeDirty === false && erasure.sourceCommit === rc.sourceCommit && erasure.rcHash === releaseCandidateHash && erasure.accounts === 100 && erasure.readableSurfacesAfter === 0 && erasure.completeReceipts === 100 && erasure.restoreRedeletions === 100 && erasure.totalSurfaceReceiptRowsAfterRestore === 1200 && Array.isArray(erasure.dependencyGate?.tests) && erasure.dependencyGate.tests.length === 3 && Object.values(erasure.dependencyGate?.runtimes ?? {}).length === 3 && Object.values(erasure.dependencyGate.runtimes).every((item) => runtimeDigest.test(item));
const hashes = [erasure.test?.evidenceSha256, erasure.dependencyGate?.evidenceSha256, rc.behaviorManifest?.sha256];
if (!validRC || !validErasure || hashes.some((item) => !hex64.test(item ?? "")) || JSON.stringify(expected) !== JSON.stringify(actual) || new Set(actual).size !== actual.length || JSON.stringify(expectedRCArtifacts) !== JSON.stringify(actualRCArtifacts) || new Set(actualRCArtifacts).size !== actualRCArtifacts.length || !requiredErasureArtifacts.every((path) => erasure.artifacts.some((item) => item.path === path)) || new Set(erasure.artifacts.map((item) => item.path)).size !== erasure.artifacts.length || digest(rcBase) !== releaseCandidateHash || digest(erasureBase) !== reportHash || behavior.sourceCommit !== rc.sourceCommit || digest(behaviorBase) !== manifestHash || rc.behaviorManifest.manifestHash !== manifestHash) process.exit(1);
process.stdout.write([rc.sourceCommit, releaseCandidateHash, ...hashes].join("\t"));
NODE
)"
IFS=$'\t' read -r source_commit rc_hash erasure_evidence_hash erasure_dependency_hash behavior_manifest_hash <<<"${bundle_values}"
[[ "$(git -C "${root}" rev-parse HEAD)" == "${source_commit}" ]] || { echo "RC source commit does not match HEAD" >&2; exit 1; }

verify_hash() {
  local expected="$1" file="$2" actual
  actual="$(shasum -a 256 "${file}" | awk '{print $1}')"
  [[ "${actual}" == "${expected}" ]] || { echo "hash mismatch: ${file}" >&2; exit 1; }
}
verify_hash "${erasure_evidence_hash}" "${bundle}/evidence/account-erasure-integration.json"
verify_hash "${erasure_dependency_hash}" "${bundle}/evidence/account-erasure-dependencies.json"
verify_hash "${behavior_manifest_hash}" "${bundle}/product-behavior-manifest.json"

emit_artifacts() {
  node - "$1" <<'NODE'
const fs = require("node:fs");
const value = JSON.parse(fs.readFileSync(process.argv[2], "utf8"));
if (!Array.isArray(value.artifacts) || value.artifacts.some((item) => typeof item?.path !== "string" || /[\t\r\n]/.test(item.path) || !/^[0-9a-f]{64}$/.test(item.sha256 ?? ""))) process.exit(1);
for (const item of value.artifacts) process.stdout.write(`${item.path}\t${item.sha256}\n`);
NODE
}

while IFS=$'\t' read -r path expected; do
  actual="$(git -C "${root}" show "${source_commit}:${path}" | shasum -a 256 | awk '{print $1}')"
  [[ "${actual}" == "${expected}" ]] || { echo "RC source artifact mismatch: ${path}" >&2; exit 1; }
done < <(emit_artifacts "${bundle}/ga-release-candidate.json")
while IFS=$'\t' read -r path expected; do
  actual="$(git -C "${root}" show "${source_commit}:${path}" | shasum -a 256 | awk '{print $1}')"
  [[ "${actual}" == "${expected}" ]] || { echo "erasure source artifact mismatch: ${path}" >&2; exit 1; }
done < <(emit_artifacts "${bundle}/account-erasure-100.json")

mkdir -p "${root}/release-candidates"
incoming="$(mktemp -d "${root}/release-candidates/.incoming.XXXXXX")"
cleanup() { [[ -d "${incoming:-}" ]] && rm -rf "${incoming}"; }
trap cleanup EXIT
install -m 0644 "${bundle}/ga-release-candidate.json" "${bundle}/ga-release-candidate.sigstore.json" \
  "${bundle}/account-erasure-100.json" "${bundle}/account-erasure-100.sigstore.json" \
  "${bundle}/product-behavior-manifest.json" "${incoming}/"
mkdir -p "${incoming}/evidence"
install -m 0644 "${bundle}/evidence/account-erasure-integration.json" "${bundle}/evidence/account-erasure-dependencies.json" "${incoming}/evidence/"
if [[ -d "${root}/release-candidates/current" ]]; then
  previous="${root}/release-candidates/previous-$(date -u +%Y%m%dT%H%M%SZ)-${rc_hash:0:12}-$$"
  mv "${root}/release-candidates/current" "${previous}"
fi
if ! mv "${incoming}" "${root}/release-candidates/current"; then
  if [[ -n "${previous:-}" && -d "${previous}" ]]; then
    mv "${previous}" "${root}/release-candidates/current"
  fi
  echo "failed to activate GA RC; previous installation restored" >&2
  exit 1
fi
incoming=""
trap - EXIT
printf 'installed GA RC: commit=%s hash=%s\n' "${source_commit}" "${rc_hash}"
