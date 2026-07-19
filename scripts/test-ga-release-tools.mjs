import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import { chmod, copyFile, mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const node = (script, args) => execFileSync(process.execPath, [resolve(root, script), ...args], { cwd: root, stdio: "pipe" });
const git = (...args) => execFileSync("git", args, { cwd: root, encoding: "utf8" });
const commit = git("rev-parse", "HEAD").trim();
const temporary = await mkdtemp(join(tmpdir(), "lites-ga-release-tools-"));
const releases = join(temporary, "releases");
await import("node:fs/promises").then(({ mkdir }) => mkdir(releases));
const delivery = JSON.parse(git("show", `${commit}:deploy/private-delivery/manifest.json`));
const services = [...delivery.releaseImages, "reference-tool-runtime"];
for (const [index, service] of services.entries()) {
  const evidence = { schema_version: "1.0.0", service, image: `ghcr.io/example/lites-${service}`, digest: `sha256:${(index + 1).toString(16).padStart(64, "0")}`, source_commit: commit, workflow_ref: "example/repo/.github/workflows/supply-chain.yml@refs/heads/main", sbom_attested: true, provenance_mode: "max", cosign_keyless_signature_verified: true };
  await writeFile(join(releases, `${service}.json`), `${JSON.stringify(evidence)}\n`);
}
const behaviorPath = join(temporary, "behavior.json");
node("scripts/build-product-behavior-manifest.mjs", ["--source-commit", commit, "--output", behaviorPath]);
const behavior = JSON.parse(await readFile(behaviorPath, "utf8"));
assert.equal(behavior.components.length, 13);
assert.match(behavior.manifestHash, /^[0-9a-f]{64}$/);

const rcPath = join(temporary, "rc.json");
node("scripts/build-ga-release-candidate.mjs", ["--source-commit", commit, "--releases", releases, "--behavior-manifest", behaviorPath, "--output", rcPath]);
const rc = JSON.parse(await readFile(rcPath, "utf8"));
assert.equal(rc.images.length, services.length);
assert.equal(rc.sourceCommit, commit);
assert.match(rc.releaseCandidateHash, /^[0-9a-f]{64}$/);

const equivalentPath = join(temporary, "equivalent.json");
node("scripts/compare-product-behavior-manifests.mjs", ["--pilot", behaviorPath, "--rc", behaviorPath, "--output", equivalentPath]);
assert.equal(JSON.parse(await readFile(equivalentPath, "utf8")).status, "passed");

const changed = structuredClone(behavior);
changed.components[0].hash = "f".repeat(64);
const changedPath = join(temporary, "changed.json");
await writeFile(changedPath, `${JSON.stringify(changed)}\n`);
let rejected = false;
try {
  node("scripts/compare-product-behavior-manifests.mjs", ["--pilot", behaviorPath, "--rc", changedPath, "--output", join(temporary, "changed-report.json")]);
} catch (error) {
  rejected = error.status === 1;
}
assert.equal(rejected, true, "pilot comparison must reject a changed pilot-scoped hash");

// Exercise the customer-side installer in a disposable clean repository. The
// fake Cosign binary only isolates installer semantics; real signature
// verification is enforced separately by the pinned CI Cosign invocation.
const installerRepository = join(temporary, "installer-repository");
const installerBundle = join(temporary, "installer-bundle");
const fakeBin = join(temporary, "fake-bin");
await Promise.all([mkdir(installerRepository, { recursive: true }), mkdir(join(installerBundle, "evidence"), { recursive: true }), mkdir(fakeBin, { recursive: true })]);
const currentDelivery = JSON.parse(await readFile(resolve(root, "deploy/private-delivery/manifest.json"), "utf8"));
const expectedRCArtifacts = [...currentDelivery.requiredSourceArtifacts, "deploy/private-delivery/manifest.json", "behavior-manifests/schema.json", "behavior-manifests/impact-matrix.json"].sort();
const requiredErasureArtifacts = ["cmd/account-erasure-worker/main.go", "internal/identity/erasure/service.go", "internal/identity/postgres/account_erasure_surface.go", "deploy/migrations/000084_account_erasure_receipts.up.sql", "scripts/install-ga-release-candidate.sh", "scripts/stage6-account-erasure-gate.sh", "scripts/write-stage6-account-erasure-report.mjs", ".github/workflows/supply-chain.yml"];
for (const path of new Set([...expectedRCArtifacts, ...requiredErasureArtifacts, ".gitignore"])) {
  const target = join(installerRepository, path);
  await mkdir(dirname(target), { recursive: true });
  await copyFile(resolve(root, path), target);
}
const repositoryGit = (...args) => execFileSync("git", args, { cwd: installerRepository, encoding: "utf8" });
repositoryGit("init", "-q");
repositoryGit("config", "user.name", "GA Installer Self-Test");
repositoryGit("config", "user.email", "ga-installer-self-test@invalid.example");
repositoryGit("add", ".");
repositoryGit("commit", "-qm", "installer fixture");
const installerCommit = repositoryGit("rev-parse", "HEAD").trim();
const sha256 = (value) => createHash("sha256").update(value).digest("hex");
const sourceArtifacts = async (paths) => Promise.all(paths.map(async (path) => ({ path, sha256: sha256(await readFile(join(installerRepository, path))) })));
const behaviorBase = { manifestVersion: "1.0.0", sourceCommit: installerCommit, createdAt: "2026-01-01T00:00:00.000Z", components: [{ kind: "policy", id: "self-test", hash: "1".repeat(64), pilotScope: true }], impactRules: {} };
const installerBehavior = { ...behaviorBase, manifestHash: sha256(JSON.stringify(behaviorBase)) };
const behaviorRaw = `${JSON.stringify(installerBehavior, null, 2)}\n`;
await writeFile(join(installerBundle, "product-behavior-manifest.json"), behaviorRaw);
const installerServices = [...currentDelivery.releaseImages, "reference-tool-runtime"].sort();
const rcBase = {
  schemaVersion: "1.0.0", releaseKind: "ga_release_candidate", sourceCommit: installerCommit, createdAt: "2026-01-01T00:00:00.000Z", immutable: true,
  images: installerServices.map((service, index) => ({ service, image: `ghcr.io/example/lites-${service}`, digest: `sha256:${(index + 1).toString(16).padStart(64, "0")}`, workflowRef: "example/repo/.github/workflows/supply-chain.yml@refs/heads/main", sbomAttested: true, provenanceMode: "max", signatureVerified: true })),
  behaviorManifest: { path: "product-behavior-manifest.json", sha256: sha256(behaviorRaw), manifestHash: installerBehavior.manifestHash },
  artifacts: await sourceArtifacts(expectedRCArtifacts),
};
const installerRC = { ...rcBase, releaseCandidateHash: sha256(JSON.stringify(rcBase)) };
const databaseEvidence = `${JSON.stringify({ Action: "pass", Test: "TestAccountErasureHundredSubjectsAndRestoreEpoch", Elapsed: 1 })}\n`;
const dependencyEvidence = ["TestS3CompatiblePurgeRemovesAllVersionsAndDeleteMarkers", "TestVaultTransitSubjectKeyPurgeIsIrreversible", "TestTaggedSubjectCachePutAndPurgeAreAtomicAndConfined"].map((Test) => JSON.stringify({ Action: "pass", Test, Elapsed: 1 })).join("\n") + "\n";
await writeFile(join(installerBundle, "evidence/account-erasure-integration.json"), databaseEvidence);
await writeFile(join(installerBundle, "evidence/account-erasure-dependencies.json"), dependencyEvidence);
const erasureBase = {
  reportVersion: "1.0.0", stage: 6, kind: "account-erasure-100", status: "passed", generatedAt: "2026-01-01T00:00:00.000Z", sourceCommit: installerCommit, worktreeDirty: false, rcHash: installerRC.releaseCandidateHash,
  accounts: 100, readableSurfacesAfter: 0, completeReceipts: 100, restoreRedeletions: 100, totalSurfaceReceiptRowsAfterRestore: 1200,
  test: { evidenceSha256: sha256(databaseEvidence) },
  dependencyGate: { evidenceSha256: sha256(dependencyEvidence), runtimes: { s3: `example/s3@sha256:${"2".repeat(64)}`, vault: `example/vault@sha256:${"3".repeat(64)}`, valkey: `example/valkey@sha256:${"4".repeat(64)}` }, tests: [{}, {}, {}] },
  artifacts: await sourceArtifacts(requiredErasureArtifacts),
};
const installerErasure = { ...erasureBase, reportHash: sha256(JSON.stringify(erasureBase)) };
const issueSnapshot = { schemaVersion: "1.0.0", provider: "github-rest", repository: "erweixin/langshift.dev", sourceCommit: installerCommit, rcHash: installerRC.releaseCandidateHash, capturedAt: "2026-01-01T00:00:00.000Z", querySha256: "5".repeat(64), paginationComplete: true, pageCount: 1, totalCount: 0, issues: [] };
const issueSnapshotRaw = `${JSON.stringify(issueSnapshot, null, 2)}\n`;
await writeFile(join(installerBundle, "evidence/release-issues-snapshot.json"), issueSnapshotRaw);
const releaseIssuesBase = { reportVersion: "1.0.0", stage: 6, kind: "release-issues", generatedAt: "2026-01-01T00:00:00.000Z", status: "passed", sourceCommit: installerCommit, worktreeDirty: false, rcHash: installerRC.releaseCandidateHash, repository: "erweixin/langshift.dev", policyVersion: "1.0.0", snapshot: { path: "evidence/release-issues-snapshot.json", sha256: sha256(issueSnapshotRaw), capturedAt: issueSnapshot.capturedAt, maximumAgeHours: 24, provider: "github-rest", querySha256: issueSnapshot.querySha256, paginationComplete: true, pages: 1, totalIssues: 0 }, openP0: 0, openP1: 0, unclassifiedOpen: 0, openCounts: { P0: 0, P1: 0, P2: 0, P3: 0, unclassified: 0, total: 0 }, openIssues: [] };
const installerReleaseIssues = { ...releaseIssuesBase, reportHash: sha256(JSON.stringify(releaseIssuesBase)) };
await Promise.all([
  writeFile(join(installerBundle, "ga-release-candidate.json"), `${JSON.stringify(installerRC, null, 2)}\n`),
  writeFile(join(installerBundle, "account-erasure-100.json"), `${JSON.stringify(installerErasure, null, 2)}\n`),
  writeFile(join(installerBundle, "release-issues.json"), `${JSON.stringify(installerReleaseIssues, null, 2)}\n`),
  writeFile(join(installerBundle, "ga-release-candidate.sigstore.json"), "{}\n"),
  writeFile(join(installerBundle, "account-erasure-100.sigstore.json"), "{}\n"),
  writeFile(join(installerBundle, "release-issues.sigstore.json"), "{}\n"),
]);
const fakeCosign = join(fakeBin, "cosign");
await writeFile(fakeCosign, "#!/bin/sh\nexit 0\n");
await chmod(fakeCosign, 0o755);
const installerEnvironment = { ...process.env, PATH: `${fakeBin}:${process.env.PATH}` };
execFileSync("bash", [join(installerRepository, "scripts/install-ga-release-candidate.sh"), installerBundle, currentDelivery.releaseEvidence.cosignCertificateIdentity], { cwd: installerRepository, env: installerEnvironment, stdio: "pipe" });
assert.equal(JSON.parse(await readFile(join(installerRepository, "release-candidates/current/ga-release-candidate.json"), "utf8")).releaseCandidateHash, installerRC.releaseCandidateHash);
const invalidErasureBase = { ...erasureBase, artifacts: erasureBase.artifacts.filter((artifact) => artifact.path !== "cmd/account-erasure-worker/main.go") };
await writeFile(join(installerBundle, "account-erasure-100.json"), `${JSON.stringify({ ...invalidErasureBase, reportHash: sha256(JSON.stringify(invalidErasureBase)) }, null, 2)}\n`);
let invalidInstallerRejected = false;
try {
  execFileSync("bash", [join(installerRepository, "scripts/install-ga-release-candidate.sh"), installerBundle, currentDelivery.releaseEvidence.cosignCertificateIdentity], { cwd: installerRepository, env: installerEnvironment, stdio: "pipe" });
} catch (error) {
  invalidInstallerRejected = error.status === 1;
}
assert.equal(invalidInstallerRejected, true, "installer must reject a signed bundle that omits a critical erasure artifact");
assert.equal(JSON.parse(await readFile(join(installerRepository, "release-candidates/current/ga-release-candidate.json"), "utf8")).releaseCandidateHash, installerRC.releaseCandidateHash, "a rejected bundle must not replace the installed RC");
await rm(temporary, { recursive: true, force: true });
console.log(`GA release tools self-test passed: images=${services.length} components=${behavior.components.length} installerImages=${installerServices.length}`);
