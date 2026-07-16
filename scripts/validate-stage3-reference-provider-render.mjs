#!/usr/bin/env node
import { readFile } from "node:fs/promises";

const input = process.argv[2];
if (!input) throw new Error("usage: validate-stage3-reference-provider-render.mjs RENDERED_YAML");
const rendered = await readFile(input, "utf8");
const failures = [];
const check = (condition, message) => { if (!condition) failures.push(message); };
for (const [kind, count] of Object.entries({ServiceAccount:1, PodDisruptionBudget:1, Deployment:1, Service:2, NetworkPolicy:1})) {
  check((rendered.match(new RegExp(`^kind: ${kind}$`, "gm")) ?? []).length === count, `expected ${count} ${kind}`);
}
for (const pattern of [
  /replicas: 3\b/, /minAvailable: 2\b/, /maxUnavailable: 1/, /maxSurge: 1/,
  /automountServiceAccountToken: false/, /runAsNonRoot: true/, /runAsUser: 65532/,
  /readOnlyRootFilesystem: true/, /allowPrivilegeEscalation: false/, /capabilities: \{drop: \[ALL\]\}/,
  /image: "registry\.example\/lites-reference-provider@sha256:a{64}"/,
  /type: LoadBalancer/, /loadBalancerClass: "service\.k8s\.aws\/nlb"/,
  /loadBalancerSourceRanges: \["203\.0\.113\.0\/24"\]/, /externalTrafficPolicy: Local/,
  /sessionAffinity: ClientIP/, /timeoutSeconds: 10800/,
  /port: 443/, /targetPort: https/, /REFERENCE_PROVIDER_MODEL\s*, value: "reference-model-2026-07-16"/,
  /REFERENCE_LOCAL_HOLD\s*, value: "400ms"/, /REFERENCE_REPLAY_RATE_NUMERATOR\s*, value: "153"/,
  /secretName: reference-provider-secrets/, /policyTypes: \[Ingress, Egress\]/,
  /cidr: "10\.40\.0\.0\/24"/, /egress: \[\]/,
]) check(pattern.test(rendered), `rendered contract does not match ${pattern}`);
check(!/^kind: Secret$/m.test(rendered), "chart rendered a plaintext Secret");
check(!/hostNetwork: true|hostPID: true|hostIPC: true|hostPort:/.test(rendered), "chart uses a host namespace or port");
check(!/0\.0\.0\.0\/0|::\/0/.test(rendered), "chart allows an unrestricted source");
if (failures.length) {
  for (const failure of failures) console.error(failure);
  process.exit(1);
}
console.log("stage-3 reference provider chart: resources=6 replicas=3 public_tls=1 egress_rules=0 status=passed");
