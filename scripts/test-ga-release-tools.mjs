import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdtemp, readFile, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";

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
console.log(`GA release tools self-test passed: images=${services.length} components=${behavior.components.length}`);
