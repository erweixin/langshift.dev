import { createHash } from "node:crypto";
import { mkdir, writeFile } from "node:fs/promises";
import { resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const sha256 = value => createHash("sha256").update(JSON.stringify(value)).digest("hex");
const hash = "a".repeat(64);
const hashB = "b".repeat(64);
const at = "2026-07-16T00:00:00.000Z";

const schedule = {
  schema_version: 1,
  dst_policy: "next_valid_wall_time_earliest_duplicate",
  timezone: "Asia/Shanghai",
  local_time: "09:00",
  weekdays: [1, 2, 3, 4, 5],
  channel: "in_app",
};
const coachPreferences = {
  schema_version: 1,
  difficulty: "harder",
  available_minutes: 45,
  tone: "socratic",
  explanation_depth: "deep",
};

// These are the exact durable facts emitted by the execution and product
// services. Object insertion order is intentionally stable because fixtures
// bind the canonical JSON representation used by the existing contract chain.
const payloads = {
  ApprovalPreviewGroupAuthorized: { approval_id: "approval:1", proposal_hash: hash, state: "execution" },
  CoachContextSnapshotted: { subject_id: "coach-context:1", subject_version: 1, coach_context_id: "coach-context:1", conversation_id: "conversation:1", mission_id: "mission:1", focus_version: 3, manifest_hash: hash, payload_hash: hashB },
  DailyTaskCompleted: { subject_id: "daily-task:1", subject_version: 5, review_id: "review:1", evidence_id: "evidence:1" },
  DailyTaskUpdated: { subject_id: "daily-task:1", subject_version: 2, action: "reschedule", reschedule_for: "2026-07-18" },
  DirectApprovalGroupAuthorized: { approval_id: "approval:2", proposal_hash: hash, state: "execution" },
  PreferencesInitialized: { subject_id: "preferences:1", subject_version: 1, locale: "zh-CN", timezone: "Asia/Shanghai", coach_preferences: coachPreferences },
  PreferencesUpdated: { subject_id: "preferences:1", subject_version: 2, locale: "zh-CN", timezone: "Asia/Shanghai", coach_preferences: coachPreferences },
  ReminderDeliveryFailed: { subject_id: "delivery:1", subject_version: 2, schedule_id: "reminder:1", delivery_id: "delivery:1", delivery_key: hash, occurrence_at: at, attempt: 1, error_code: "provider_unavailable" },
  ReminderDeliveryScheduled: { subject_id: "delivery:1", subject_version: 1, schedule_id: "reminder:1", delivery_id: "delivery:1", delivery_key: hash, occurrence_at: at, channel: "in_app" },
  ReminderOccurrenceScheduled: { subject_id: "reminder:1", subject_version: 2, delivery_id: "delivery:1", delivery_key: hash, occurrence_at: at, next_occurrence_at: "2026-07-17T00:00:00.000Z" },
  ReminderScheduleCancelled: { subject_id: "reminder:1", subject_version: 5, action: "cancel", previous_status: "paused", status: "cancelled", schedule, next_occurrence_at: null },
  ReminderScheduleCreated: { subject_id: "reminder:1", subject_version: 1, schedule_kind: "daily_practice", status: "active", schedule, next_occurrence_at: at },
  ReminderSchedulePaused: { subject_id: "reminder:1", subject_version: 3, action: "pause", previous_status: "active", status: "paused", schedule, next_occurrence_at: null },
  ReminderScheduleReplaced: { subject_id: "reminder:1", subject_version: 2, action: "replace", previous_status: "active", status: "active", schedule, next_occurrence_at: at },
  ReminderScheduleResumed: { subject_id: "reminder:1", subject_version: 4, action: "resume", previous_status: "paused", status: "active", schedule, next_occurrence_at: at },
  RouteRevisionFailed: { subject_id: "route-revision:1", subject_version: 2, route_revision_id: "route-revision:1", planner_run_id: "run:1", reason: "planner_output_invalid" },
  RouteRevisionProposed: { subject_id: "route-revision:1", subject_version: 2, route_revision_id: "route-revision:1", planner_run_id: "run:1", route_payload_hash: hash },
  RouteRevisionStale: { subject_id: "route-revision:2", subject_version: 2, route_revision_id: "route-revision:2", planner_run_id: "run:2", route_payload_hash: hash },
  RunMessageFinalized: { message_id: "message:1", run_id: "run:1", content_hash: hash },
  RunWaitingApproval: { run_id: "run:1", step_id: "step:1", tool_count: 2 },
  RunWaitingTool: { approval_id: "approval:1", proposal_hash: hash, state: "waiting_tool" },
  SubmissionCreated: { submission_id: "submission:1", daily_task_id: "daily-task:1", task_version: 3, submission_revision: 1, submission_kind: "code", content_hash: hash, understanding_hash: hashB },
  SubmissionReviewCompleted: { subject_id: "daily-task:1", subject_version: 4, review_id: "review:1", submission_id: "submission:1", verdict: "pass" },
};

const stringSchema = key => {
  if (key.endsWith("_hash") || key === "delivery_key") return { type: "string", pattern: "^[0-9a-f]{64}$" };
  if (key.endsWith("_at") || key === "next_occurrence_at") return { type: "string", format: "date-time" };
  if (key === "reschedule_for") return { type: "string", format: "date" };
  if (key === "timezone") return { type: "string", minLength: 1, maxLength: 128 };
  if (key === "local_time") return { type: "string", pattern: "^(?:[01][0-9]|2[0-3]):[0-5][0-9]$" };
  return { type: "string", minLength: 1 };
};

const schemaFor = (value, key = "") => {
  if (value === null) return { type: ["string", "null"], format: "date-time" };
  if (typeof value === "string") return stringSchema(key);
  if (typeof value === "number") return { type: "integer", minimum: key === "schema_version" ? 1 : 1 };
  if (Array.isArray(value)) return { type: "array", minItems: 1, maxItems: 7, uniqueItems: true, items: { type: "integer", minimum: 1, maximum: 7 } };
  const properties = Object.fromEntries(Object.entries(value).map(([name, item]) => [name, schemaFor(item, name)]));
  return { type: "object", additionalProperties: false, required: Object.keys(value), properties };
};

const enumAt = (schema, field, values) => {
  schema.properties.payload.properties[field] = { enum: values };
};

const eventSchema = (eventType, payload) => ({
  $schema: "https://json-schema.org/draft/2020-12/schema",
  $id: `https://lites.dev/events/${eventType}/1.json`,
  title: `${eventType} v1`,
  type: "object",
  additionalProperties: false,
  required: ["event_id", "tenant_id", "user_id", "aggregate_id", "aggregate_version", "occurred_at", "payload"],
  properties: {
    event_id: { type: "string" }, tenant_id: { type: "string" }, user_id: { type: "string" },
    aggregate_id: { type: "string" }, aggregate_version: { type: "integer", minimum: 1 },
    occurred_at: { type: "string", format: "date-time" }, causation_id: { type: "string" }, correlation_id: { type: "string" },
    payload: schemaFor(payload),
  },
  "x-event-type": eventType,
  "x-event-schema-version": 1,
  "x-compatibility": "additive",
  "x-owner": eventType.startsWith("Reminder") || eventType.startsWith("Daily") || eventType.startsWith("Submission") || eventType.startsWith("Preferences") || eventType.startsWith("Route") ? "product-platform" : "agent-platform",
});

const schemas = Object.fromEntries(Object.entries(payloads).map(([eventType, payload]) => [eventType, eventSchema(eventType, payload)]));
enumAt(schemas.DailyTaskUpdated, "action", ["start", "skip", "reschedule"]);
schemas.DailyTaskUpdated.properties.payload.properties.reschedule_for = { type: ["string", "null"], format: "date" };
for (const eventType of ["ReminderScheduleCreated", "ReminderScheduleReplaced", "ReminderSchedulePaused", "ReminderScheduleResumed", "ReminderScheduleCancelled"]) {
  const scheduleSchema = schemas[eventType].properties.payload.properties.schedule;
  scheduleSchema.properties.schema_version = { type: "integer", const: 1 };
  scheduleSchema.properties.dst_policy = { const: "next_valid_wall_time_earliest_duplicate" };
  scheduleSchema.properties.channel = { enum: ["email", "push", "in_app"] };
}
enumAt(schemas.ReminderDeliveryScheduled, "channel", ["email", "push", "in_app"]);
for (const eventType of ["PreferencesInitialized", "PreferencesUpdated"]) {
  enumAt(schemas[eventType], "locale", ["en", "zh-CN"]);
  const coach = schemas[eventType].properties.payload.properties.coach_preferences;
  coach.properties.schema_version = { type: "integer", const: 1 };
  coach.properties.difficulty = { enum: ["easier", "standard", "harder"] };
  coach.properties.available_minutes = { type: "integer", minimum: 5, maximum: 480 };
  coach.properties.tone = { enum: ["encouraging", "direct", "socratic"] };
  coach.properties.explanation_depth = { enum: ["concise", "balanced", "deep"] };
}
enumAt(schemas.SubmissionCreated, "submission_kind", ["code", "writing", "design"]);
enumAt(schemas.SubmissionReviewCompleted, "verdict", ["pass", "needs_revision"]);
enumAt(schemas.RunWaitingTool, "state", ["waiting_tool"]);
for (const eventType of ["ApprovalPreviewGroupAuthorized", "DirectApprovalGroupAuthorized"]) enumAt(schemas[eventType], "state", ["execution"]);

const registry = { amendmentVersion: "1.15.0", baseContractVersion: "1.14.0", compatibility: "additive", schemas };
const fixtures = {
  fixtureVersion: "1.15.0",
  baseFixtureVersion: "1.14.0",
  purityRule: "Upcasters are deterministic pure functions with no network, clock or current-config access.",
  fixtures: Object.entries(payloads).map(([eventType, input]) => ({
    fixtureId: `${eventType}-v1-canonical`, eventType, fromVersion: 1, toVersion: 1,
    input, expected: input, inputHash: sha256(input), expectedHash: sha256(input),
  })),
};

const directory = resolve(root, "contracts/events/amendments/v1.15.0");
await mkdir(directory, { recursive: true });
await writeFile(resolve(directory, "registry.json"), `${JSON.stringify(registry, null, 2)}\n`);
await writeFile(resolve(directory, "upcaster-fixtures.json"), `${JSON.stringify(fixtures, null, 2)}\n`);
console.log(`wrote ${Object.keys(schemas).length} closed v1.15 event schemas and fixtures`);
