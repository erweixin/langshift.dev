import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const sha256 = (value) => createHash("sha256").update(value).digest("hex");
const hex40 = /^[0-9a-f]{40}$/;
const hex64 = /^[0-9a-f]{64}$/;
const without = (value, key) => { const copy = { ...value }; delete copy[key]; return copy; };

function validateManifest(manifest) {
  if (manifest?.manifestVersion !== "1.0.0" || !hex40.test(manifest.sourceCommit ?? "") || !Number.isFinite(Date.parse(manifest.createdAt)) || !Array.isArray(manifest.components) || manifest.components.length < 1 || !manifest.components.every((component) => typeof component.kind === "string" && typeof component.id === "string" && hex64.test(component.hash ?? "") && typeof component.pilotScope === "boolean" && Array.isArray(component.inputs) && component.inputs.length > 0 && new Set(component.inputs).size === component.inputs.length) || new Set(manifest.components.map((component) => `${component.kind}:${component.id}`)).size !== manifest.components.length || !hex64.test(manifest.manifestHash ?? "") || sha256(JSON.stringify(without(manifest, "manifestHash"))) !== manifest.manifestHash || manifest.impactRules?.manifestVersion !== "1.0.0" || !Array.isArray(manifest.impactRules.rules) || typeof manifest.impactRules.exclusionRule !== "string") throw new Error("product behavior manifest is invalid");
}

export function buildPilotEquivalenceReport({ pilotManifest, rcManifest, pilotReport, releaseCandidate }, { pilotManifestPath, rcManifestPath, pilotReportPath, rcPath, inputHashes, generatedAt, worktreeDirty = false }) {
  validateManifest(pilotManifest);
  validateManifest(rcManifest);
  if (pilotReport?.reportVersion !== "1.0.0" || pilotReport.kind !== "design-partner-pilot" || pilotReport.status !== "approved" || pilotReport.behaviorManifestHash !== pilotManifest.manifestHash || pilotReport.sourceCommit !== pilotManifest.sourceCommit || !hex64.test(pilotReport.reportHash ?? "") || sha256(JSON.stringify(without(pilotReport, "reportHash"))) !== pilotReport.reportHash) throw new Error("approved pilot report is not self-hashed or bound to the pilot behavior manifest");
  if (releaseCandidate?.schemaVersion !== "1.0.0" || releaseCandidate.releaseKind !== "ga_release_candidate" || releaseCandidate.immutable !== true || releaseCandidate.sourceCommit !== rcManifest.sourceCommit || releaseCandidate.behaviorManifest?.manifestHash !== rcManifest.manifestHash || !hex64.test(releaseCandidate.releaseCandidateHash ?? "") || sha256(JSON.stringify(without(releaseCandidate, "releaseCandidateHash"))) !== releaseCandidate.releaseCandidateHash) throw new Error("release candidate is not self-hashed or bound to the RC behavior manifest");
  if (![pilotManifestPath, rcManifestPath, pilotReportPath, rcPath].every((path) => typeof path === "string" && path.length > 0) || !Object.values(inputHashes ?? {}).every((value) => hex64.test(value ?? "")) || Object.keys(inputHashes ?? {}).length !== 4 || !Number.isFinite(Date.parse(generatedAt))) throw new Error("equivalence evidence metadata is invalid");
  const pilotComponents = new Map(pilotManifest.components.filter((component) => component.pilotScope).map((component) => [`${component.kind}:${component.id}`, component]));
  const rcComponents = new Map(rcManifest.components.filter((component) => component.pilotScope).map((component) => [`${component.kind}:${component.id}`, component]));
  const ids = [...new Set([...pilotComponents.keys(), ...rcComponents.keys()])].sort();
  const differences = [];
  for (const id of ids) {
    const pilot = pilotComponents.get(id);
    const rc = rcComponents.get(id);
    if (!pilot) differences.push({ component: id, reason: "added_to_rc_pilot_scope" });
    else if (!rc) differences.push({ component: id, reason: "removed_from_rc_pilot_scope" });
    else if (pilot.hash !== rc.hash) differences.push({ component: id, reason: "content_hash_changed", pilotHash: pilot.hash, rcHash: rc.hash });
    else if (JSON.stringify([...pilot.inputs].sort()) !== JSON.stringify([...rc.inputs].sort())) differences.push({ component: id, reason: "input_set_changed" });
  }
  if (JSON.stringify(pilotManifest.impactRules) !== JSON.stringify(rcManifest.impactRules)) differences.push({ component: "impactRules", reason: "impact_rules_changed" });
  const passed = !worktreeDirty && differences.length === 0 && pilotComponents.size === pilotManifest.components.length && rcComponents.size === rcManifest.components.length;
  const base = { reportVersion: "1.0.0", stage: 6, kind: "pilot-rc-behavior-equivalence", generatedAt: new Date(generatedAt).toISOString(), status: passed ? "passed" : "failed", sourceCommit: rcManifest.sourceCommit, worktreeDirty, rcHash: releaseCandidate.releaseCandidateHash, pilot: { reportPath: pilotReportPath, reportHash: pilotReport.reportHash, completedAt: pilotReport.completedAt, sourceCommit: pilotManifest.sourceCommit, manifestPath: pilotManifestPath, manifestHash: pilotManifest.manifestHash }, releaseCandidate: { path: rcPath, sourceCommit: rcManifest.sourceCommit, manifestPath: rcManifestPath, manifestHash: rcManifest.manifestHash }, inputs: { pilotReportSha256: inputHashes.pilotReport, pilotManifestSha256: inputHashes.pilotManifest, rcManifestSha256: inputHashes.rcManifest, releaseCandidateSha256: inputHashes.releaseCandidate }, comparison: { mode: "exact_pilot_scope", pilotComponents: pilotComponents.size, rcComponents: rcComponents.size, unchangedComponents: ids.length - differences.filter((difference) => difference.component !== "impactRules").length, impactRulesEqual: JSON.stringify(pilotManifest.impactRules) === JSON.stringify(rcManifest.impactRules), differences } };
  return { ...base, reportHash: sha256(JSON.stringify(base)) };
}

async function main() {
  const root = resolve(import.meta.dirname, "..");
  const args = new Map();
  for (let index = 2; index < process.argv.length; index += 2) args.set(process.argv[index], process.argv[index + 1]);
  const paths = { pilotManifest: args.get("--pilot-manifest"), rcManifest: args.get("--rc-manifest"), pilotReport: args.get("--pilot-report"), releaseCandidate: args.get("--release-candidate") };
  const output = args.get("--output");
  if (!output || Object.values(paths).some((value) => !value)) throw new Error("--pilot-manifest, --rc-manifest, --pilot-report, --release-candidate and --output are required");
  const entries = await Promise.all(Object.entries(paths).map(async ([key, path]) => { const raw = await readFile(resolve(root, path)); return [key, { raw, value: JSON.parse(raw) }]; }));
  const inputs = Object.fromEntries(entries);
  const currentCommit = execFileSync("git", ["rev-parse", "HEAD"], { cwd: root, encoding: "utf8" }).trim();
  if (inputs.rcManifest.value.sourceCommit !== currentCommit) throw new Error("RC behavior manifest is not bound to the current commit");
  const worktreeDirty = execFileSync("git", ["status", "--porcelain=v1"], { cwd: root, encoding: "utf8" }).trim().length > 0;
  const report = buildPilotEquivalenceReport({ pilotManifest: inputs.pilotManifest.value, rcManifest: inputs.rcManifest.value, pilotReport: inputs.pilotReport.value, releaseCandidate: inputs.releaseCandidate.value }, { pilotManifestPath: paths.pilotManifest, rcManifestPath: paths.rcManifest, pilotReportPath: paths.pilotReport, rcPath: paths.releaseCandidate, inputHashes: Object.fromEntries(Object.entries(inputs).map(([key, input]) => [key, sha256(input.raw)])), generatedAt: new Date().toISOString(), worktreeDirty });
  const target = resolve(root, output);
  await mkdir(dirname(target), { recursive: true });
  await writeFile(target, `${JSON.stringify(report, null, 2)}\n`);
  console.log(`${report.status}: pilot=${report.comparison.pilotComponents} rc=${report.comparison.rcComponents} differences=${report.comparison.differences.length} report=${report.reportHash}`);
  if (report.status !== "passed") process.exitCode = 1;
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) await main();
