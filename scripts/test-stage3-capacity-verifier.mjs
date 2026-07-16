#!/usr/bin/env node
import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { readFile } from "node:fs/promises";
import { validateCapacityResult } from "./write-stage3-capacity-report.mjs";

const profile = JSON.parse(await readFile("load-tests/stage3/reference-capacity-profile.json"));
const canonical = JSON.parse(await readFile("load-tests/stage3/reference-capacity-observer.example.json"));
const canonicalJSON = (value) => Array.isArray(value) ? `[${value.map(canonicalJSON).join(",")}]` : value && typeof value === "object" ? `{${Object.keys(value).sort().map((key) => `${JSON.stringify(key)}:${canonicalJSON(value[key])}`).join(",")}}` : JSON.stringify(value);
const queryContractSha256 = createHash("sha256").update(canonicalJSON(canonical.queries)).digest("hex");
const digest = "a".repeat(64);
const sourceCommit = "b".repeat(40);
const measurement = (target, value = target) => ({ target, achieved: value, value, threshold: target, observations: 1800, prometheusQuery: "sum(rate(lites_metric[5m]))", rawSeriesSha256: digest });
const result = {
  resultVersion: "1.0.0", profileId: profile.profileId, sourceCommit,
  startedAt: "2026-07-15T00:00:00.000Z", finishedAt: "2026-07-15T00:30:00.000Z", durationSeconds: 1800,
  environment: {
    topology: "reference_production", deploymentEnvironment: profile.profileId, highAvailability: true, localFoundation: false, regions: ["us-east-1"], availabilityZones: ["us-east-1a", "us-east-1b", "us-east-1c"],
    deploymentManifestSha256: digest, imageDigestsSha256: digest, hardwareTopologySha256: digest, providerEmulatorSha256: digest,
    runtimeImageDigest: `sha256:${digest}`, runtimeKernelDigest: `sha256:${digest}`, runtimeRootFSDigest: `sha256:${digest}`, agentReleaseBundleSha256: digest, behaviorDeploymentSha256: digest, datasetManifestSha256: digest, queryContractSha256, prometheusSnapshotSha256: digest,
  },
  vectors: Object.fromEntries(Object.entries(profile.vectors).map(([key, value]) => [key, measurement(value)])),
  slos: Object.fromEntries(Object.entries(profile.slos).map(([key, threshold]) => [key, { ...measurement(threshold), value: key.endsWith("Minimum") ? threshold : key.endsWith("MaximumExclusive") ? threshold / 2 : threshold }])),
  zeroTolerance: Object.fromEntries(profile.zeroTolerance.map((key) => [key, { value: 0, observations: 1800, prometheusQuery: "sum(lites_violation_total)", rawSeriesSha256: digest }])),
};
assert.deepEqual(validateCapacityResult(profile, result, sourceCommit, queryContractSha256), []);

const mutations = [
  ["duration", (x) => { x.durationSeconds = 1799; }],
  ["topology", (x) => { x.environment.localFoundation = true; }],
  ["deployment environment", (x) => { x.environment.deploymentEnvironment = "other"; }],
  ["vector", (x) => { x.vectors.concurrentConnections.achieved = 9999; }],
  ["observation coverage", (x) => { x.vectors.concurrentConnections.observations = profile.minimumObservationsPerMeasurement - 1; }],
  ["minimum SLO", (x) => { x.slos.apiAvailabilityMinimum.value = 0.9; }],
  ["maximum SLO", (x) => { x.slos.apiLatencyP95MillisMaximum.value = 251; }],
  ["exclusive SLO", (x) => { x.slos.toolOutcomeUnknownRateMaximumExclusive.value = 0.001; }],
  ["zero tolerance", (x) => { x.zeroTolerance.lost_event_facts.value = 1; }],
  ["evidence", (x) => { x.environment.prometheusSnapshotSha256 = "missing"; }],
  ["release bundle evidence", (x) => { delete x.environment.agentReleaseBundleSha256; }],
  ["behavior deployment evidence", (x) => { delete x.environment.behaviorDeploymentSha256; }],
  ["Runtime kernel evidence", (x) => { delete x.environment.runtimeKernelDigest; }],
  ["Runtime rootfs evidence", (x) => { delete x.environment.runtimeRootFSDigest; }],
];
for (const [name, mutate] of mutations) {
  const changed = structuredClone(result);
  mutate(changed);
  assert.notDeepEqual(validateCapacityResult(profile, changed, sourceCommit, queryContractSha256), [], `${name} mutation passed`);
}
console.log(`stage-3 capacity verifier self-test: mutations=${mutations.length} status=passed`);
