#!/usr/bin/env node
import { readdir, readFile } from "node:fs/promises";
import path from "node:path";

const root = process.argv[2];
if (!root) throw new Error("usage: validate-stage2-helm-render.mjs RENDERED_DIRECTORY");

async function yamlFiles(directory) {
  const result = [];
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    const target = path.join(directory, entry.name);
    if (entry.isDirectory()) result.push(...await yamlFiles(target));
    else if (entry.isFile() && /\.ya?ml$/.test(entry.name)) result.push(target);
  }
  return result;
}

const documents = [];
for (const file of await yamlFiles(root)) {
  const contents = await readFile(file, "utf8");
  for (const body of contents.split(/^---\s*$/m)) {
    const kind = body.match(/^kind:\s*([^\s]+)\s*$/m)?.[1];
    if (kind) documents.push({ file, body, kind, name: body.match(/^metadata:\s*\n\s+name:\s*([^\s]+)\s*$/m)?.[1] ?? "" });
  }
}

const failures = [];
const assert = (condition, message) => { if (!condition) failures.push(message); };
const byKind = (kind) => documents.filter((document) => document.kind === kind);
const expectedCounts = {
  ConfigMap: 6,
  Deployment: 6,
  ExternalSecret: 6,
  HorizontalPodAutoscaler: 5,
  Job: 1,
  NetworkPolicy: 9,
  PodDisruptionBudget: 6,
  Service: 3,
  ServiceAccount: 6,
};
for (const [kind, count] of Object.entries(expectedCounts)) assert(byKind(kind).length === count, `${kind} count ${byKind(kind).length}, expected ${count}`);
assert(documents.length === 48, `resource count ${documents.length}, expected 48`);

const components = ["api-gateway", "identity-service", "identity-import-worker", "identity-mail-worker", "outbox-publisher", "store-epoch-authority"];
for (const component of components) {
  const deployment = byKind("Deployment").find((document) => document.name === `lites-${component}`);
  assert(Boolean(deployment), `missing Deployment for ${component}`);
  if (!deployment) continue;
  const body = deployment.body;
  for (const [pattern, message] of [
    [/replicas:\s+3\b/, "three replicas"],
    [/image:\s+"[^"\s]+@sha256:[0-9a-f]{64}"/, "digest-only image"],
    [/maxUnavailable:\s+0/, "zero unavailable rolling update"],
    [/runAsNonRoot:\s+true/, "non-root pod"],
    [/runAsUser:\s+65532/, "fixed runtime uid"],
    [/automountServiceAccountToken:\s+false/, "disabled service account token"],
    [/readOnlyRootFilesystem:\s+true/, "read-only root filesystem"],
    [/allowPrivilegeEscalation:\s+false/, "disabled privilege escalation"],
    [/capabilities:\s+\{drop:\s+\[ALL\]\}/, "all capabilities dropped"],
    [/topologyKey:\s+topology\.kubernetes\.io\/zone[\s\S]*?whenUnsatisfiable:\s+DoNotSchedule/, "required zone spread"],
    [/minDomains:\s+3/, "three zone domains"],
    [/secret\.reloader\.stakater\.com\/reload:/, "secret rotation restart"],
    [/resources:[\s\S]*?limits:[\s\S]*?requests:/, "resource requests and limits"],
  ]) assert(pattern.test(body), `${component} missing ${message}`);
  assert(!/host(Network|PID|IPC):\s+true|hostPort:/.test(body), `${component} uses a host namespace or port`);
  assert(!/:latest(?:@|"|\s)/.test(body), `${component} uses latest image`);
}

for (const externalSecret of byKind("ExternalSecret")) {
  assert(/^apiVersion:\s+external-secrets\.io\/v1\s*$/m.test(externalSecret.body), `${externalSecret.name} does not use ExternalSecret v1`);
  assert(/creationPolicy:\s+Owner/.test(externalSecret.body) && /deletionPolicy:\s+Retain/.test(externalSecret.body), `${externalSecret.name} has unsafe target lifecycle`);
  assert(/dataFrom:[\s\S]*extract:[\s\S]*key:/.test(externalSecret.body), `${externalSecret.name} has no remote extract`);
}

const defaultDeny = byKind("NetworkPolicy").find((document) => document.name === "lites-default-deny");
assert(Boolean(defaultDeny) && /podSelector:\s+\{\}/.test(defaultDeny.body) && /policyTypes:\s+\[Ingress, Egress\]/.test(defaultDeny.body), "missing default deny ingress and egress");
const dns = byKind("NetworkPolicy").find((document) => document.name === "lites-dns-egress");
assert(Boolean(dns) && /protocol:\s+UDP, port:\s+53/.test(dns.body) && /protocol:\s+TCP, port:\s+53/.test(dns.body), "missing restricted DNS egress");

for (const pdb of byKind("PodDisruptionBudget")) assert(/minAvailable:\s+2/.test(pdb.body), `${pdb.name} does not preserve two replicas`);
for (const hpa of byKind("HorizontalPodAutoscaler")) {
  assert(/^apiVersion:\s+autoscaling\/v2\s*$/m.test(hpa.body), `${hpa.name} does not use autoscaling/v2`);
  assert(/minReplicas:\s+3/.test(hpa.body) && /stabilizationWindowSeconds:\s+300/.test(hpa.body), `${hpa.name} has unsafe scaling bounds`);
}

const migration = byKind("Job")[0];
assert(Boolean(migration) && /helm\.sh\/hook:\s+pre-install,pre-upgrade/.test(migration.body), "migration is not a pre-install and pre-upgrade hook");
assert(Boolean(migration) && /image:\s+"[^"\s]+@sha256:[0-9a-f]{64}"/.test(migration.body), "migration image is not digest pinned");
assert(Boolean(migration) && /DATABASE_URL_FILE[\s\S]*\/run\/secrets\/database-url/.test(migration.body), "migration database credential is not file-backed");

const configMaps = byKind("ConfigMap").map((document) => document.body).join("\n");
assert(!/^\s{2,}(?:PASSWORD|TOKEN|SECRET|PRIVATE_KEY):/m.test(configMaps), "raw secret-like key is present in a ConfigMap");
assert(!documents.some((document) => /\bkind:\s+Secret\b/.test(document.body)), "chart renders a plaintext Kubernetes Secret");

if (failures.length) {
  for (const failure of failures) console.error(failure);
  process.exit(1);
}
console.log(`stage-2 helm contract: resources=${documents.length} deployments=6 multi_az=6 external_secrets=6 default_deny=1 status=passed`);
