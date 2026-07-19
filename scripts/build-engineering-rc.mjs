import { randomUUID } from "node:crypto";
import { execFileSync, spawnSync } from "node:child_process";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import {
  buildEngineeringRCManifest,
  buildSourceDependencySBOM,
  sha256,
  validateEngineeringRCInputs,
  verifyEngineeringRCManifest,
} from "./engineering-rc-contract.mjs";

const root = resolve(import.meta.dirname, "..");
const option = (name, fallback) => { const index = process.argv.indexOf(name); return index >= 0 ? process.argv[index + 1] : fallback; };
const verificationPath = option("--verification", ".tmp/verification/macos-engineering-rc.json");
const outputDirectory = option("--output", "release-candidates/current");
const outputRoot = resolve(root, outputDirectory);
const git = (...args) => execFileSync("git", args, { cwd: root, encoding: "utf8" }).trim();
const sourceCommit = git("rev-parse", "HEAD");
if (git("status", "--porcelain=v1", "--untracked-files=all")) throw new Error("Engineering RC requires a clean worktree");

const verificationRaw = await readFile(resolve(root, verificationPath), "utf8");
const verification = JSON.parse(verificationRaw);
if (verification.result !== "engineering_rc_passed" || verification.sourceCommit !== sourceCommit || verification.worktreeDirty !== false || verification.commercialGA !== false) throw new Error("verification is not a clean, current Engineering RC pass");
validateEngineeringRCInputs({ verification, sourceCommit });

await mkdir(resolve(outputRoot, "sbom"), { recursive: true });
const behaviorPath = resolve(outputRoot, "product-behavior-manifest.json");
const behaviorBuild = spawnSync(process.execPath, ["scripts/build-product-behavior-manifest.mjs", "--source-commit", sourceCommit, "--output", behaviorPath], { cwd: root, stdio: "inherit" });
if (behaviorBuild.status !== 0) throw new Error("behavior manifest generation failed");
const tracePath = resolve(outputRoot, "interface-coverage.json");
const traceBuild = spawnSync(process.execPath, ["scripts/verify-stage0-baseline.mjs", "--require-engineering-rc", "--write-report", tracePath], { cwd: root, stdio: "inherit" });
if (traceBuild.status !== 0) throw new Error("interface coverage generation failed");

const modulesRaw = execFileSync("go", ["list", "-m", "-json", "all"], { cwd: root, encoding: "utf8" });
const modules = parseJSONStream(modulesRaw).filter((item) => !item.Main).map((item) => ({ type: "library", name: item.Path, version: item.Version ?? item.Replace?.Version ?? "unknown", purl: item.Version ? `pkg:golang/${item.Path}@${item.Version}` : undefined }));
const lock = JSON.parse(await readFile(resolve(root, "package-lock.json"), "utf8"));
const npmComponents = Object.entries(lock.packages ?? {}).filter(([path, value]) => path.includes("node_modules/") && value?.version).map(([path, value]) => { const name = path.slice(path.lastIndexOf("node_modules/") + 13); return { type: "library", name, version: value.version, purl: `pkg:npm/${encodeURIComponent(name)}@${value.version}` }; });
const components = [...modules, ...npmComponents].sort((left, right) => `${left.purl}`.localeCompare(`${right.purl}`));
const generatedAt = new Date().toISOString();
const sbom = buildSourceDependencySBOM({ sourceCommit, generatedAt, serialNumber: `urn:uuid:${randomUUID()}`, components });
const sbomPath = resolve(outputRoot, "sbom/source-dependencies.cdx.json");
await writeFile(sbomPath, `${JSON.stringify(sbom, null, 2)}\n`);

const migrationRaw = await readFile(resolve(root, "deploy/migrations/manifest.json"));
const currentOpenAPIRaw = await readFile(resolve(root, "contracts/openapi/lites.current.openapi.json"));
const knownLimitationsRaw = await readFile(resolve(root, "docs/releases/engineering-rc-known-limitations.md"));
const releaseNotesRaw = await readFile(resolve(root, "docs/releases/engineering-rc-release-notes.md"));
const behaviorRaw = await readFile(behaviorPath);
const traceRaw = await readFile(tracePath);
const artifacts = [
  [verificationPath, Buffer.from(verificationRaw)],
  [`${outputDirectory}/product-behavior-manifest.json`, behaviorRaw],
  [`${outputDirectory}/interface-coverage.json`, traceRaw],
  [`${outputDirectory}/sbom/source-dependencies.cdx.json`, await readFile(sbomPath)],
  ["deploy/migrations/manifest.json", migrationRaw],
  ["contracts/openapi/lites.current.openapi.json", currentOpenAPIRaw],
  ["docs/releases/engineering-rc-known-limitations.md", knownLimitationsRaw],
  ["docs/releases/engineering-rc-release-notes.md", releaseNotesRaw],
].map(([path, body]) => ({ path, sha256: sha256(body), sizeBytes: body.length }));
const manifest = buildEngineeringRCManifest({ sourceCommit, generatedAt, dependencyComponents: components.length, artifacts });
verifyEngineeringRCManifest(manifest);
await writeFile(resolve(outputRoot, "engineering-rc.json"), `${JSON.stringify(manifest, null, 2)}\n`);
console.log(`Engineering RC: commit=${sourceCommit} dependencies=${components.length} hash=${manifest.engineeringReleaseCandidateHash}`);

function parseJSONStream(source) {
  const values = [];
  let depth = 0, start = -1, string = false, escaped = false;
  for (let index = 0; index < source.length; index += 1) {
    const character = source[index];
    if (string) { if (escaped) escaped = false; else if (character === "\\") escaped = true; else if (character === '"') string = false; continue; }
    if (character === '"') { string = true; continue; }
    if (character === "{") { if (depth === 0) start = index; depth += 1; }
    if (character === "}") { depth -= 1; if (depth === 0 && start >= 0) { values.push(JSON.parse(source.slice(start, index + 1))); start = -1; } }
  }
  return values;
}
