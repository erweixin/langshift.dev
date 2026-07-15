#!/usr/bin/env node
import { readdir, readFile } from "node:fs/promises";
import path from "node:path";

const root = process.argv[2];
if (!root) throw new Error("usage: validate-stage2-nats-render.mjs RENDERED_DIRECTORY");

async function files(directory) {
  const result = [];
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    const target = path.join(directory, entry.name);
    if (entry.isDirectory()) result.push(...await files(target));
    else if (entry.isFile() && /\.ya?ml$/.test(entry.name)) result.push(target);
  }
  return result;
}

const documents = [];
for (const file of await files(root)) {
  const contents = await readFile(file, "utf8");
  for (const body of contents.split(/^---\s*$/m)) {
    const kind = body.match(/^kind:\s*([^\s]+)\s*$/m)?.[1];
    if (kind) documents.push({ kind, body });
  }
}

const failures = [];
const assert = (condition, message) => { if (!condition) failures.push(message); };
const ofKind = (kind) => documents.filter((document) => document.kind === kind);
const statefulSet = ofKind("StatefulSet")[0]?.body ?? "";
const configMap = ofKind("ConfigMap")[0]?.body ?? "";
const pdb = ofKind("PodDisruptionBudget")[0]?.body ?? "";
const storageClass = ofKind("StorageClass")[0]?.body ?? "";
const externalSecret = ofKind("ExternalSecret")[0]?.body ?? "";
const policies = ofKind("NetworkPolicy").map((document) => document.body).join("\n");

assert(ofKind("StatefulSet").length === 1 && /replicas:\s+3\b/.test(statefulSet), "NATS must render one three-replica StatefulSet");
assert((statefulSet.match(/image:\s+[^\s]+@sha256:[0-9a-f]{64}/g) ?? []).length === 1, "the NATS runtime image must be digest pinned and auxiliary images disabled");
for (const requirement of [
  /automountServiceAccountToken:\s+false/,
  /runAsNonRoot:\s+true/,
  /seccompProfile:\s*\n\s+type:\s+RuntimeDefault/,
  /readOnlyRootFilesystem:\s+true/,
  /allowPrivilegeEscalation:\s+false/,
  /capabilities:\s*\n\s+drop:\s*\n\s+- ALL/,
  /minDomains:\s+3[\s\S]*?topologyKey:\s+topology\.kubernetes\.io\/zone[\s\S]*?whenUnsatisfiable:\s+DoNotSchedule/,
  /storageClassName:\s+lites-nats-gp3-encrypted/,
  /storage:\s+200Gi/,
  /startupProbe:/,
  /readinessProbe:/,
  /livenessProbe:/,
]) assert(requirement.test(statefulSet), `NATS StatefulSet control missing: ${requirement}`);
assert(/cpu:\s+"?2"?[\s\S]*?memory:\s+8Gi/.test(statefulSet), "NATS production resource floor is missing");
assert(/"jetstream":\s*\{[\s\S]*?"store_dir":\s*"\/data"/.test(configMap), "JetStream file store is not enabled");
assert((configMap.match(/"min_version":\s*"1\.3"/g) ?? []).length === 2 && (configMap.match(/"verify":\s*true/g) ?? []).length >= 2 && /"verify_and_map":\s*true/.test(configMap), "NATS client and route mTLS 1.3 enforcement is incomplete");
for (const identity of ["lites-outbox-publisher", "lites-agent-scheduler", "lites-identity-import-worker", "lites-identity-mail-worker", "lites-realtime-gateway"]) assert(configMap.includes(`"user": "${identity}"`), `NATS certificate identity ${identity} is missing`);
const realtimeStart=configMap.indexOf('"user": "lites-realtime-gateway"');
const realtimePermissionsStart=realtimeStart<0?-1:configMap.lastIndexOf('"permissions":',realtimeStart);
const realtimePermissions=realtimePermissionsStart<0?"":configMap.slice(realtimePermissionsStart,realtimeStart);
assert(realtimePermissions.includes("$JS.API.INFO")&&realtimePermissions.includes("lites.commands.events.publish")&&!realtimePermissions.includes("lites.commands.>")&&!realtimePermissions.includes("$JS.API.STREAM."), "Realtime Gateway must have only account readiness request and events.publish wake subscription permissions");
assert(!/"publish":\s*\[\s*">"/.test(configMap) && !/"subscribe":\s*\[\s*">"/.test(configMap), "NATS user has unrestricted subject permissions");
assert(/maxUnavailable:\s+1/.test(pdb), "NATS PDB must preserve quorum during disruption");
assert(/provisioner:\s+ebs\.csi\.aws\.com/.test(storageClass) && /encrypted:\s+"true"/.test(storageClass) && /kmsKeyId:\s+"arn:aws:kms:/.test(storageClass) && /reclaimPolicy:\s+Retain/.test(storageClass), "KMS-encrypted retained gp3 storage class is incomplete");
assert(/^apiVersion:\s+external-secrets\.io\/v1$/m.test(externalSecret) && /creationPolicy:\s+Owner/.test(externalSecret) && /deletionPolicy:\s+Retain/.test(externalSecret), "NATS TLS ExternalSecret lifecycle is unsafe");
assert(/name:\s+lites-nats-default-deny[\s\S]*?policyTypes:\s+\[Ingress, Egress\]/.test(policies), "NATS default-deny policy is missing");
for (const port of [4222, 6222, 8222, 53]) assert(new RegExp(`port: ${port}`).test(policies), `NATS NetworkPolicy lacks port ${port}`);
assert(!documents.some((document) => document.kind === "Secret"), "render includes a plaintext Kubernetes Secret");

if (failures.length) {
  for (const failure of failures) console.error(failure);
  process.exit(1);
}
console.log("stage-2 nats contract: replicas=3 jetstream=file mTLS=1.3 storage=kms-gp3 default_deny=1 status=passed");
