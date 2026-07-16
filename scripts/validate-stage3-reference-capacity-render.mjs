#!/usr/bin/env node
import { readFile } from "node:fs/promises";

const input = process.argv[2];
if (!input) throw new Error("usage: validate-stage3-reference-capacity-render.mjs RENDERED_YAML");
const rendered = await readFile(input, "utf8");
const canonical = await readFile("load-tests/stage3/reference-capacity-profile.json", "utf8");
const chartCopy = await readFile("deploy/helm/reference-capacity/files/reference-capacity-profile.json", "utf8");
const failures = [];
const check = (condition, message) => { if (!condition) failures.push(message); };
check(canonical === chartCopy, "chart capacity profile is not byte-identical to the gate profile");
const renderedProfileBlock = rendered.match(/reference-capacity-profile\.json: \|\n((?:    .*\n)+)/)?.[1] ?? "";
const renderedProfile = renderedProfileBlock.split("\n").filter((line, index, lines) => index < lines.length - 1 || line !== "").map((line) => line.slice(4)).join("\n") + (renderedProfileBlock.endsWith("\n") ? "\n" : "");
check(renderedProfile === canonical, "rendered ConfigMap changes the byte-level capacity profile hash");
for (const kind of ["ServiceAccount", "ConfigMap", "Deployment", "Service", "NetworkPolicy"]) check(new RegExp(`^kind: ${kind}$`, "m").test(rendered), `missing ${kind}`);
for (const pattern of [
  /replicas: 1\b/, /type: Recreate/, /automountServiceAccountToken: false/, /runAsNonRoot: true/, /runAsUser: 65532/,
  /readOnlyRootFilesystem: true/, /allowPrivilegeEscalation: false/, /capabilities: \{drop: \[ALL\]\}/,
  /persistentVolumeClaim:\s*(?:\{claimName: reference-capacity-dataset\}|\n\s+claimName: reference-capacity-dataset)/,
  /LITES_ENVIRONMENT\s*, value: stage3-reference-production-v1/,
  /LITES_SOURCE_COMMIT\s*, value: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"/,
  /image: "registry\.example\.invalid\/lites\/reference-capacity-controller@sha256:[a-f0-9]{64}"/,
  /policyTypes: \[Ingress, Egress\]/,
  /cidr: "10\.20\.0\.0\/24"/,
  /cidr: "10\.30\.0\.0\/24"/,
]) check(pattern.test(rendered), `rendered contract does not match ${pattern}`);
check(!/kind: Secret\b/.test(rendered), "chart rendered a plaintext Secret");
check(!/hostNetwork: true|hostPID: true|hostIPC: true|hostPort:/.test(rendered), "chart uses a host namespace or port");
check(!/0\.0\.0\.0\/0|::\/0/.test(rendered), "chart allows unrestricted egress");
if (failures.length) {
  for (const failure of failures) console.error(failure);
  process.exit(1);
}
console.log("stage-3 reference capacity chart: resources=5 fixed_profile=1 isolated_load_node=1 status=passed");
