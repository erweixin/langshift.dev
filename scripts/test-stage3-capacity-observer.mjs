#!/usr/bin/env node
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { reducePrometheusRange, validateObserverConfig } from "./run-stage3-capacity-observer.mjs";

const profile = JSON.parse(await readFile("load-tests/stage3/reference-capacity-profile.json"));
const digest = "a".repeat(64);
const client = { baseUrl: "https://capacity.example.invalid", bearerTokenFile: "/run/secrets/token", caFile: "/run/secrets/ca", clientCertificateFile: "/run/secrets/cert", clientKeyFile: "/run/secrets/key" };
const queries = Object.fromEntries(Object.keys(profile.vectors).map((name) => [name, `vector_${name}{capacity_profile=\"{{profile_id}}\"}`]));
const sloQueries = Object.fromEntries(Object.keys(profile.slos).map((name) => [name, `slo_${name}{capacity_profile=\"{{profile_id}}\"}`]));
const zeroQueries = Object.fromEntries(profile.zeroTolerance.map((name) => [name, `zero_${name}{capacity_profile=\"{{profile_id}}\"}`]));
const config = {
  configVersion: "1.0.0", profileId: profile.profileId, sourceCommit: "b".repeat(40), sampleIntervalSeconds: 15, maximumWarmupSeconds: 900,
  controller: client, prometheus: { ...client, baseUrl: "https://prometheus.example.invalid" },
  environment: { topology: "reference_production", deploymentEnvironment: profile.profileId, highAvailability: true, localFoundation: false, regions: ["us-test-1"], availabilityZones: ["us-test-1a", "us-test-1b"], ...Object.fromEntries(["deploymentManifestSha256", "imageDigestsSha256", "hardwareTopologySha256", "providerEmulatorSha256", "runtimeImageDigest", "runtimeKernelDigest", "runtimeRootFSDigest", "agentReleaseBundleSha256", "behaviorDeploymentSha256", "datasetManifestSha256"].map((key) => [key, digest])) },
  queries: { vectors: queries, slos: sloQueries, zeroTolerance: zeroQueries },
};
const canonicalQueries = structuredClone(config.queries);
assert.deepEqual(validateObserverConfig(config, profile, canonicalQueries), []);
for (const mutate of [
  (value) => { value.environment.localFoundation = true; },
  (value) => { delete value.queries.vectors.concurrentConnections; },
  (value) => { value.prometheus.baseUrl = "http://127.0.0.1:9090"; },
  (value) => { value.environment.availabilityZones = ["us-test-1a"]; },
  (value) => { value.sampleIntervalSeconds = 1; },
	(value) => { value.environment.deploymentEnvironment = "other"; },
	(value) => { value.queries.vectors.concurrentConnections = `vector_forged{deployment_environment_name="{{profile_id}}"}`; },
	(value) => { value.maximumWarmupSeconds = 901; },
	(value) => { delete value.environment.agentReleaseBundleSha256; },
	(value) => { delete value.environment.behaviorDeploymentSha256; },
	(value) => { delete value.environment.runtimeKernelDigest; },
	(value) => { delete value.environment.runtimeRootFSDigest; },
]) {
  const changed = structuredClone(config);
  mutate(changed);
  assert.notDeepEqual(validateObserverConfig(changed, profile, canonicalQueries), []);
}
const raw = JSON.stringify({ status: "success", data: { resultType: "matrix", result: [{ metric: {}, values: [[1, "3"], [2, "7"], [3, "5"]] }] } });
assert.equal(reducePrometheusRange(raw, "minimum").value, 3);
assert.equal(reducePrometheusRange(raw, "maximum").value, 7);
assert.equal(reducePrometheusRange(raw, "minimum").observations, 3);
assert.throws(() => reducePrometheusRange(JSON.stringify({ status: "success", data: { resultType: "matrix", result: [] } }), "minimum"));
console.log("stage-3 capacity observer self-test: config_mutations=12 reductions=4 status=passed");
