import { readFile } from "node:fs/promises";
import { resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const foundation = await readFile(resolve(root, "deploy/compose/foundation.compose.yaml"), "utf8");
const local = await readFile(resolve(root, "deploy/compose/local-product.compose.yaml"), "utf8");
const macosProductGate = await readFile(resolve(root, "scripts/macos-product-stack.sh"), "utf8");

const fail = (message) => {
  console.error(`local compose contract failed: ${message}`);
  process.exitCode = 1;
};

if (foundation.includes("../../contracts/database:/docker-entrypoint-initdb.d")) {
  fail("foundation must mount init SQL files individually so the local overlay can add its final initializer");
}
for (const file of ["000001_contract_baseline.sql", "900000_verify_contract.sql"]) {
  if (!foundation.includes(`../../contracts/database/${file}:/docker-entrypoint-initdb.d/${file}:ro`)) {
    fail(`foundation is missing the explicit ${file} initializer mount`);
  }
}
if (!local.includes("./postgres-init-local.sh:/docker-entrypoint-initdb.d/999_local.sh:ro")) {
  fail("local role and migration initializer is not mounted after contract verification");
}
if (!local.includes("test -f /tmp/lites-local-init-complete && pg_isready") || !local.includes("retries: 60")) {
  fail("Postgres health must wait for the complete local migration and role initializer");
}
const postgresInitializer = await readFile(resolve(root, "deploy/compose/postgres-init-local.sh"), "utf8");
if (!postgresInitializer.trimEnd().endsWith("touch /tmp/lites-local-init-complete")) {
  fail("Postgres initializer must write its readiness marker only after all database work");
}
if (!postgresInitializer.includes("engineering-projection@lites.invalid") || !postgresInitializer.includes("20000000-0000-4000-8000-000000000002") || !postgresInitializer.includes("local behavior identities are inconsistent")) {
  fail("the public anonymous tenant is missing its explicit engineering behavior-projection identity");
}
if (!postgresInitializer.includes("lites_behavior_service") || !postgresInitializer.includes("engineering-behavior-automation@lites.invalid") || !postgresInitializer.includes("20000000-0000-4000-8000-000000000003")) {
  fail("the behavior control plane is missing its purpose-scoped role or automation identity");
}
if (!local.includes("networks: [foundation, local-edge]") || !local.includes("\nnetworks:\n  local-edge:\n    driver: bridge")) {
  fail("host-facing Web and mailbox fixtures require a separate Docker Desktop bridge");
}
if (!macosProductGate.includes("down --volumes --remove-orphans --rmi local") || !macosProductGate.includes('reference=${project}-*') || macosProductGate.includes("docker system prune")) {
  fail("the macOS product gate must remove only its project-scoped build images on success and partial failure");
}
const stableGateImages = local.match(/^    image: lites-macos-gate\/[a-z0-9-]+:engineering$/gm) ?? [];
const buildDefinitions = local.match(/^    build:$/gm) ?? [];
if (stableGateImages.length !== 18 || stableGateImages.length !== buildDefinitions.length || new Set(stableGateImages).size !== stableGateImages.length) {
  fail(`all ${buildDefinitions.length} local build services must reuse one stable, unique lites-macos-gate image tag; found ${stableGateImages.length}`);
}

const streamLimit = 'NATS_STREAM_MAX_BYTES: "1073741824"';
const streamLimitCount = local.split(streamLimit).length - 1;
if (streamLimitCount !== 2) {
  fail(`expected the two JetStream creators to use the 1 GiB local limit, found ${streamLimitCount}`);
}

if (!local.includes("TRUSTED_PROXY_CIDRS: 192.0.2.0/24") || /TRUSTED_PROXY_CIDRS:.*(?:10\.0\.0\.0|172\.16\.0\.0|192\.168\.0\.0)/.test(local)) {
  fail("the local Next rewrite must not be trusted as an authenticated edge proxy");
}
if (!local.includes("LITES_LOCAL_PAYLOAD_KEY_SEED_FILE: /run/lites-local/payload-key-seed")) {
  fail("the engineering-only tenant payload key seed is not mounted into local services");
}
if (!local.includes("  realtime-gateway:\n") || !local.includes("REALTIME_UPSTREAM_URL: https://realtime-gateway:8443") || !local.includes("REALTIME_UPSTREAM_TLS_SERVER_NAME: realtime-gateway")) {
  fail("the local Gateway must route realtime traffic to the authenticated realtime service");
}
if (local.includes("REALTIME_UPSTREAM_URL: https://product-service:8443")) {
  fail("realtime traffic must not be sent to a service with a different trusted-context audience");
}
const upstreams = [
  ["BEHAVIOR", "behavior-control-plane"],
  ["AGENT", "agent-control-plane"],
  ["CONTRACT", "contract-service"],
];
for (const [name, service] of upstreams) {
  if (!local.includes(`  ${service}:\n`) || !local.includes(`${name}_UPSTREAM_URL: https://${service}:8443`) || !local.includes(`${name}_UPSTREAM_TLS_SERVER_NAME: ${service}`)) {
    fail(`the local Gateway must route ${name.toLowerCase()} traffic to the authenticated ${service}`);
  }
  if (local.includes(`${name}_UPSTREAM_URL: https://product-service:8443`) || local.includes(`${name}_UPSTREAM_TLS_SERVER_NAME: product-service`)) {
    fail(`${name.toLowerCase()} traffic must not be sent to product-service`);
  }
}
for (const service of ["loki", "otel-collector"]) {
  const pattern = new RegExp(`  ${service}:\\n    profiles: \\[linux-observability\\]`);
  if (!pattern.test(local)) fail(`${service} must be an explicit optional Linux observability profile in the macOS overlay`);
}
if (!local.includes("loki: {condition: service_started, required: false}")) {
  fail("Grafana must not make the optional Linux Loki binary a macOS product-gate dependency");
}
const localArtifacts = await readFile(resolve(root, "cmd/lites-local-artifacts/main.go"), "utf8");
if (!localArtifacts.includes('"payload-key-seed":') || !localArtifacts.includes("secret()")) {
  fail("the local artifact generator does not create an ephemeral payload key seed");
}
if (!local.includes("MINIO_KMS_SECRET_KEY_FILE: /run/lites-local/minio-kms-secret-key") || !localArtifacts.includes('w.bytes("minio-kms-secret-key"')) {
  fail("the local MinIO service must receive a short-lived static KMS key for real SSE-S3 writes");
}
for (const service of ["identity-service", "identity-import-worker", "identity-mail-worker", "product-service", "product-worker", "agent-control-plane", "behavior-control-plane", "contract-service", "agent-worker"]) {
  const source = await readFile(resolve(root, `cmd/${service}/main.go`), "utf8");
  if (!source.includes("ProviderForEnvironment(") || !source.includes('os.Getenv("LITES_LOCAL_PAYLOAD_KEY_SEED_FILE")')) {
    fail(`${service} does not explicitly select the environment-scoped payload key provider`);
  }
}
for (const artifact of ["agent-control-bundle.json", "behavior-bundle.json", "behavior-signing-keyring.json", "contract-bundle.json", "database-url-behavior"]) {
  if (!localArtifacts.includes(`"${artifact}"`)) fail(`the local artifact generator is missing ${artifact}`);
}
const behaviorAdapter = await readFile(resolve(root, "internal/localtestadapter/behavior.go"), "utf8");
const cursorReservation = behaviorAdapter.indexOf("INSERT INTO agent.event_cursors");
const eventInsertion = behaviorAdapter.indexOf("INSERT INTO agent.events");
if (cursorReservation < 0 || eventInsertion < 0 || cursorReservation > eventInsertion || !behaviorAdapter.includes("last_seq=agent.event_cursors.last_seq+$3")) {
  fail("the local behavior adapter must atomically reserve cursor sequence before inserting events");
}
for (const service of ["outbox-publisher", "agent-scheduler"]) {
  const lines = local.split("\n");
  const start = lines.findIndex((line) => line === `  ${service}:`);
  const end = lines.findIndex((line, index) => index > start && /^  [a-z0-9-]+:$/.test(line));
  const block = lines.slice(start, end === -1 ? undefined : end).join("\n");
  if (start === -1 || !block.includes(streamLimit)) {
    fail(`${service} is missing the Docker Desktop JetStream limit`);
  }
}

if (!process.exitCode) console.log("local compose contract passed");
