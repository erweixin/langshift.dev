import { createHash } from "node:crypto";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const readJSON = async (path) => JSON.parse(await readFile(resolve(root, path), "utf8"));
const readText = async (path) => readFile(resolve(root, path), "utf8");
const hash = (value) => createHash("sha256").update(JSON.stringify(value)).digest("hex");
const api = await readJSON("contracts/openapi/amendments/v1.25.0/session-reauthentication.json");
const events = await readJSON("contracts/events/amendments/v1.20.0/registry.json");
const fixtures = await readJSON("contracts/events/amendments/v1.20.0/upcaster-fixtures.json");
const handler = await readText("internal/identity/api/session_handler.go");
const router = await readText("internal/identity/api/handler.go");
const service = await readText("internal/identity/postgres/reauthentication_service.go");
const production = await readText("cmd/identity-service/main.go");
const integration = await readText("internal/identity/postgres/auth_service_integration_test.go");
const results = [];
const check = (id, passed, details) => results.push({ id, status: passed ? "passed" : "failed", details });
const operation = api.operations["auth.reauthentication.create.v1"];
const request = api.schemas.SessionReauthenticationRequestV1;
const response = api.schemas.SessionReauthenticationResourceV1;
const event = events.schemas.SessionReauthenticated;
const fixture = fixtures.fixtures[0];

check("OPENAPI-CHAIN", api.amendmentVersion === "1.25.0" && api.baseContractVersion === "1.24.0" && api.compatibility === "versioned-client-additive", "reauthentication additively extends the entitlement API contract");
check("OPERATION-BOUNDARY", operation.method === "POST" && operation.path === "/v1/auth/reauthentication" && operation.csrf === "required" && operation.idempotency === "required_encrypted_durable_response", "current-session reauthentication requires POST, CSRF and durable encrypted idempotency");
check("RATE-LIMIT", operation.rateLimit.includes("10 attempts") && router.includes("reauthenticationSessionLimit") && router.includes("Capacity: 10") && router.includes("Window: 15 * time.Minute"), "the signed session plus client network subject is rate limited");
check("CLOSED-SCHEMAS", [request, response].every((schema) => schema.type === "object" && schema.additionalProperties === false && schema.required.length > 0), "request and response representations are closed");
check("PASSWORD-WRITE-ONLY", request.properties.password.writeOnly === true && !Object.hasOwn(response.properties, "password") && !Object.hasOwn(event.properties.payload.properties, "password"), "password exists only in the write-only request schema and never in response or event data");
check("TRUSTED-SESSION", !["user_id", "tenant_id", "membership_id", "session_id"].some((field) => Object.hasOwn(request.properties, field)) && handler.includes("authenticatedMetadata(writer, request, true)"), "identity and current session come only from signed gateway context");
check("EXACT-FIVE-MINUTES", operation.safety.includes("at most five minutes") && production.includes("ReauthenticationTTL: 5 * time.Minute") && service.includes("now.Add(service.ReauthenticationTTL)"), "the production validity window is exactly five minutes and response derives from the same configured TTL");
check("CREDENTIAL-CAS", service.includes("credentialVersion != lookup.CredentialVersion") && service.includes("sessionVersion != lookup.SessionVersion") && service.includes("FOR UPDATE") && service.includes("FOR SHARE"), "session and password credential versions are checked under database locks");
check("PASSWORD-DIGEST-PRIVACY", service.includes("idempotency.RequestDigest(canonical, service.RequestDigestPepper)") && !service.includes("Password: command.Password, \"event"), "password-bearing idempotency requests use a keyed digest and never feed the event payload");
check("SUCCESS-AND-FAILURE-AUDIT", service.includes("session_reauthenticated") && service.includes("session_reauthentication_failed") && integration.includes("reauthenticationSuccessAudits != 1") && integration.includes("reauthenticationFailureAudits != 1"), "successful and failed password proofs are separately audited and integration-tested");
check("EVENT-CHAIN", events.amendmentVersion === "1.20.0" && events.baseContractVersion === "1.19.0" && event["x-event-type"] === "SessionReauthenticated", "SessionReauthenticated is an additive identity event");
check("EVENT-CLOSED", event.additionalProperties === false && event.properties.payload.additionalProperties === false && event.properties.payload.properties.method.const === "password", "event envelope and payload are closed and identify only the proof method");
check("FIXTURE-PURITY", fixtures.baseFixtureVersion === "1.19.0" && fixture.eventType === "SessionReauthenticated" && fixture.fromVersion === 1 && fixture.toVersion === 1 && fixture.inputHash === hash(fixture.input) && fixture.expectedHash === hash(fixture.expected), "the event has a deterministic verified identity fixture");

const failures = results.filter((result) => result.status === "failed");
const base = { reportVersion: "1.0.0", stage: 5, kind: "session-reauthentication-contract", generatedAt: new Date().toISOString(), status: failures.length ? "failed" : "passed", summary: { checks: results.length, passed: results.length - failures.length, failed: failures.length }, openAPIHash: hash(api), eventRegistryHash: hash(events), fixtureHash: hash(fixtures), results };
const report = { ...base, reportHash: hash(base) };
const target = resolve(root, process.env.LITES_GATE_REPORT_ROOT ?? "gate-reports", "stage-5/session-reauthentication-contract.json");
await mkdir(dirname(target), { recursive: true });
await writeFile(target, `${JSON.stringify(report, null, 2)}\n`);
console.log(`${report.status}: ${report.summary.passed}/${report.summary.checks} checks; report ${report.reportHash}`);
if (failures.length) {
  for (const failure of failures) console.error(`${failure.id}: ${failure.details}`);
  process.exitCode = 1;
}
