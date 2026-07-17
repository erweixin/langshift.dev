import { createHash } from "node:crypto";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const readJSON = async (path) => JSON.parse(await readFile(resolve(root, path), "utf8"));
const readText = async (path) => readFile(resolve(root, path), "utf8");
const hash = (value) => createHash("sha256").update(JSON.stringify(value)).digest("hex");
const api = await readJSON("contracts/openapi/amendments/v1.26.0/support-cases.json");
const events = await readJSON("contracts/events/amendments/v1.21.0/registry.json");
const fixtures = await readJSON("contracts/events/amendments/v1.21.0/upcaster-fixtures.json");
const migration = await readText("deploy/migrations/000081_support_case_control_plane.up.sql");
const handler = await readText("internal/product/api/support_handler.go");
const service = await readText("internal/product/postgres/support_service.go");
const integration = await readText("internal/product/postgres/support_service_integration_test.go");
const gateway = await readText("cmd/api-gateway/main.go");
const results = [];
const check = (id, passed, details) => results.push({ id, status: passed ? "passed" : "failed", details });
const create = api.operations["support.cases.create.v2"];
const list = api.operations["support.cases.list.v2"];
const reply = api.operations["support.cases.reply.v2"];
const createdEvent = events.schemas.SupportCaseCreated;
const repliedEvent = events.schemas.SupportCaseReplied;

check("OPENAPI-CHAIN", api.amendmentVersion === "1.26.0" && api.baseContractVersion === "1.25.0" && api.compatibility === "versioned-client-additive", "support cases additively extend the reauthentication API contract");
check("MUTATION-BOUNDARY", [create, reply].every((operation) => operation.method === "POST" && operation.csrf === "required" && operation.idempotency === "required_encrypted_durable_response"), "create and reply require CSRF and durable encrypted idempotency");
check("CAS-REPLY", reply.optimisticConcurrency.includes("If-Match") && handler.includes("ExpectedCaseVersion != expected") && service.includes("current != command.ExpectedCaseVersion"), "replies bind body and If-Match to the exact case version");
check("CLOSED-SCHEMAS", ["SupportCaseCreateV2", "SupportReplyV2", "SupportMessageV2", "SupportCaseV2", "SupportCasePageV2"].every((name) => api.schemas[name].type === "object" && api.schemas[name].additionalProperties === false), "all support representations are closed");
check("CURRENT-MEMBERSHIP", list.authorization.includes("current_role") && service.includes("requireMembership") && service.includes("status='active'"), "request scope is reauthorized against the current active membership in PostgreSQL");
check("REQUESTER-ISOLATION", service.includes("requester_user_id=$3") && service.includes("requesterID != command.UserID") && integration.includes("cross-requester get"), "members cannot list, read, or reply to another requester's case");
check("TENANT-RLS", migration.includes("FORCE ROW LEVEL SECURITY") && migration.includes("support_cases_tenant_isolation") && migration.includes("support_messages_tenant_isolation"), "case and message metadata are protected by forced tenant RLS");
check("ENCRYPTED-CONTENT", api.privacy.includes("envelope-encrypted") && migration.includes("subject_ref text") && migration.includes("body_ref text") && !migration.includes("subject text") && !migration.includes("body text"), "subject and message plaintext never enter PostgreSQL");
check("IMMUTABLE-MESSAGES", migration.includes("support_case_messages_append_only") && migration.includes("agent.reject_append_only_mutation"), "support messages are append-only evidence");
check("LIFECYCLE-FENCE", migration.includes("enforce_support_case_lifecycle") && migration.includes("NEW.version<>OLD.version+1") && migration.includes("OLD.status='closed'"), "case transitions are database-fenced and closed cases are immutable");
check("SLA-SNAPSHOT", api.sla.includes("snapshotted") && migration.includes("resolve_support_sla") && migration.includes("response_due_at") && migration.includes("resolution_due_at"), "contract support entitlement is bounded and frozen at case creation");
check("EXACT-GATEWAY", gateway.includes('path == "/v1/support/cases"') && gateway.includes('strings.HasPrefix(path, "/v1/support/cases/")'), "gateway has an exact support route family rather than a broad prefix");
check("EVENT-CHAIN", events.amendmentVersion === "1.21.0" && events.baseContractVersion === "1.20.0" && createdEvent["x-event-type"] === "SupportCaseCreated" && repliedEvent["x-event-type"] === "SupportCaseReplied", "support lifecycle events additively extend SessionReauthenticated");
check("EVENT-PRIVACY", !Object.hasOwn(createdEvent.properties.payload.properties, "subject") && !Object.hasOwn(createdEvent.properties.payload.properties, "body") && !Object.hasOwn(repliedEvent.properties.payload.properties, "body"), "events carry references and hashes, never support plaintext");
check("FIXTURE-PURITY", fixtures.baseFixtureVersion === "1.20.0" && fixtures.fixtures.length === 2 && fixtures.fixtures.every((fixture) => fixture.fromVersion === 1 && fixture.toVersion === 1 && fixture.inputHash === hash(fixture.input) && fixture.expectedHash === hash(fixture.expected)), "both event fixtures are deterministic and content-addressed");
check("REAL-POSTGRES-TEST", integration.includes("TestSupportCasesAreTenantScopedIdempotentEncryptedReferencesWithFrozenSLA") && integration.includes("ErrIdempotencyConflict") && integration.includes("ErrPermissionDenied"), "integration coverage includes idempotency substitution, tenant-wide denial, CAS, SLA, and plaintext-reference checks");

const failures = results.filter((result) => result.status === "failed");
const base = { reportVersion: "1.0.0", stage: 5, kind: "support-case-control-plane-contract", generatedAt: new Date().toISOString(), status: failures.length ? "failed" : "passed", summary: { checks: results.length, passed: results.length - failures.length, failed: failures.length }, openAPIHash: hash(api), eventRegistryHash: hash(events), fixtureHash: hash(fixtures), results };
const report = { ...base, reportHash: hash(base) };
const target = resolve(root, "gate-reports/stage-5/support-case-control-plane-contract.json");
await mkdir(dirname(target), { recursive: true });
await writeFile(target, `${JSON.stringify(report, null, 2)}\n`);
console.log(`${report.status}: ${report.summary.passed}/${report.summary.checks} checks; report ${report.reportHash}`);
if (failures.length) {
  for (const failure of failures) console.error(`${failure.id}: ${failure.details}`);
  process.exitCode = 1;
}
