#!/usr/bin/env node
import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import { chmod, readFile, mkdir, writeFile } from "node:fs/promises";
import { request as httpsRequest } from "node:https";
import path from "node:path";
import { pathToFileURL } from "node:url";

const sha256 = (value) => createHash("sha256").update(value).digest("hex");
const evidenceKeys = ["deploymentManifestSha256", "imageDigestsSha256", "hardwareTopologySha256", "providerEmulatorSha256", "runtimeImageDigest", "runtimeKernelDigest", "runtimeRootFSDigest", "agentReleaseBundleSha256", "behaviorDeploymentSha256", "datasetManifestSha256"];

function exactKeys(actual, expected) {
  return actual.length === expected.length && actual.every((key, index) => key === expected[index]);
}

function canonicalJSON(value) {
  if (Array.isArray(value)) return `[${value.map(canonicalJSON).join(",")}]`;
  if (value && typeof value === "object") return `{${Object.keys(value).sort().map((key) => `${JSON.stringify(key)}:${canonicalJSON(value[key])}`).join(",")}}`;
  return JSON.stringify(value);
}

export function validateObserverConfig(config, profile, canonicalQueries) {
  const failures = [];
  const check = (condition, code) => { if (!condition) failures.push(code); };
  check(config?.configVersion === "1.0.0", "config_version");
  check(config?.profileId === profile?.profileId, "profile_id");
  check(/^[0-9a-f]{40}$/.test(config?.sourceCommit ?? ""), "source_commit");
  check(config?.sampleIntervalSeconds === profile?.sampleIntervalSeconds, "sample_interval");
  check(Number.isInteger(config?.maximumWarmupSeconds) && config.maximumWarmupSeconds >= 60 && config.maximumWarmupSeconds <= 900, "maximum_warmup");
  for (const endpoint of [config?.controller?.baseUrl, config?.prometheus?.baseUrl]) {
    try {
      const parsed = new URL(endpoint);
      check(parsed.protocol === "https:" && parsed.username === "" && parsed.password === "", "secure_endpoint");
    } catch {
      failures.push("secure_endpoint");
    }
  }
  for (const client of [config?.controller, config?.prometheus]) {
    check(typeof client?.bearerTokenFile === "string" && path.isAbsolute(client.bearerTokenFile), "token_file");
    for (const key of ["caFile", "clientCertificateFile", "clientKeyFile"]) check(typeof client?.[key] === "string" && path.isAbsolute(client[key]), `tls_${key}`);
  }
	const environment = config?.environment ?? {};
	check(environment.topology === "reference_production" && environment.highAvailability === true && environment.localFoundation === false, "production_topology");
	check(environment.deploymentEnvironment === profile?.profileId, "deployment_environment");
  check(Array.isArray(environment.regions) && environment.regions.length === 1, "region_count");
  check(Array.isArray(environment.availabilityZones) && environment.availabilityZones.length >= 2, "availability_zones");
  for (const key of evidenceKeys) check(/^(sha256:)?[0-9a-f]{64}$/.test(environment[key] ?? ""), `evidence_${key}`);
  for (const group of ["vectors", "slos", "zeroTolerance"]) {
    const expected = group === "zeroTolerance" ? [...profile.zeroTolerance].sort() : Object.keys(profile[group] ?? {}).sort();
    const actual = Object.keys(config?.queries?.[group] ?? {}).sort();
    check(exactKeys(actual, expected), `queries_${group}`);
    for (const query of Object.values(config?.queries?.[group] ?? {})) check(typeof query === "string" && query.length > 0 && query.length <= 4096 && query.includes("{{profile_id}}"), `query_${group}`);
  }
	check(canonicalQueries && canonicalJSON(config?.queries) === canonicalJSON(canonicalQueries), "query_contract");
  return [...new Set(failures)].sort();
}

export function reducePrometheusRange(raw, mode) {
  const response = JSON.parse(raw);
  if (response?.status !== "success" || response?.data?.resultType !== "matrix" || !Array.isArray(response.data.result) || response.data.result.length !== 1) {
    throw new Error("Prometheus query_range must return exactly one matrix series");
  }
  const values = response.data.result[0]?.values;
  if (!Array.isArray(values) || values.length === 0) throw new Error("Prometheus query_range returned no observations");
  const numeric = values.map((sample) => Number(sample?.[1]));
  if (numeric.some((value) => !Number.isFinite(value))) throw new Error("Prometheus query_range returned a non-finite observation");
  let value;
  if (mode === "minimum") value = Math.min(...numeric);
  else if (mode === "maximum") value = Math.max(...numeric);
  else throw new Error(`unsupported reduction mode: ${mode}`);
  return { value, observations: numeric.length, rawSeriesSha256: sha256(raw) };
}

function reductionForSLO(name) {
  return name.endsWith("Minimum") ? "minimum" : "maximum";
}

async function loadClientConfig(config) {
  const [token, ca, cert, key] = await Promise.all([
    readFile(config.bearerTokenFile, "utf8"), readFile(config.caFile),
    readFile(config.clientCertificateFile), readFile(config.clientKeyFile),
  ]);
  if (token.trim() === "" || token.includes("\n") && token.trim().includes("\n")) throw new Error("bearer token file must contain exactly one non-empty token");
  return { token: token.trim(), ca, cert, key };
}

async function requestJSON(baseUrl, route, client, { method = "GET", body, timeoutMilliseconds = 15_000 } = {}) {
  const url = new URL(route, baseUrl);
  if (url.protocol !== "https:" || url.username || url.password) throw new Error("capacity control requests require credential-free HTTPS URLs");
  const payload = body === undefined ? undefined : Buffer.from(JSON.stringify(body));
  return await new Promise((resolve, reject) => {
    const request = httpsRequest(url, {
      method, ca: client.ca, cert: client.cert, key: client.key, rejectUnauthorized: true,
      timeout: timeoutMilliseconds,
      headers: {
        authorization: `Bearer ${client.token}`,
        accept: "application/json",
        ...(payload ? { "content-type": "application/json", "content-length": payload.length } : {}),
      },
    }, (response) => {
      const chunks = [];
      let size = 0;
      response.on("data", (chunk) => {
        size += chunk.length;
        if (size > 16 * 1024 * 1024) {
          request.destroy(new Error("capacity control response exceeds 16 MiB"));
          return;
        }
        chunks.push(chunk);
      });
      response.on("end", () => {
        const raw = Buffer.concat(chunks).toString("utf8");
        if ((response.statusCode ?? 500) < 200 || (response.statusCode ?? 500) >= 300) {
          reject(new Error(`capacity control HTTP ${response.statusCode}`));
          return;
        }
        try { resolve({ parsed: JSON.parse(raw), raw }); } catch { reject(new Error("capacity control returned invalid JSON")); }
      });
    });
    request.on("timeout", () => request.destroy(new Error("capacity control request timed out")));
    request.on("error", reject);
    if (payload) request.write(payload);
    request.end();
  });
}

function assertControllerBinding(status, runId, profileId, sourceCommit, expectedStatuses) {
  const allowed = Array.isArray(expectedStatuses) ? expectedStatuses : [expectedStatuses];
  if (status?.runId !== runId || status?.profileId !== profileId || status?.sourceCommit !== sourceCommit || !allowed.includes(status?.status) || !/^[0-9a-f]{64}$/.test(status?.datasetSha256 ?? "")) {
    throw new Error("load controller response is not bound to the requested run/profile/commit/status");
  }
}

function assertFreshHeartbeat(status, intervalSeconds) {
  const heartbeat = Date.parse(status?.lastHeartbeatAt ?? "");
  if (!Number.isFinite(heartbeat) || heartbeat > Date.now() + 5000 || Date.now() - heartbeat > intervalSeconds * 3000) throw new Error("load controller heartbeat is stale");
}

const sleep = (milliseconds) => new Promise((resolve) => setTimeout(resolve, milliseconds));

async function observe(config, profile, profileRaw, canonicalQueries, output) {
  const failures = validateObserverConfig(config, profile, canonicalQueries);
  if (failures.length) throw new Error(`invalid capacity observer config: ${failures.join(", ")}`);
  const head = execFileSync("git", ["rev-parse", "HEAD"], { encoding: "utf8" }).trim();
  if (head !== config.sourceCommit) throw new Error("capacity observer sourceCommit does not match Git HEAD");
  if (execFileSync("git", ["status", "--porcelain"], { encoding: "utf8" }).trim() !== "") throw new Error("capacity observer requires a clean Git worktree");

  const [controllerClient, prometheusClient] = await Promise.all([loadClientConfig(config.controller), loadClientConfig(config.prometheus)]);
  const profileHash = sha256(profileRaw);
  let interruptionSignal = "";
  const interruptINT = () => { interruptionSignal = "SIGINT"; };
  const interruptTERM = () => { interruptionSignal = "SIGTERM"; };
  process.once("SIGINT", interruptINT);
  process.once("SIGTERM", interruptTERM);
  let runId = "";
  let completed = false;
  try {
    const start = await requestJSON(config.controller.baseUrl, "/v1/reference-capacity-runs", controllerClient, {
      method: "POST",
      body: { profileId: profile.profileId, profileSha256: profileHash, sourceCommit: config.sourceCommit, durationSeconds: profile.durationSeconds },
    });
    runId = start.parsed?.runId ?? "";
    if (!/^[A-Za-z0-9][A-Za-z0-9._:-]{7,127}$/.test(runId)) throw new Error("load controller returned an invalid runId");
    assertControllerBinding(start.parsed, runId, profile.profileId, config.sourceCommit, ["warming", "running"]);
    if (start.parsed.datasetSha256 !== config.environment.datasetManifestSha256.replace(/^sha256:/, "")) throw new Error("load controller dataset does not match the immutable environment evidence");
    assertFreshHeartbeat(start.parsed, config.sampleIntervalSeconds);
    const warmupDeadline = Date.now() + config.maximumWarmupSeconds * 1000;
    let running = start.parsed;
    while (running.status === "warming") {
      if (Date.now() >= warmupDeadline) throw new Error("load controller did not reach the fixed steady state before the warmup deadline");
      await sleep(Math.min(config.sampleIntervalSeconds * 1000, warmupDeadline - Date.now()));
      if (interruptionSignal) throw new Error(`capacity observation interrupted by ${interruptionSignal}`);
      const status = await requestJSON(config.controller.baseUrl, `/v1/reference-capacity-runs/${encodeURIComponent(runId)}`, controllerClient);
      assertControllerBinding(status.parsed, runId, profile.profileId, config.sourceCommit, ["warming", "running"]);
      if (status.parsed.datasetSha256 !== start.parsed.datasetSha256) throw new Error("load controller changed the immutable dataset binding");
      assertFreshHeartbeat(status.parsed, config.sampleIntervalSeconds);
      running = status.parsed;
    }
    const startedMilliseconds = Date.parse(running.startedAt ?? "");
    if (!Number.isFinite(startedMilliseconds) || startedMilliseconds > Date.now() + 5000 || Date.now() - startedMilliseconds > config.sampleIntervalSeconds * 2000) throw new Error("load controller running timestamp is invalid or stale");
    const startedAt = new Date(startedMilliseconds);
    const runningStartedAt = running.startedAt;
    while (Date.now() - startedAt.getTime() < profile.durationSeconds * 1000) {
      await sleep(Math.min(config.sampleIntervalSeconds * 1000, profile.durationSeconds * 1000 - (Date.now() - startedAt.getTime())));
      if (interruptionSignal) throw new Error(`capacity observation interrupted by ${interruptionSignal}`);
      const status = await requestJSON(config.controller.baseUrl, `/v1/reference-capacity-runs/${encodeURIComponent(runId)}`, controllerClient);
      assertControllerBinding(status.parsed, runId, profile.profileId, config.sourceCommit, "running");
      if (status.parsed.datasetSha256 !== start.parsed.datasetSha256) throw new Error("load controller changed the immutable dataset binding");
      assertFreshHeartbeat(status.parsed, config.sampleIntervalSeconds);
      if (status.parsed.startedAt !== runningStartedAt) throw new Error("load controller changed the measured start timestamp");
    }
    const stop = await requestJSON(config.controller.baseUrl, `/v1/reference-capacity-runs/${encodeURIComponent(runId)}/complete`, controllerClient, { method: "POST" });
    assertControllerBinding(stop.parsed, runId, profile.profileId, config.sourceCommit, "completed");
    if (stop.parsed.datasetSha256 !== start.parsed.datasetSha256) throw new Error("load controller changed the immutable dataset binding");
    if (stop.parsed.startedAt !== runningStartedAt) throw new Error("load controller completion changed the measured start timestamp");
    const completedMilliseconds = Date.parse(stop.parsed.completedAt ?? "");
    if (!Number.isFinite(completedMilliseconds) || completedMilliseconds > Date.now() + 5000 || Date.now() - completedMilliseconds > config.sampleIntervalSeconds * 2000) throw new Error("load controller completion timestamp is invalid or stale");
    const finishedAt = new Date(completedMilliseconds);
    const elapsedSeconds = (finishedAt.getTime() - startedAt.getTime()) / 1000;
    if (elapsedSeconds < profile.durationSeconds) throw new Error("capacity observation ended before the profile duration");

    const measurements = { vectors: {}, slos: {}, zeroTolerance: {} };
    const prometheusSnapshot = [];
    for (const [group, queries] of Object.entries(config.queries)) {
      for (const [name, query] of Object.entries(queries)) {
        const renderedQuery = query.replaceAll("{{profile_id}}", profile.profileId);
        const params = new URLSearchParams({ query: renderedQuery, start: String(startedAt.getTime() / 1000), end: String(finishedAt.getTime() / 1000), step: String(config.sampleIntervalSeconds) });
        const response = await requestJSON(config.prometheus.baseUrl, `/api/v1/query_range?${params}`, prometheusClient);
        prometheusSnapshot.push(Buffer.from(`${group}:${name}\0`), Buffer.from(response.raw), Buffer.from("\0"));
        const mode = group === "vectors" ? "minimum" : group === "zeroTolerance" ? "maximum" : reductionForSLO(name);
        const reduced = reducePrometheusRange(response.raw, mode);
        measurements[group][name] = {
          ...(group === "vectors" ? { target: profile.vectors[name], achieved: reduced.value } : {}),
          ...(group === "slos" ? { threshold: profile.slos[name], value: reduced.value } : {}),
          ...(group === "zeroTolerance" ? { value: reduced.value } : {}),
          observations: reduced.observations, prometheusQuery: renderedQuery, rawSeriesSha256: reduced.rawSeriesSha256,
        };
      }
    }
    const result = {
      resultVersion: "1.0.0", profileId: profile.profileId, sourceCommit: config.sourceCommit,
      startedAt: startedAt.toISOString(), finishedAt: finishedAt.toISOString(), durationSeconds: elapsedSeconds,
      environment: { ...config.environment, queryContractSha256: sha256(canonicalJSON(canonicalQueries)), prometheusSnapshotSha256: sha256(Buffer.concat(prometheusSnapshot)), loadControllerAttestationSha256: sha256(stop.raw), loadControllerRunId: runId },
      ...measurements,
    };
    await mkdir(path.dirname(output), { recursive: true });
    await writeFile(output, `${JSON.stringify(result, null, 2)}\n`, { mode: 0o600 });
    await chmod(output, 0o600);
    completed = true;
    return result;
  } finally {
    process.removeListener("SIGINT", interruptINT);
    process.removeListener("SIGTERM", interruptTERM);
    if (!completed && /^[A-Za-z0-9][A-Za-z0-9._:-]{7,127}$/.test(runId)) {
      try { await requestJSON(config.controller.baseUrl, `/v1/reference-capacity-runs/${encodeURIComponent(runId)}/abort`, controllerClient, { method: "POST" }); } catch { /* preserve the original failure */ }
    }
  }
}

async function main() {
  const args = new Map();
  for (let index = 2; index < process.argv.length; index += 2) args.set(process.argv[index], process.argv[index + 1]);
  const configPath = args.get("--config");
  const output = path.resolve(args.get("--output") ?? "load-tests/stage3/results/reference-capacity-result.json");
  if (!configPath) throw new Error("usage: run-stage3-capacity-observer.mjs --config <absolute-config.json> [--output <raw-result.json>]");
  const [configRaw, profileRaw, canonicalRaw] = await Promise.all([readFile(path.resolve(configPath)), readFile("load-tests/stage3/reference-capacity-profile.json"), readFile("load-tests/stage3/reference-capacity-observer.example.json")]);
  const result = await observe(JSON.parse(configRaw), JSON.parse(profileRaw), profileRaw, JSON.parse(canonicalRaw).queries, output);
  console.log(`stage-3 capacity observation: duration=${result.durationSeconds}s vectors=${Object.keys(result.vectors).length} status=recorded output=${output}`);
}

if (import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) await main();
