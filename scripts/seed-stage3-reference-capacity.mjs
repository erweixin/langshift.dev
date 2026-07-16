#!/usr/bin/env node
import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import { mkdir, readFile, rename, rm, writeFile } from "node:fs/promises";
import { request as httpsRequest } from "node:https";
import path from "node:path";
import { pathToFileURL } from "node:url";

const sha256 = (value) => createHash("sha256").update(value).digest("hex");
const labelPattern = /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/;
const commitPattern = /^[0-9a-f]{40}$/;
const profile = JSON.parse(await readFile("load-tests/stage3/reference-capacity-profile.json"));

export function validateSeederConfig(config) {
  const failures = [];
  const check = (condition, code) => { if (!condition) failures.push(code); };
  check(config?.configVersion === "1.0.0", "configVersion");
  check(labelPattern.test(config?.datasetId ?? ""), "datasetId");
  check(commitPattern.test(config?.sourceCommit ?? ""), "sourceCommit");
  check(path.isAbsolute(config?.outputDirectory ?? "") && config.outputDirectory !== "/", "outputDirectory");
  check(config?.mountedDirectory === "/run/capacity", "mountedDirectory");
  try {
    const endpoint = new URL(config?.gateway?.baseUrl);
    check(endpoint.protocol === "https:" && endpoint.username === "" && endpoint.password === "" && endpoint.pathname === "/" && endpoint.search === "" && endpoint.hash === "", "gateway.baseUrl");
    const origin = new URL(config?.gateway?.publicOrigin);
    check(origin.protocol === "https:" && origin.username === "" && origin.password === "" && origin.pathname === "/" && origin.search === "" && origin.hash === "", "gateway.publicOrigin");
  } catch { failures.push("gateway.url"); }
  for (const key of ["caFile", "clientCertificateFile", "clientKeyFile"]) check(path.isAbsolute(config?.gateway?.[key] ?? ""), `gateway.${key}`);
  check(labelPattern.test(config?.gateway?.tlsServerName ?? ""), "gateway.tlsServerName");
  check(Number.isInteger(config?.maximumConcurrency) && config.maximumConcurrency >= 1 && config.maximumConcurrency <= 100, "maximumConcurrency");
  check(config?.maximumWarmupSeconds === 900, "maximumWarmupSeconds");
  check(Array.isArray(config?.principals) && config.principals.length >= 6 && config.principals.length <= 100, "principals");
  const labels = new Set();
  const tenants = new Map();
  let connections = 0;
  let hotUsers = 0;
  for (const principal of config?.principals ?? []) {
    check(labelPattern.test(principal?.label ?? "") && !labels.has(principal.label), "principal.label");
    labels.add(principal?.label);
    check(labelPattern.test(principal?.tenantBucket ?? ""), "principal.tenantBucket");
    check(typeof principal?.email === "string" && principal.email.includes("@") && principal.email.length <= 254, "principal.email");
    check(path.isAbsolute(principal?.passwordFile ?? ""), "principal.passwordFile");
    check(typeof principal?.missionId === "string" && principal.missionId.length > 0 && principal.missionId.length <= 255, "principal.missionId");
    check(["coach", "task", "project"].includes(principal?.conversationMode), "principal.conversationMode");
    check(Number.isInteger(principal?.connectionCount) && principal.connectionCount >= 0 && principal.connectionCount <= 10000, "principal.connectionCount");
    check(Number.isInteger(principal?.conversationCount) && principal.conversationCount >= 100 && principal.conversationCount <= 10000, "principal.conversationCount");
    check(Array.isArray(principal?.scenarios) && principal.scenarios.length > 0 && principal.scenarios.length <= 100 && principal.scenarios.every((scenario) => labelPattern.test(scenario)), "principal.scenarios");
    connections += principal?.connectionCount ?? 0;
    tenants.set(principal?.tenantBucket, (tenants.get(principal?.tenantBucket) ?? 0) + (principal?.connectionCount ?? 0));
    if (principal?.hotUser === true) hotUsers++;
  }
  check(connections === profile.vectors.concurrentConnections, "connections");
  check(tenants.size === 5 && [...tenants.values()].every((count) => count === connections / 5), "tenantShares");
  check(hotUsers === 1, "hotUsers");
  return [...new Set(failures)].sort();
}

async function loadClient(config) {
  const [ca, cert, key] = await Promise.all([readFile(config.caFile), readFile(config.clientCertificateFile), readFile(config.clientKeyFile)]);
  return { ca, cert, key, servername: config.tlsServerName, baseUrl: config.baseUrl, publicOrigin: config.publicOrigin };
}

async function requestJSON(client, route, { body, cookies = [], idempotencyKey } = {}) {
  const url = new URL(route, client.baseUrl);
  const encoded = body === undefined ? undefined : Buffer.from(JSON.stringify(body));
  return await new Promise((resolve, reject) => {
    const request = httpsRequest(url, {
      method: "POST", ca: client.ca, cert: client.cert, key: client.key, servername: client.servername,
      rejectUnauthorized: true, timeout: 30_000,
      headers: {
        accept: "application/json", origin: client.publicOrigin,
        ...(encoded ? { "content-type": "application/json", "content-length": encoded.length } : {}),
        ...(cookies.length ? { cookie: cookies.join("; "), "x-csrf-token": cookieValue(cookies, "__Host-lites_csrf") } : {}),
        ...(idempotencyKey ? { "idempotency-key": idempotencyKey } : {}),
        "user-agent": "lites-reference-capacity-seeder/1.0",
      },
    }, (response) => {
      const chunks = [];
      let size = 0;
      response.on("data", (chunk) => {
        size += chunk.length;
        if (size > 1024 * 1024) request.destroy(new Error("seed response exceeds 1 MiB"));
        else chunks.push(chunk);
      });
      response.on("end", () => {
        const raw = Buffer.concat(chunks).toString("utf8");
        if ((response.statusCode ?? 500) < 200 || (response.statusCode ?? 500) >= 300) {
          reject(new Error(`seed request ${url.pathname} returned HTTP ${response.statusCode}`));
          return;
        }
        try { resolve({ body: JSON.parse(raw), cookies: response.headers["set-cookie"] ?? [], etag: response.headers.etag ?? "" }); }
        catch { reject(new Error(`seed request ${url.pathname} returned invalid JSON`)); }
      });
    });
    request.on("timeout", () => request.destroy(new Error("seed request timed out")));
    request.on("error", reject);
    if (encoded) request.write(encoded);
    request.end();
  });
}

export function cookieValue(cookies, name) {
  for (const cookie of cookies) {
    const first = cookie.split(";", 1)[0];
    const separator = first.indexOf("=");
    if (separator > 0 && first.slice(0, separator) === name) return first.slice(separator + 1);
  }
  return "";
}

async function mapLimit(values, concurrency, operation) {
  const result = new Array(values.length);
  let next = 0;
  await Promise.all(Array.from({ length: Math.min(concurrency, values.length) }, async () => {
    while (true) {
      const index = next++;
      if (index >= values.length) return;
      result[index] = await operation(values[index], index);
    }
  }));
  return result;
}

async function seedPrincipal(config, principal, client, credentialsDirectory) {
  const password = (await readFile(principal.passwordFile, "utf8")).trim();
  if (password === "" || password.includes("\n") || password.includes("\r") || password.includes("\0")) throw new Error(`invalid password file for ${principal.label}`);
  const login = await requestJSON(client, "/v1/auth/login", { body: { request_id: `capacity-login-${principal.label}`, email: principal.email, password } });
  const session = cookieValue(login.cookies, "__Host-lites_session");
  const csrf = cookieValue(login.cookies, "__Host-lites_csrf");
  if (!session || !csrf || !login.body?.expires_at || Date.parse(login.body.expires_at) - Date.now() < (profile.durationSeconds + config.maximumWarmupSeconds + 600) * 1000) throw new Error(`session for ${principal.label} does not cover warmup and measurement`);
  const sessionName = `${principal.label}-session`;
  const csrfName = `${principal.label}-csrf`;
  await Promise.all([
    writeFile(path.join(credentialsDirectory, sessionName), `${session}\n`, { mode: 0o600, flag: "wx" }),
    writeFile(path.join(credentialsDirectory, csrfName), `${csrf}\n`, { mode: 0o600, flag: "wx" }),
  ]);
  const indexes = Array.from({ length: principal.conversationCount }, (_, index) => index);
  const conversations = await mapLimit(indexes, config.maximumConcurrency, async (index) => {
    const requestId = `capacity-conversation-${principal.label}-${String(index).padStart(5, "0")}`;
    const created = await requestJSON(client, "/v1/conversations", {
      cookies: login.cookies.map((cookie) => cookie.split(";", 1)[0]),
      idempotencyKey: requestId,
      body: { request_id: requestId, mission_id: principal.missionId, title: `Reference ${principal.label} ${index + 1}`, mode: principal.conversationMode },
    });
    if (!created.body?.id || created.body.version !== 1 || created.etag !== '"1"' || created.body.status !== "active") throw new Error(`invalid Conversation seed response for ${principal.label}/${index}`);
    return { id: created.body.id, version: 1, scenario: principal.scenarios[index % principal.scenarios.length] };
  });
  return {
    label: principal.label, tenantBucket: principal.tenantBucket, hotUser: principal.hotUser === true,
    sessionTokenFile: `${config.mountedDirectory}/credentials/${sessionName}`,
    csrfTokenFile: `${config.mountedDirectory}/credentials/${csrfName}`,
    connectionCount: principal.connectionCount, conversations,
  };
}

async function main() {
  const configPath = process.argv[2];
  if (!configPath || !path.isAbsolute(configPath)) throw new Error("usage: seed-stage3-reference-capacity.mjs /absolute/seeder-config.json");
  const config = JSON.parse(await readFile(configPath, "utf8"));
  const failures = validateSeederConfig(config);
  if (failures.length) throw new Error(`invalid reference capacity seeder config: ${failures.join(", ")}`);
  const head = execFileSync("git", ["rev-parse", "HEAD"], { encoding: "utf8" }).trim();
  if (head !== config.sourceCommit) throw new Error("reference capacity seeder sourceCommit does not match Git HEAD");
  if (execFileSync("git", ["status", "--porcelain"], { encoding: "utf8" }).trim() !== "") throw new Error("reference capacity seeder requires a clean Git worktree");
  const client = await loadClient(config.gateway);
  let created = false;
  try {
    await mkdir(config.outputDirectory, { mode: 0o700 });
    created = true;
    const credentialsDirectory = path.join(config.outputDirectory, "credentials");
    await mkdir(credentialsDirectory, { mode: 0o700 });
    const principals = [];
    for (const principal of config.principals) principals.push(await seedPrincipal(config, principal, client, credentialsDirectory));
    const dataset = { datasetVersion: "1.0.0", datasetId: config.datasetId, profileId: profile.profileId, sourceCommit: config.sourceCommit, principals };
    const encoded = `${JSON.stringify(dataset, null, 2)}\n`;
    const temporary = path.join(config.outputDirectory, ".dataset.json.tmp");
    await writeFile(temporary, encoded, { mode: 0o600, flag: "wx" });
    await rename(temporary, path.join(config.outputDirectory, "dataset.json"));
    console.log(`stage-3 reference capacity dataset: principals=${principals.length} conversations=${principals.reduce((sum, principal) => sum + principal.conversations.length, 0)} connections=${principals.reduce((sum, principal) => sum + principal.connectionCount, 0)} sha256=${sha256(encoded)} output=${config.outputDirectory}`);
  } catch (error) {
    if (created) await rm(config.outputDirectory, { recursive: true, force: true });
    throw error;
  }
}

if (import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) await main();
