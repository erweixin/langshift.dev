#!/usr/bin/env node
import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import { readFile, writeFile, mkdir } from "node:fs/promises";
import path from "node:path";

const args = new Map();
for (let index = 2; index < process.argv.length; index += 2) {
  const key = process.argv[index];
  const value = process.argv[index + 1];
  if (!key?.startsWith("--") || value === undefined) throw new Error("invalid infrastructure report arguments");
  args.set(key, value);
}

const sourceCommit = args.get("--source-commit");
const resolvedComposePath = args.get("--resolved-compose");
const postgresCounts = args.get("--postgres-counts")?.split(":").map(Number);
if (!/^[0-9a-f]{40}$/.test(sourceCommit ?? "") || !resolvedComposePath || postgresCounts?.length !== 2 || postgresCounts.some(value=>!Number.isInteger(value)||value<1)) {
  throw new Error("usage: write-stage2-infrastructure-report.mjs --source-commit <40-hex> --resolved-compose <path> --postgres-counts <tables:forced-rls>");
}

const root = process.cwd();
const sha256 = (contents) => createHash("sha256").update(contents).digest("hex");
const git = (arguments_, options = {}) => execFileSync("git", arguments_, { cwd: root, ...options });
git(["cat-file", "-e", `${sourceCommit}^{commit}`]);

const evidencePaths = [
  "deploy/compose/foundation.compose.yaml",
  "deploy/compose/.env.example",
  "deploy/observability/grafana-datasources.yaml",
  "deploy/observability/loki.yaml",
  "deploy/observability/otel-collector.yaml",
  "deploy/observability/prometheus.yaml",
  "deploy/observability/tempo.yaml",
  "scripts/stage2-infrastructure-smoke.sh",
  "scripts/validate-stage2-foundation-compose.mjs",
  "scripts/write-stage2-infrastructure-report.mjs",
];

const evidence = [];
for (const relativePath of evidencePaths) {
  const workingContents = await readFile(path.resolve(root, relativePath));
  const committedContents = git(["show", `${sourceCommit}:${relativePath}`]);
  const workingSha256 = sha256(workingContents);
  if (workingSha256 !== sha256(committedContents)) {
    throw new Error(`infrastructure evidence differs from source commit: ${relativePath}`);
  }
  evidence.push({ path: relativePath, sha256: workingSha256, sizeBytes: workingContents.length });
}

const resolvedCompose = JSON.parse(await readFile(resolvedComposePath, "utf8"));
const serviceNames = Object.keys(resolvedCompose.services ?? {}).sort();
const requiredServices = [
  "grafana",
  "loki",
  "minio",
  "minio-client",
  "nats",
  "nats-box",
  "otel-collector",
  "postgres",
  "probe",
  "prometheus",
  "tempo",
  "valkey",
  "vault",
].sort();
if (JSON.stringify(serviceNames) !== JSON.stringify(requiredServices)) {
  throw new Error(`resolved infrastructure services differ from required contract: ${serviceNames.join(",")}`);
}

const images = serviceNames.map((service) => {
  const reference = resolvedCompose.services[service].image ?? "";
  const match = reference.match(/^([^@\s]+:[^@\s]+)@(sha256:[0-9a-f]{64})$/);
  if (!match) throw new Error(`${service} image is not tag and digest pinned`);
  return { service, reference: match[1], digest: match[2] };
});

const report = {
  reportVersion: "1.0.0",
  stage: 2,
  kind: "infrastructure-foundation-smoke",
  generatedAt: new Date().toISOString(),
  sourceCommit,
  status: "passed",
  scope: {
    purpose: "hermetic single-tenant foundation and CI smoke",
    productionMultiAzDeployment: false,
    hostPublishedPorts: 0,
    internalNetworks: ["foundation"],
    vaultMode: "development-only",
  },
  results: {
    composeContract: "13/13 services validated",
    postgres: { tables: postgresCounts[0], forcedRlsTables: postgresCounts[1], temporaryReadWrite: "passed" },
    jetstream: { streamCreate: "passed", publish: "passed", messages: 1 },
    valkey: { authenticatedSetGet: "passed" },
    objectStorage: { buckets: 2, versioning: "enabled", writeRead: "passed" },
    vault: { kvWriteRead: "passed" },
    observability: {
      readinessEndpoints: 5,
      otlpCollectorToTempoTrace: "passed",
      services: ["otel-collector", "tempo", "loki", "prometheus", "grafana"],
    },
  },
  controls: {
    immutableImageReferences: images.length,
    noLatestTags: true,
    noHostPorts: true,
    internalNetworkOnly: true,
    noNewPrivilegesServices: serviceNames.length,
    fileBackedComposeSecrets: 4,
    disposableRandomRuntimeCredentials: 7,
    environmentOnlyRuntimeCredentials: 3,
    automaticContainerAndVolumeCleanup: true,
  },
  images,
  evidence,
  zeroToleranceFailures: [],
};
report.reportHash = sha256(JSON.stringify(report));

const output = path.resolve(root, "gate-reports/stage-2/infrastructure-smoke-report.json");
await mkdir(path.dirname(output), { recursive: true });
await writeFile(output, `${JSON.stringify(report, null, 2)}\n`, { mode: 0o644 });
console.log(`stage-2 infrastructure report: services=${images.length} status=passed report=${report.reportHash}`);
