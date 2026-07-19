import { createHash } from "node:crypto";

export const engineeringRCDeferredProductionValidation = Object.freeze([
  "linux_kvm_firecracker",
  "official_cloud_multi_az",
  "capacity_and_recovery_drills",
  "external_penetration_test",
  "real_model_bilingual_review",
  "design_partner_pilot",
  "legal_activation",
  "first_customer_delivery_drill",
]);

export const sha256 = (value) => createHash("sha256").update(
  typeof value === "string" || Buffer.isBuffer(value) ? value : JSON.stringify(value),
).digest("hex");

const without = (value, key) => Object.fromEntries(Object.entries(value).filter(([name]) => name !== key));
const hex40 = /^[0-9a-f]{40}$/;
const hex64 = /^[0-9a-f]{64}$/;

export function validateEngineeringRCInputs({ verification, releaseIssues, sourceCommit }) {
  if (!hex40.test(sourceCommit ?? "")) throw new Error("source commit is invalid");
  if (
    verification?.result !== "engineering_rc_passed"
    || verification.sourceCommit !== sourceCommit
    || verification.worktreeDirty !== false
    || verification.commercialGA !== false
    || !hex64.test(verification.reportHash ?? "")
    || verification.reportHash !== sha256(without(verification, "reportHash"))
  ) throw new Error("verification is not a clean, current, self-hashed Engineering RC pass");

  const expectedIssueBinding = sha256(`engineering-rc-issues:${sourceCommit}`);
  if (
    releaseIssues?.status !== "passed"
    || releaseIssues.sourceCommit !== sourceCommit
    || releaseIssues.worktreeDirty !== false
    || releaseIssues.rcHash !== expectedIssueBinding
    || releaseIssues.openP0 !== 0
    || releaseIssues.openP1 !== 0
    || releaseIssues.unclassifiedOpen !== 0
    || !hex64.test(releaseIssues.reportHash ?? "")
    || releaseIssues.reportHash !== sha256(without(releaseIssues, "reportHash"))
  ) throw new Error("release issue inventory is not a clean, current, self-hashed Engineering RC pass");
  return releaseIssues.reportHash;
}

export function buildSourceDependencySBOM({ sourceCommit, generatedAt, serialNumber, components }) {
  if (!hex40.test(sourceCommit ?? "") || !Number.isFinite(Date.parse(generatedAt)) || !/^urn:uuid:[0-9a-f-]{36}$/i.test(serialNumber ?? "") || !Array.isArray(components)) throw new Error("SBOM metadata is invalid");
  if (components.some((item) => typeof item?.name !== "string" || typeof item?.version !== "string" || item.name.length === 0 || item.version.length === 0)) throw new Error("SBOM component is invalid");
  return {
    bomFormat: "CycloneDX",
    specVersion: "1.5",
    serialNumber,
    version: 1,
    metadata: { timestamp: new Date(generatedAt).toISOString(), component: { type: "application", name: "lites", version: sourceCommit } },
    components,
  };
}

export function buildEngineeringRCManifest({ sourceCommit, generatedAt, releaseIssueHash, dependencyComponents, artifacts }) {
  if (!hex40.test(sourceCommit ?? "") || !Number.isFinite(Date.parse(generatedAt)) || !hex64.test(releaseIssueHash ?? "") || !Number.isInteger(dependencyComponents) || dependencyComponents < 0 || !Array.isArray(artifacts)) throw new Error("Engineering RC manifest inputs are invalid");
  const requiredSuffixes = [
    "macos-engineering-rc.json",
    "engineering-rc-issues.json",
    "product-behavior-manifest.json",
    "interface-coverage.json",
    "sbom/source-dependencies.cdx.json",
    "deploy/migrations/manifest.json",
    "contracts/openapi/lites.current.openapi.json",
    "docs/releases/engineering-rc-known-limitations.md",
    "docs/releases/engineering-rc-release-notes.md",
  ];
  if (
    new Set(artifacts.map((item) => item.path)).size !== artifacts.length
    || artifacts.some((item) => typeof item?.path !== "string" || !hex64.test(item.sha256 ?? "") || !Number.isInteger(item.sizeBytes) || item.sizeBytes < 1)
    || requiredSuffixes.some((suffix) => !artifacts.some((item) => item.path.endsWith(suffix)))
  ) throw new Error("Engineering RC artifact set is incomplete or invalid");
  const base = {
    schemaVersion: "1.0.0",
    releaseKind: "engineering_release_candidate",
    result: "engineering_rc_passed",
    commercialGA: false,
    sourceCommit,
    generatedAt: new Date(generatedAt).toISOString(),
    immutable: true,
    evidenceSemantics: { passed: "executed on this commit", deferred: "outside macOS Engineering RC", fixture_validated: "validator or contract only", failed: "requirement not met" },
    deferredProductionValidation: [...engineeringRCDeferredProductionValidation],
    releaseIssues: { openP0: 0, openP1: 0, unclassifiedOpen: 0, reportHash: releaseIssueHash },
    dependencyComponents,
    artifacts,
  };
  return { ...base, engineeringReleaseCandidateHash: sha256(base) };
}

export function verifyEngineeringRCManifest(manifest) {
  const { engineeringReleaseCandidateHash, ...base } = manifest ?? {};
  if (
    manifest?.releaseKind !== "engineering_release_candidate"
    || manifest.result !== "engineering_rc_passed"
    || manifest.commercialGA !== false
    || manifest.immutable !== true
    || !hex40.test(manifest.sourceCommit ?? "")
    || !hex64.test(engineeringReleaseCandidateHash ?? "")
    || engineeringReleaseCandidateHash !== sha256(base)
    || JSON.stringify(manifest.deferredProductionValidation) !== JSON.stringify(engineeringRCDeferredProductionValidation)
  ) throw new Error("Engineering RC manifest is invalid or has been tampered with");
  return true;
}
