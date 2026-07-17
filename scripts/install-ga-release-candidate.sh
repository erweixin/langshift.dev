#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
bundle="${1:-}"
certificate_identity="${2:-}"
[[ -d "${bundle}" && -n "${certificate_identity}" ]] || { echo "usage: $0 <extracted-rc-directory> <certificate-identity>" >&2; exit 2; }
bundle="$(cd "${bundle}" && pwd)"
command -v cosign >/dev/null || { echo "cosign is required" >&2; exit 2; }
command -v jq >/dev/null || { echo "jq is required" >&2; exit 2; }
[[ -z "$(git -C "${root}" status --porcelain=v1)" ]] || { echo "source worktree must be clean before installing an RC" >&2; exit 2; }
expected_identity="$(jq -er '.releaseEvidence.cosignCertificateIdentity' "${root}/deploy/private-delivery/manifest.json")"
issuer="$(jq -er '.releaseEvidence.cosignCertificateOIDCIssuer' "${root}/deploy/private-delivery/manifest.json")"
[[ "${certificate_identity}" == "${expected_identity}" ]] || { echo "certificate identity does not match the source-bound release policy" >&2; exit 2; }

for file in \
  ga-release-candidate.json ga-release-candidate.sigstore.json \
  account-erasure-100.json account-erasure-100.sigstore.json \
  release-issues.json release-issues.sigstore.json \
  product-behavior-manifest.json \
  evidence/account-erasure-integration.json evidence/account-erasure-dependencies.json \
  evidence/release-issues-snapshot.json; do
  [[ -f "${bundle}/${file}" ]] || { echo "missing RC artifact: ${file}" >&2; exit 1; }
done

cosign verify-blob --bundle "${bundle}/ga-release-candidate.sigstore.json" \
  --certificate-identity "${certificate_identity}" --certificate-oidc-issuer "${issuer}" \
  "${bundle}/ga-release-candidate.json" >/dev/null
cosign verify-blob --bundle "${bundle}/account-erasure-100.sigstore.json" \
  --certificate-identity "${certificate_identity}" --certificate-oidc-issuer "${issuer}" \
  "${bundle}/account-erasure-100.json" >/dev/null
cosign verify-blob --bundle "${bundle}/release-issues.sigstore.json" \
  --certificate-identity "${certificate_identity}" --certificate-oidc-issuer "${issuer}" \
  "${bundle}/release-issues.json" >/dev/null

source_commit="$(jq -er '.sourceCommit | select(test("^[0-9a-f]{40}$"))' "${bundle}/ga-release-candidate.json")"
rc_hash="$(jq -er '.releaseCandidateHash | select(test("^[0-9a-f]{64}$"))' "${bundle}/ga-release-candidate.json")"
[[ "$(git -C "${root}" rev-parse HEAD)" == "${source_commit}" ]] || { echo "RC source commit does not match HEAD" >&2; exit 1; }
jq -e --arg commit "${source_commit}" '.schemaVersion=="1.0.0" and .releaseKind=="ga_release_candidate" and .immutable==true and .sourceCommit==$commit and (.images|length)>0 and all(.images[]; .signatureVerified==true and .sbomAttested==true and .provenanceMode=="max" and (.image|type=="string" and length>0) and (.digest|test("^sha256:[0-9a-f]{64}$")))' "${bundle}/ga-release-candidate.json" >/dev/null
jq -e --arg commit "${source_commit}" --arg rc "${rc_hash}" '.reportVersion=="1.0.0" and .kind=="account-erasure-100" and .status=="passed" and .worktreeDirty==false and .sourceCommit==$commit and .rcHash==$rc and .accounts==100 and .readableSurfacesAfter==0 and .completeReceipts==100 and .restoreRedeletions==100 and .totalSurfaceReceiptRowsAfterRestore==1200 and (.dependencyGate.tests|length)==3 and all(.dependencyGate.runtimes[]; test("@sha256:[0-9a-f]{64}$"))' "${bundle}/account-erasure-100.json" >/dev/null
jq -e --arg commit "${source_commit}" --arg rc "${rc_hash}" '.reportVersion=="1.0.0" and .kind=="release-issues" and .status=="passed" and .worktreeDirty==false and .sourceCommit==$commit and .rcHash==$rc and .openP0==0 and .openP1==0 and .unclassifiedOpen==0 and .snapshot.paginationComplete==true' "${bundle}/release-issues.json" >/dev/null

node - "${root}/deploy/private-delivery/manifest.json" "${bundle}/ga-release-candidate.json" "${bundle}/account-erasure-100.json" "${bundle}/product-behavior-manifest.json" "${bundle}/release-issues.json" <<'NODE'
const fs = require("node:fs");
const { createHash } = require("node:crypto");
const delivery = JSON.parse(fs.readFileSync(process.argv[2], "utf8"));
const rc = JSON.parse(fs.readFileSync(process.argv[3], "utf8"));
const erasure = JSON.parse(fs.readFileSync(process.argv[4], "utf8"));
const behavior = JSON.parse(fs.readFileSync(process.argv[5], "utf8"));
const issues = JSON.parse(fs.readFileSync(process.argv[6], "utf8"));
const digest = (value) => createHash("sha256").update(JSON.stringify(value)).digest("hex");
const { releaseCandidateHash, ...rcBase } = rc;
const { reportHash, ...erasureBase } = erasure;
const { manifestHash, ...behaviorBase } = behavior;
const { reportHash: issueReportHash, ...issueBase } = issues;
const expected = [...delivery.releaseImages, "reference-tool-runtime"].sort();
const actual = rc.images.map((item) => item.service).sort();
const expectedRCArtifacts = [...delivery.requiredSourceArtifacts, "deploy/private-delivery/manifest.json", "behavior-manifests/schema.json", "behavior-manifests/impact-matrix.json"].sort();
const actualRCArtifacts = rc.artifacts.map((item) => item.path).sort();
const requiredErasureArtifacts = ["cmd/account-erasure-worker/main.go", "internal/identity/erasure/service.go", "internal/identity/postgres/account_erasure_surface.go", "deploy/migrations/000084_account_erasure_receipts.up.sql", "scripts/install-ga-release-candidate.sh", "scripts/stage6-account-erasure-gate.sh", "scripts/write-stage6-account-erasure-report.mjs", ".github/workflows/supply-chain.yml"];
if (JSON.stringify(expected) !== JSON.stringify(actual) || new Set(actual).size !== actual.length || JSON.stringify(expectedRCArtifacts) !== JSON.stringify(actualRCArtifacts) || new Set(actualRCArtifacts).size !== actualRCArtifacts.length || !requiredErasureArtifacts.every((path) => erasure.artifacts.some((item) => item.path === path)) || new Set(erasure.artifacts.map((item) => item.path)).size !== erasure.artifacts.length || digest(rcBase) !== releaseCandidateHash || digest(erasureBase) !== reportHash || digest(issueBase) !== issueReportHash || behavior.sourceCommit !== rc.sourceCommit || digest(behaviorBase) !== manifestHash || rc.behaviorManifest.manifestHash !== manifestHash) process.exit(1);
NODE

verify_hash() {
  local expected="$1" file="$2" actual
  actual="$(shasum -a 256 "${file}" | awk '{print $1}')"
  [[ "${actual}" == "${expected}" ]] || { echo "hash mismatch: ${file}" >&2; exit 1; }
}
verify_hash "$(jq -er '.test.evidenceSha256' "${bundle}/account-erasure-100.json")" "${bundle}/evidence/account-erasure-integration.json"
verify_hash "$(jq -er '.dependencyGate.evidenceSha256' "${bundle}/account-erasure-100.json")" "${bundle}/evidence/account-erasure-dependencies.json"
verify_hash "$(jq -er '.snapshot.sha256' "${bundle}/release-issues.json")" "${bundle}/evidence/release-issues-snapshot.json"
verify_hash "$(jq -er '.behaviorManifest.sha256' "${bundle}/ga-release-candidate.json")" "${bundle}/product-behavior-manifest.json"

while IFS=$'\t' read -r path expected; do
  actual="$(git -C "${root}" show "${source_commit}:${path}" | shasum -a 256 | awk '{print $1}')"
  [[ "${actual}" == "${expected}" ]] || { echo "RC source artifact mismatch: ${path}" >&2; exit 1; }
done < <(jq -er '.artifacts[] | [.path,.sha256] | @tsv' "${bundle}/ga-release-candidate.json")
while IFS=$'\t' read -r path expected; do
  actual="$(git -C "${root}" show "${source_commit}:${path}" | shasum -a 256 | awk '{print $1}')"
  [[ "${actual}" == "${expected}" ]] || { echo "erasure source artifact mismatch: ${path}" >&2; exit 1; }
done < <(jq -er '.artifacts[] | [.path,.sha256] | @tsv' "${bundle}/account-erasure-100.json")

mkdir -p "${root}/release-candidates"
incoming="$(mktemp -d "${root}/release-candidates/.incoming.XXXXXX")"
cleanup() { [[ -d "${incoming:-}" ]] && rm -rf "${incoming}"; }
trap cleanup EXIT
install -m 0644 "${bundle}/ga-release-candidate.json" "${bundle}/ga-release-candidate.sigstore.json" \
  "${bundle}/account-erasure-100.json" "${bundle}/account-erasure-100.sigstore.json" \
  "${bundle}/release-issues.json" "${bundle}/release-issues.sigstore.json" \
  "${bundle}/product-behavior-manifest.json" "${incoming}/"
mkdir -p "${incoming}/evidence"
install -m 0644 "${bundle}/evidence/account-erasure-integration.json" "${bundle}/evidence/account-erasure-dependencies.json" "${incoming}/evidence/"
install -m 0644 "${bundle}/evidence/release-issues-snapshot.json" "${incoming}/evidence/"
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
