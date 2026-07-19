import assert from "node:assert/strict";
import {
  buildEngineeringRCManifest,
  buildSourceDependencySBOM,
  sha256,
  validateEngineeringRCInputs,
  verifyEngineeringRCManifest,
} from "./engineering-rc-contract.mjs";

const sourceCommit = "a".repeat(40);
const selfHash = (base, key) => ({ ...base, [key]: sha256(base) });
const verification = selfHash({ result: "engineering_rc_passed", sourceCommit, worktreeDirty: false, commercialGA: false }, "reportHash");
assert.equal(validateEngineeringRCInputs({ verification, sourceCommit }), true);
for (const mutate of [
  (copy) => { copy.verification.worktreeDirty = true; },
  (copy) => { copy.verification.reportHash = "0".repeat(64); },
]) {
  const copy = structuredClone({ verification });
  mutate(copy);
  assert.throws(() => validateEngineeringRCInputs({ ...copy, sourceCommit }));
}

const sbom = buildSourceDependencySBOM({ sourceCommit, generatedAt: "2026-01-01T00:00:00Z", serialNumber: "urn:uuid:00000000-0000-4000-8000-000000000001", components: [{ type: "library", name: "example", version: "1.0.0" }] });
assert.equal(sbom.bomFormat, "CycloneDX");
assert.equal(sbom.metadata.component.version, sourceCommit);

const paths = [
  ".tmp/verification/macos-engineering-rc.json",
  "release-candidates/current/product-behavior-manifest.json",
  "release-candidates/current/interface-coverage.json",
  "release-candidates/current/sbom/source-dependencies.cdx.json",
  "deploy/migrations/manifest.json",
  "contracts/openapi/lites.current.openapi.json",
  "docs/releases/engineering-rc-known-limitations.md",
  "docs/releases/engineering-rc-release-notes.md",
];
const artifacts = paths.map((path) => ({ path, sha256: "c".repeat(64), sizeBytes: 1 }));
const manifest = buildEngineeringRCManifest({ sourceCommit, generatedAt: "2026-01-01T00:00:00Z", dependencyComponents: 1, artifacts });
assert.equal(verifyEngineeringRCManifest(manifest), true);
assert.throws(() => verifyEngineeringRCManifest({ ...manifest, commercialGA: true }), /invalid or has been tampered/);
assert.throws(() => buildEngineeringRCManifest({ sourceCommit, generatedAt: "2026-01-01T00:00:00Z", dependencyComponents: 1, artifacts: artifacts.slice(1) }), /artifact set/);
console.log("Engineering RC contract self-test passed: clean inputs accepted; dirty, forged and incomplete evidence rejected");
