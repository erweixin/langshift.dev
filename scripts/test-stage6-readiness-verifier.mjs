import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { chmod, copyFile, mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const temporary = await mkdtemp(join(tmpdir(), "lites-stage6-readiness-"));
const repository = join(temporary, "repository");
const fakeBin = join(temporary, "bin");
const sha256 = (value) => createHash("sha256").update(value).digest("hex");
const hashed = (base) => ({ ...base, reportHash: sha256(JSON.stringify(base)) });
const writeJSON = async (path, value) => {
  await mkdir(dirname(path), { recursive: true });
  await writeFile(path, `${JSON.stringify(value, null, 2)}\n`);
};

await Promise.all([mkdir(repository), mkdir(fakeBin)]);
for (const [path, content] of [
  ["README.md", "readiness self-test\n"],
  ["behavior-manifests/schema.json", "{}\n"],
  ["behavior-manifests/impact-matrix.json", "{}\n"],
  ["cmd/account-erasure-worker/main.go", "package main\n"],
  ["CLA.md", "fixture\n"],
  ["CONTRIBUTING.md", "fixture\n"],
  ["TRADEMARKS.md", "fixture\n"],
  ["SECURITY.md", "fixture\n"],
  ["NOTICE", "fixture\n"],
  ["apps/web/src/components/legal-notice.tsx", "fixture\n"],
  [".gitignore", "release-candidates/\nrelease-evidence/\ngate-reports/\npilot-reports/\n"],
]) {
  const target = join(repository, path);
  await mkdir(dirname(target), { recursive: true });
  await writeFile(target, content);
}
await mkdir(join(repository, "scripts"), { recursive: true });
await copyFile(resolve(root, "scripts/write-stage6-ga-readiness-report.mjs"), join(repository, "scripts/write-stage6-ga-readiness-report.mjs"));
await copyFile(resolve(root, "scripts/stage6-approval-crypto.mjs"), join(repository, "scripts/stage6-approval-crypto.mjs"));
await copyFile(resolve(root, "scripts/stage6-evidence-manifest.mjs"), join(repository, "scripts/stage6-evidence-manifest.mjs"));
await copyFile(resolve(root, "scripts/verify-legal-governance.mjs"), join(repository, "scripts/verify-legal-governance.mjs"));
const workflowIdentity = "https://github.com/example/lites/.github/workflows/supply-chain.yml@refs/heads/main";
const issuer = "https://token.actions.githubusercontent.com";
const delivery = {
  releaseImages: ["web-app"],
  requiredSourceArtifacts: ["README.md"],
  releaseEvidence: { cosignCertificateIdentity: workflowIdentity, cosignCertificateOIDCIssuer: issuer },
  stage6EvidenceAuthorities: {
    automatedReports: [{ certificateIdentity: workflowIdentity, certificateOIDCIssuer: issuer }],
    externalPenetration: [],
    pilotReports: [],
    finalApproval: [],
  },
};
await writeJSON(join(repository, "deploy/private-delivery/manifest.json"), delivery);
await writeJSON(join(repository, "contracts/release/stage6-approver-keyring.json"), { keyringVersion: "1.0.0", status: "unassigned", requiredRoles: { pilot: ["product", "research", "security", "privacy", "qa"], final: ["product", "engineering", "security", "operations", "release"] }, keys: [] });
await copyFile(resolve(root, "contracts/release/stage6-evidence-layout.json"), join(repository, "contracts/release/stage6-evidence-layout.json"));
await writeJSON(join(repository, "config/legal-governance.json"), { policyVersion: "1.0.0", status: "active", projectName: "Lites", repository: "https://github.com/erweixin/langshift.dev", rightsHolderLegalName: "Fixture Rights Holder Ltd.", rightsHolderAddress: "1 Fixture Road", governingLaw: "Fixture jurisdiction", claSubmissionAddress: "cla@fixture.invalid", claAcceptanceMethod: "signed_document", securityDisclosureChannel: "https://github.com/erweixin/langshift.dev/security/advisories/new", communityLicense: "AGPL-3.0-only", commercialLicensingEnabled: true });
await writeJSON(join(repository, "product-evals/bilingual-transition-evals.json"), { samples: [] });
await writeJSON(join(repository, "contracts/catalog/profile-contracts.json"), { profiles: ["route_planner", "daily_planner", "coach", "evaluator", "artifact_builder"].map((id) => ({ id, maxCostUsd: 1, p95LatencyMs: 1000 })) });
for (const profile of ["route_planner", "daily_planner", "coach", "evaluator", "artifact_builder"]) await writeJSON(join(repository, `profile-evals/${profile}/bilingual-slice.json`), { samples: [] });
const git = (...args) => execFileSync("git", args, { cwd: repository, encoding: "utf8" });
git("init", "-q");
git("config", "user.name", "Stage6 Readiness Self-Test");
git("config", "user.email", "stage6-readiness@invalid.example");
git("add", ".");
git("commit", "-qm", "readiness fixture");
const commit = git("rev-parse", "HEAD").trim();

const behaviorBase = { manifestVersion: "1.0.0", sourceCommit: commit, createdAt: "2026-01-01T00:00:00.000Z", components: [{ kind: "policy", id: "fixture", hash: "1".repeat(64) }], impactRules: {} };
const behavior = { ...behaviorBase, manifestHash: sha256(JSON.stringify(behaviorBase)) };
const behaviorRaw = `${JSON.stringify(behavior, null, 2)}\n`;
const installed = join(repository, "release-candidates/current");
await mkdir(installed, { recursive: true });
await writeFile(join(installed, "product-behavior-manifest.json"), behaviorRaw);
const artifactPaths = ["README.md", "deploy/private-delivery/manifest.json", "behavior-manifests/schema.json", "behavior-manifests/impact-matrix.json"].sort();
const artifacts = await Promise.all(artifactPaths.map(async (path) => ({ path, sha256: sha256(await readFile(join(repository, path))) })));
const rcBase = {
  schemaVersion: "1.0.0", releaseKind: "ga_release_candidate", sourceCommit: commit, createdAt: "2026-01-01T00:00:00.000Z", immutable: true,
  images: ["reference-tool-runtime", "web-app"].map((service, index) => ({ service, image: `ghcr.io/example/${service}`, digest: `sha256:${String(index + 1).repeat(64)}`, workflowRef: "fixture", sbomAttested: true, provenanceMode: "max", signatureVerified: true })),
  behaviorManifest: { path: "product-behavior-manifest.json", sha256: sha256(behaviorRaw), manifestHash: behavior.manifestHash }, artifacts,
};
const rc = { ...rcBase, releaseCandidateHash: sha256(JSON.stringify(rcBase)) };
await writeJSON(join(installed, "ga-release-candidate.json"), rc);
await writeJSON(join(installed, "ga-release-candidate.sigstore.json"), {});

const stage1Base = { reportVersion: "1.0.0", stage: 1, status: "passed", sourceCommit: commit, worktreeDirty: false };
const stage1Path = join(repository, "release-evidence/current/stage-1-gate-report.json");
await writeJSON(stage1Path, hashed(stage1Base));
await writeJSON(join(repository, "release-evidence/current/stage-1-gate-report.sigstore.json"), {});
const penetrationBase = { reportVersion: "1.0.0", kind: "external-penetration-test", status: "passed", unresolvedCritical: 0, unresolvedHigh: 0, externalProvider: "fixture", rcHash: rc.releaseCandidateHash };
await writeJSON(join(repository, "release-evidence/current/external-penetration-test.json"), hashed(penetrationBase));
await writeJSON(join(repository, "release-evidence/current/external-penetration-test.sigstore.json"), {});
const fakeCosign = join(fakeBin, "cosign");
await writeFile(fakeCosign, "#!/bin/sh\nexit 0\n");
await chmod(fakeCosign, 0o755);
const environment = { ...process.env, PATH: `${fakeBin}:${process.env.PATH}` };
const run = () => {
  try {
    execFileSync(process.execPath, [join(repository, "scripts/write-stage6-ga-readiness-report.mjs")], { cwd: repository, env: environment, stdio: "pipe" });
  } catch (error) {
    assert.equal(error.status, 1, "fixture remains incomplete by design");
  }
  return JSON.parse(readFileSync(join(repository, "release-evidence/current/ga-readiness.json"), "utf8"));
};
const result = (report, id) => report.results.find((item) => item.id === id);
let report = run();
assert.equal(result(report, "SIGNED-UNIQUE-RC").status, "passed");
assert.equal(result(report, "STAGE-1-RC-GATE").status, "passed");
assert.equal(result(report, "EXTERNAL-PENETRATION").status, "pending", "empty external authority allowlist must fail closed");

await writeJSON(stage1Path, { ...hashed(stage1Base), tampered: true });
report = run();
assert.equal(result(report, "STAGE-1-RC-GATE").status, "pending", "tampered reportHash must be rejected");
await writeJSON(stage1Path, hashed(stage1Base));
await writeFile(fakeCosign, "#!/bin/sh\nexit 1\n");
report = run();
assert.equal(result(report, "SIGNED-UNIQUE-RC").status, "pending");
assert.equal(result(report, "STAGE-1-RC-GATE").status, "pending", "failed signature verification must be rejected");

await rm(temporary, { recursive: true, force: true });
console.log("Stage 6 readiness verifier self-test passed: authorized=accepted tampered=rejected unsigned=rejected empty-authority=rejected");
