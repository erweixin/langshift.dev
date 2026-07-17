import { createHash } from "node:crypto";
import { mkdir, writeFile } from "node:fs/promises";
import { resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const sha256 = value => createHash("sha256").update(JSON.stringify(value)).digest("hex");
const hash = "a".repeat(64);
const hashB = "b".repeat(64);

const payloads = {
  ProjectWorkspaceHeadAdvanced: { subject_id: "project:1", subject_version: 6, project_id: "project:1", binding_id: "binding:1", binding_version: 2, previous_head_revision: "git:abc", head_revision: "git:def", binding_manifest_hash: hashB },
  ProjectCompleted: { subject_id: "project:1", subject_version: 8, project_id: "project:1", workspace_revision: "git:def", completion_manifest_hash: hash, reflection_hash: hashB, completed_at: "2026-07-16T00:00:00.000Z" },
};

const schemaFor = (value, key = "") => {
  if (typeof value === "boolean") return { type: "boolean" };
  if (typeof value === "number") return { type: "integer", minimum: 1 };
  if (key.endsWith("_hash")) return { type: "string", pattern: "^[0-9a-f]{64}$" };
  if (key.endsWith("_at")) return { type: "string", format: "date-time" };
  return { type: "string", minLength: 1 };
};

const eventSchema = (eventType, payload) => ({
  $schema: "https://json-schema.org/draft/2020-12/schema",
  $id: `https://lites.dev/events/${eventType}/1.json`,
  title: `${eventType} v1`, type: "object", additionalProperties: false,
  required: ["event_id", "tenant_id", "user_id", "aggregate_id", "aggregate_version", "occurred_at", "payload"],
  properties: {
    event_id: { type: "string" }, tenant_id: { type: "string" }, user_id: { type: "string" },
    aggregate_id: { type: "string" }, aggregate_version: { type: "integer", minimum: 1 },
    occurred_at: { type: "string", format: "date-time" }, causation_id: { type: "string" }, correlation_id: { type: "string" },
    payload: { type: "object", additionalProperties: false, required: Object.keys(payload), properties: Object.fromEntries(Object.entries(payload).map(([key, value]) => [key, schemaFor(value, key)])) },
  },
  "x-event-type": eventType, "x-event-schema-version": 1, "x-compatibility": "additive", "x-owner": "product-platform",
});

const schemas = Object.fromEntries(Object.entries(payloads).map(([name, payload]) => [name, eventSchema(name, payload)]));

const registry = { amendmentVersion: "1.16.0", baseContractVersion: "1.15.0", compatibility: "additive", schemas };
const fixtures = {
  fixtureVersion: "1.16.0", baseFixtureVersion: "1.15.0",
  purityRule: "Upcasters are deterministic pure functions with no network, clock or current-config access.",
  fixtures: Object.entries(payloads).map(([eventType, input]) => ({ fixtureId: `${eventType}-v1-canonical`, eventType, fromVersion: 1, toVersion: 1, input, expected: input, inputHash: sha256(input), expectedHash: sha256(input) })),
};

const directory = resolve(root, "contracts/events/amendments/v1.16.0");
await mkdir(directory, { recursive: true });
await writeFile(resolve(directory, "registry.json"), `${JSON.stringify(registry, null, 2)}\n`);
await writeFile(resolve(directory, "upcaster-fixtures.json"), `${JSON.stringify(fixtures, null, 2)}\n`);
console.log(`wrote ${Object.keys(schemas).length} closed v1.16 Create event schemas and fixtures`);
