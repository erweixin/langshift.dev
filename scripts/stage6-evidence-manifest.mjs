import { createHash } from "node:crypto";

export const sha256 = (value) => createHash("sha256").update(value).digest("hex");

export function requiredFinalEvidencePaths(layout) {
  if (layout?.layoutVersion !== "1.0.0" || layout.root !== "release-evidence/current" || layout.signatureSuffix !== ".sigstore.json" || !layout.reports || !layout.rawEvidence || !layout.sourceInputs) throw new Error("Stage 6 evidence layout is invalid");
  const reportEntries = Object.entries(layout.reports).filter(([key]) => !["final_approval", "ga_readiness"].includes(key));
  const paths = [
    ...reportEntries.flatMap(([, relative]) => {
      const report = `${layout.root}/${relative}`;
      return [report, report.replace(/\.json$/, layout.signatureSuffix)];
    }),
    ...Object.values(layout.rawEvidence).map((relative) => `${layout.root}/${relative}`),
    ...Object.values(layout.sourceInputs),
    "release-candidates/current/ga-release-candidate.json",
    "release-candidates/current/ga-release-candidate.sigstore.json",
    "release-candidates/current/product-behavior-manifest.json",
    "release-candidates/current/account-erasure-100.json",
    "release-candidates/current/account-erasure-100.sigstore.json",
    "release-candidates/current/evidence/account-erasure-integration.json",
    "release-candidates/current/evidence/account-erasure-dependencies.json",
    "release-candidates/current/release-issues.json",
    "release-candidates/current/release-issues.sigstore.json",
    "release-candidates/current/evidence/release-issues-snapshot.json",
    "config/legal-governance.json"
  ].sort();
  if (new Set(paths).size !== paths.length) throw new Error("Stage 6 final evidence layout contains duplicate paths");
  return paths;
}

export function calculateFinalEvidenceManifestHash({ sourceCommit, rcHash, artifacts }) {
  const manifest = { manifestVersion: "1.0.0", sourceCommit, rcHash, artifacts: [...artifacts].sort((left, right) => left.path.localeCompare(right.path)) };
  return { manifest, hash: sha256(JSON.stringify(manifest)) };
}
