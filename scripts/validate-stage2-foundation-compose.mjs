import { readFile } from "node:fs/promises";

const input = process.argv[2];
if (!input) throw new Error("usage: validate-stage2-foundation-compose.mjs RESOLVED_COMPOSE_JSON");
const model = JSON.parse(await readFile(input, "utf8"));
const required = ["postgres", "nats", "valkey", "minio", "vault", "tempo", "loki", "otel-collector", "prometheus", "grafana", "probe", "nats-box", "minio-client"];
const services = model.services ?? {};
const failures = [];
const assert = (condition, message) => { if (!condition) failures.push(message); };

for (const name of required) {
  const service = services[name];
  assert(Boolean(service), `missing service ${name}`);
  if (!service) continue;
  assert(/^[^@\s]+:[^@\s]+@sha256:[0-9a-f]{64}$/.test(service.image ?? ""), `${name} image is not tag+digest pinned`);
  assert(!service.image.includes(":latest@"), `${name} image uses latest`);
  assert(!service.ports && !service.network_mode, `${name} exposes a host/network namespace`);
  assert((service.networks ?? {}).foundation !== undefined, `${name} is outside the internal foundation network`);
  assert((service.security_opt ?? []).includes("no-new-privileges:true"), `${name} does not deny privilege escalation`);
}
assert(model.networks?.foundation?.internal === true, "foundation network is not internal");
for (const name of ["probe", "nats-box", "minio-client"]) {
  assert((services[name]?.profiles ?? []).includes("tools"), `${name} must remain an explicit tool profile`);
}
for (const name of ["postgres_data", "nats_data", "valkey_data", "minio_data", "tempo_data", "loki_data", "prometheus_data", "grafana_data"]) {
  assert(Boolean(model.volumes?.[name]), `missing durable volume ${name}`);
}
for (const name of ["postgres_password", "minio_root_user", "minio_root_password", "grafana_admin_password"]) {
  assert(Boolean(model.secrets?.[name]), `missing file-backed secret ${name}`);
}
assert((services.vault?.command ?? []).includes("-dev"), "smoke Vault must be unmistakably marked dev-only");
assert((services.nats?.command ?? []).includes("--jetstream"), "NATS JetStream is not enabled");
assert((services.valkey?.command ?? []).includes("--appendonly"), "Valkey persistence is not enabled");

if (failures.length) {
  for (const failure of failures) console.error(failure);
  process.exit(1);
}
console.log(`foundation compose contract: ${required.length}/${required.length} services validated`);
