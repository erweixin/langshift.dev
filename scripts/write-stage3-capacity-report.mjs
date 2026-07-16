#!/usr/bin/env node
import { createHash } from "node:crypto";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import path from "node:path";
import { pathToFileURL } from "node:url";

export const sha256 = (value) => createHash("sha256").update(value).digest("hex");
const requiredEvidenceKeys = ["deploymentManifestSha256", "imageDigestsSha256", "hardwareTopologySha256", "providerEmulatorSha256", "runtimeImageDigest", "runtimeKernelDigest", "runtimeRootFSDigest", "agentReleaseBundleSha256", "behaviorDeploymentSha256", "datasetManifestSha256", "queryContractSha256", "prometheusSnapshotSha256"];

function canonicalJSON(value) {
  if (Array.isArray(value)) return `[${value.map(canonicalJSON).join(",")}]`;
  if (value && typeof value === "object") return `{${Object.keys(value).sort().map((key) => `${JSON.stringify(key)}:${canonicalJSON(value[key])}`).join(",")}}`;
  return JSON.stringify(value);
}

export function validateCapacityResult(profile, result, sourceCommit, expectedQueryContractSha256) {
  const failures = [];
  if (result.resultVersion !== "1.0.0" || result.profileId !== profile.profileId) failures.push("profile_identity");
  if (result.sourceCommit !== sourceCommit) failures.push("source_commit");
  if (result.environment?.topology !== "reference_production" || result.environment?.deploymentEnvironment !== profile.profileId || result.environment?.highAvailability !== true || result.environment?.localFoundation === true) failures.push("production_topology");
  const started = Date.parse(result.startedAt ?? "");
  const finished = Date.parse(result.finishedAt ?? "");
  if (!Number.isFinite(started) || !Number.isFinite(finished) || finished <= started || (finished - started) / 1000 < profile.durationSeconds || result.durationSeconds < profile.durationSeconds) failures.push("duration");
  for (const [name, minimum] of Object.entries(profile.vectors)) {
    const measurement = result.vectors?.[name];
    if (!measurement || !Number.isFinite(measurement.achieved) || measurement.achieved < minimum || measurement.target !== minimum || measurement.observations < profile.minimumObservationsPerMeasurement || !measurement.prometheusQuery || !/^[0-9a-f]{64}$/.test(measurement.rawSeriesSha256 ?? "")) failures.push(`vector:${name}`);
  }
  for (const [name, threshold] of Object.entries(profile.slos)) {
    const measurement = result.slos?.[name];
    let passes = measurement && Number.isFinite(measurement.value) && measurement.observations >= profile.minimumObservationsPerMeasurement && measurement.prometheusQuery && /^[0-9a-f]{64}$/.test(measurement.rawSeriesSha256 ?? "");
    if (passes) passes = name.endsWith("Minimum") ? measurement.value >= threshold : name.endsWith("MaximumExclusive") ? measurement.value < threshold : measurement.value <= threshold;
    if (!passes || measurement.threshold !== threshold) failures.push(`slo:${name}`);
  }
  for (const name of profile.zeroTolerance) {
    const measurement = result.zeroTolerance?.[name];
    if (!measurement || measurement.value !== 0 || measurement.observations < profile.minimumObservationsPerMeasurement || !measurement.prometheusQuery || !/^[0-9a-f]{64}$/.test(measurement.rawSeriesSha256 ?? "")) failures.push(`zero_tolerance:${name}`);
  }
  for (const key of requiredEvidenceKeys) if (!/^(sha256:)?[0-9a-f]{64}$/.test(result.environment?.[key] ?? "")) failures.push(`evidence:${key}`);
	if (result.environment?.queryContractSha256 !== expectedQueryContractSha256) failures.push("evidence:queryContractSha256");
  if (!Array.isArray(result.environment?.regions) || result.environment.regions.length !== 1 || !Array.isArray(result.environment?.availabilityZones) || result.environment.availabilityZones.length < 2) failures.push("regional_topology");
  return [...new Set(failures)].sort();
}

async function main() {
  const args = new Map();
  for (let index = 2; index < process.argv.length; index += 2) args.set(process.argv[index], process.argv[index + 1]);
  const input = args.get("--input");
  const sourceCommit = args.get("--source-commit");
  if (!input || !/^[0-9a-f]{40}$/.test(sourceCommit ?? "")) throw new Error("usage: write-stage3-capacity-report.mjs --input <raw-result.json> --source-commit <40-hex>");
  const [profileRaw, raw, canonicalRaw] = await Promise.all([readFile("load-tests/stage3/reference-capacity-profile.json"), readFile(input), readFile("load-tests/stage3/reference-capacity-observer.example.json")]);
  const profile = JSON.parse(profileRaw);
  const result = JSON.parse(raw);
  const expectedQueryContractSha256 = sha256(canonicalJSON(JSON.parse(canonicalRaw).queries));
  const failures = validateCapacityResult(profile, result, sourceCommit, expectedQueryContractSha256);
  if (failures.length) throw new Error(`Stage 3 capacity gate failed: ${failures.join(", ")}`);
  const reportBase = {
    reportVersion: "1.0.0", stage: 3, kind: "baseline-capacity", generatedAt: result.finishedAt,
    sourceCommit, status: "passed",
    evidence: { rawResultSha256: sha256(raw), profileSha256: sha256(profileRaw), ...result.environment },
    profile: { profileId: profile.profileId, profileVersion: profile.profileVersion, durationSeconds: profile.durationSeconds, sampleIntervalSeconds: profile.sampleIntervalSeconds, minimumObservationsPerMeasurement: profile.minimumObservationsPerMeasurement },
    durationSeconds: result.durationSeconds, vectors: result.vectors, slos: result.slos,
    zeroTolerance: result.zeroTolerance, zeroToleranceFailures: [],
  };
  const report = { ...reportBase, reportHash: sha256(JSON.stringify(reportBase)) };
  const output = path.resolve("gate-reports/stage-3/baseline-capacity-report.json");
  await mkdir(path.dirname(output), { recursive: true });
  await writeFile(output, `${JSON.stringify(report, null, 2)}\n`);
  console.log(`stage-3 capacity gate: duration=${result.durationSeconds}s status=passed report=${report.reportHash}`);
}

if (import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) await main();
