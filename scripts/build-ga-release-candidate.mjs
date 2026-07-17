import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import { mkdir, readFile, readdir, writeFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const args = new Map();
for (let index = 2; index < process.argv.length; index += 2) args.set(process.argv[index], process.argv[index + 1]);
const sourceCommit = args.get("--source-commit");
const releasesDirectory = args.get("--releases");
const behaviorPath = args.get("--behavior-manifest");
const output = args.get("--output");
if (!/^[0-9a-f]{40}$/.test(sourceCommit ?? "") || !releasesDirectory || !behaviorPath || !output) throw new Error("source commit, releases directory, behavior manifest, and output are required");
const sha256 = (value) => createHash("sha256").update(value).digest("hex");
const git = (...values) => execFileSync("git", values, { cwd: root });
git("cat-file", "-e", `${sourceCommit}^{commit}`);
const privateDelivery = JSON.parse(git("show", `${sourceCommit}:deploy/private-delivery/manifest.json`).toString("utf8"));
const requiredServices = [...privateDelivery.releaseImages, "reference-tool-runtime"].sort();
const releaseRoot = resolve(root, releasesDirectory);
const coordinates = [];
for (const file of await readdir(releaseRoot)) {
  if (!file.endsWith(".json")) continue;
  const item = JSON.parse(await readFile(resolve(releaseRoot, file), "utf8"));
  if (requiredServices.includes(item.service)) coordinates.push(item);
}
coordinates.sort((left, right) => left.service.localeCompare(right.service));
for (const service of requiredServices) {
  const item = coordinates.find((candidate) => candidate.service === service);
  if (!item || item.source_commit !== sourceCommit || !/^sha256:[0-9a-f]{64}$/.test(item.digest) || item.sbom_attested !== true || item.provenance_mode !== "max" || item.cosign_keyless_signature_verified !== true) throw new Error(`missing or invalid signed image evidence for ${service}`);
}
if (coordinates.length !== requiredServices.length) throw new Error("duplicate signed image evidence");
const behaviorRaw = await readFile(resolve(root, behaviorPath), "utf8");
const behavior = JSON.parse(behaviorRaw);
if (behavior.sourceCommit !== sourceCommit || !/^[0-9a-f]{64}$/.test(behavior.manifestHash)) throw new Error("behavior manifest is not bound to the release commit");
const artifactPaths = [...privateDelivery.requiredSourceArtifacts, "deploy/private-delivery/manifest.json", "behavior-manifests/schema.json", "behavior-manifests/impact-matrix.json"].sort();
const artifacts = artifactPaths.map((path) => ({ path, sha256: sha256(git("show", `${sourceCommit}:${path}`)) }));
const createdAt = new Date(git("show", "-s", "--format=%cI", sourceCommit).toString("utf8").trim()).toISOString();
const base = { schemaVersion: "1.0.0", releaseKind: "ga_release_candidate", sourceCommit, createdAt, immutable: true, images: coordinates.map(({ service, image, digest, workflow_ref, sbom_attested, provenance_mode, cosign_keyless_signature_verified }) => ({ service, image, digest, workflowRef: workflow_ref, sbomAttested: sbom_attested, provenanceMode: provenance_mode, signatureVerified: cosign_keyless_signature_verified })), behaviorManifest: { path: behaviorPath, sha256: sha256(behaviorRaw), manifestHash: behavior.manifestHash }, artifacts };
const manifest = { ...base, releaseCandidateHash: sha256(JSON.stringify(base)) };
const target = resolve(root, output);
await mkdir(dirname(target), { recursive: true });
await writeFile(target, `${JSON.stringify(manifest, null, 2)}\n`);
console.log(`GA RC: images=${coordinates.length} commit=${sourceCommit} hash=${manifest.releaseCandidateHash}`);
