import { createHash } from "node:crypto";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const readJSON = async (path) => JSON.parse(await readFile(resolve(root, path), "utf8"));
const readText = async (path) => readFile(resolve(root, path), "utf8");
const hash = (value) => createHash("sha256").update(JSON.stringify(value)).digest("hex");
const contract = await readJSON("contracts/openapi/amendments/v1.23.0/contract-control-plane.json");
const events = await readJSON("contracts/events/amendments/v1.18.0/registry.json");
const fixtures = await readJSON("contracts/events/amendments/v1.18.0/upcaster-fixtures.json");
const entitlementContract = await readJSON("contracts/openapi/amendments/v1.24.0/entitlement-control-plane.json");
const entitlementEvents = await readJSON("contracts/events/amendments/v1.19.0/registry.json");
const entitlementFixtures = await readJSON("contracts/events/amendments/v1.19.0/upcaster-fixtures.json");
const handler = await readText("internal/contracts/api/handler.go");
const proposals = await readText("internal/contracts/postgres/control_proposals.go");
const migration77 = await readText("deploy/migrations/000077_contract_control_plane.up.sql");
const migration78 = await readText("deploy/migrations/000078_accounting_adjustment_control_plane.up.sql");
const migration79 = await readText("deploy/migrations/000079_admin_access_audit.up.sql");
const migration80 = await readText("deploy/migrations/000080_entitlement_control_plane.up.sql");
const entitlements = await readText("internal/contracts/postgres/control_entitlements.go");
const results = [];
const check = (id, passed, details) => results.push({ id, status: passed ? "passed" : "failed", details });

const operationIDs = [
  "admin.contracts.propose.v2",
  "admin.contracts.decide.v2",
  "admin.usage.read.v2",
  "admin.usage.adjustments.propose.v2",
  "admin.usage.adjustments.decide.v2",
  "admin.audit.read.v2"
];
const mutationIDs = operationIDs.filter((id) => !id.endsWith(".read.v2"));
const eventTypes = [
  "ContractProposalCreated",
  "ContractApprovalGranted",
  "ContractApprovalRejected",
  "ContractProposalExecuted",
  "AccountingAdjustmentProposed",
  "AccountingAdjustmentApprovalGranted",
  "AccountingAdjustmentApprovalRejected",
  "AccountingAdjustmentExecuted"
];
const entitlementOperationIDs = ["admin.entitlements.propose.v2", "admin.entitlements.decide.v2"];
const entitlementEventTypes = ["EntitlementProposed", "EntitlementApprovalGranted", "EntitlementApprovalRejected", "EntitlementProposalExecuted"];
const entitlementKeys = ["programs", "cohorts", "role_packs", "aggregate_analytics", "audit_export", "private_delivery", "commercial_license", "support_tier"];

check("OPENAPI-VERSION-CHAIN", contract.amendmentVersion === "1.23.0" && contract.baseContractVersion === "1.22.0" && contract.compatibility === "versioned-client-additive", "contract control plane additively extends the frozen enterprise API contract");
check("OPENAPI-OPERATIONS", operationIDs.every((id) => contract.operations[id]) && Object.keys(contract.operations).length === operationIDs.length, "all six contract, accounting and audit control-plane operations are explicit");
check("MUTATION-BOUNDARY", mutationIDs.every((id) => contract.operations[id].csrf === "required" && contract.operations[id].idempotency === "required_encrypted_durable_response" && contract.operations[id].authorization.includes("reauthentication_within_5_minutes")), "every mutation requires CSRF, durable encrypted idempotency and five-minute reauthentication");
check("READ-BOUNDARY", contract.operations["admin.usage.read.v2"].method === "GET" && contract.operations["admin.usage.read.v2"].csrf === "not_applicable" && contract.operations["admin.usage.read.v2"].requiredHeaders?.includes("X-Audit-Reason"), "usage read is authenticated and requires an explicit audit reason");
check("STRICT-VENDOR-MEDIA", mutationIDs.every((id) => contract.operations[id].requestContentType.includes(".v2+json")) && operationIDs.every((id) => contract.operations[id].responseContentType.includes(".v2+json")), "all request and response representations use exact v2 vendor media types");
check("HANDLER-MEDIA-MATCH", ["contract-proposal.v2", "contract-decision.v2", "accounting-adjustment-proposal.v2", "accounting-adjustment-decision.v2", "admin-control-resource.v2", "usage-snapshot.v2", "admin-audit-page.v2"].every((media) => handler.includes(media)), "the HTTP handler implements every frozen vendor media type");
check("CLOSED-SCHEMAS", Object.values(contract.schemas).every((schema) => schema.type === "object" && schema.additionalProperties === false && schema.required?.length && schema.properties), "every public request and response schema is closed");
check("NO-CLIENT-IDENTITY", Object.values(contract.schemas).every((schema) => !["tenant_id", "user_id", "membership_id", "session_id"].some((field) => field in schema.properties) || schema === contract.schemas.UsageSnapshotV2), "mutation schemas cannot supply trusted tenant, user, membership or session identity");
check("CONTRACT-ACTIONS", JSON.stringify(contract.schemas.ContractProposalRequestV2.properties.action.enum) === JSON.stringify(["create", "renew", "suspend", "terminate"]) && contract.schemas.ContractProposalRequestV2.allOf.length === 4, "create, renew, suspend and terminate have explicit conditional term rules");
check("CONTRACT-LICENSES", JSON.stringify(contract.schemas.ContractProposalRequestV2.properties.license_kind.enum.slice(1)) === JSON.stringify(["enterprise_cloud", "private_cloud", "commercial_self_hosted"]), "commercial deployment license kinds are frozen");
check("DECISION-BINDING", [contract.schemas.ContractDecisionRequestV2, contract.schemas.AccountingAdjustmentDecisionRequestV2].every((schema) => schema.properties.expected_proposal_version.const === 1 && schema.properties.proposal_hash.pattern === "^[0-9a-f]{64}$"), "decisions bind the exact immutable proposal hash and proposal version");
check("HTTP-PRECONDITIONS", handler.includes("StatusPreconditionRequired") && handler.includes("StatusPreconditionFailed") && contract.operations["admin.contracts.decide.v2"].concurrency.includes("428") && contract.operations["admin.contracts.decide.v2"].concurrency.includes("412"), "missing and stale If-Match preconditions fail before service execution");
check("TWO-PERSON-CONTRACT", contract.operations["admin.contracts.decide.v2"].safety.includes("Exactly two distinct approvals") && migration77.includes("approval_event_ids") && migration77.includes("jsonb_array_length(approval_event_ids)=2"), "contract execution requires exactly two approval events");
check("TWO-PERSON-ADJUSTMENT", contract.operations["admin.usage.adjustments.decide.v2"].safety.includes("Exactly two distinct current approvers") && migration78.includes("approval_event_ids") && migration78.includes("jsonb_array_length(approval_event_ids)=2"), "accounting execution requires exactly two approval events");
check("DIRECT-MUTATION-FENCES", migration77.includes("contract mutation requires approved proposal") && migration78.includes("credit grant mutation requires an exact approved adjustment"), "database triggers reject contract and granted-unit changes outside approved proposal execution");
check("ADMIN-READ-AUDIT", handler.includes("X-Audit-Reason") && migration79.includes("admin_access_audit_append_only") && migration79.includes("actor_user_id") && migration79.includes("membership_id") && migration79.includes("session_id") && migration79.includes("reason_hash"), "admin usage reads commit complete append-only actor, session and reason audit evidence");
check("AUDIT-EXPORT", contract.operations["admin.audit.read.v2"].requiredHeaders?.includes("X-Audit-Reason") && contract.operations["admin.audit.read.v2"].concurrency.includes("fifteen-minute expiry") && contract.schemas.AdminAuditPageV2.properties.items.maxItems === 200 && !["reason", "approval_event_ids", "session_id", "membership_id"].some((field) => field in contract.schemas.AdminAuditRecordV2.properties), "audit export is bounded, cursor-signed, self-auditing and omits sensitive approval details");
check("EVENT-VERSION-CHAIN", events.amendmentVersion === "1.18.0" && events.baseContractVersion === "1.17.0" && events.compatibility === "additive", "event schemas additively extend ProgramUpdated v1.17");
check("EVENT-CATALOG", eventTypes.every((type) => events.schemas[type]?.["x-event-type"] === type) && Object.keys(events.schemas).length === eventTypes.length, "all eight lifecycle events are registered without reusing an incompatible frozen event name");
check("NO-FROZEN-NAME-COLLISION", proposals.includes('"ContractProposalCreated"') && !proposals.includes('"ContractProposed"'), "contract proposal emission uses its dedicated event type");
check("EVENT-CLOSED-ENVELOPES", Object.values(events.schemas).every((schema) => schema.additionalProperties === false && schema.properties.payload), "every event envelope and payload reference is closed");
check("EVENT-HASH-BINDING", eventTypes.every((type) => JSON.stringify(events.schemas[type]).includes("proposal_hash")), "every proposal, vote and execution event carries the immutable proposal hash");
check("EVENT-PERMISSION-SNAPSHOT", ["ContractApprovalGranted", "ContractApprovalRejected", "AccountingAdjustmentApprovalGranted", "AccountingAdjustmentApprovalRejected"].every((type) => JSON.stringify(events.schemas[type]).includes("permission_snapshot")), "approval events bind the exact permission snapshot");
check("EVENT-EXECUTION-QUORUM", events.schemas.ContractProposalExecuted.properties.payload.properties.approval_count.const === 2 && events.schemas.AccountingAdjustmentExecuted.properties.payload.properties.approval_count.const === 2, "execution events prove the exact two-person quorum");
check("EVENT-REASON-SEPARATION", ["ContractProposalCreated", "AccountingAdjustmentProposed"].every((type) => ["reason_ref", "reason_hash"].every((field) => JSON.stringify(events.schemas[type]).includes(field))) && !JSON.stringify(events).includes('"reason"'), "proposal events contain only encrypted reason references and plaintext hashes");
check("FIXTURE-CATALOG", eventTypes.every((type) => fixtures.fixtures.filter((fixture) => fixture.eventType === type).length === 1), "every event type has exactly one canonical v1 fixture");
check("FIXTURE-PURITY", fixtures.baseFixtureVersion === "1.17.0" && fixtures.fixtures.every((fixture) => fixture.fromVersion === 1 && fixture.toVersion === 1 && fixture.inputHash === hash(fixture.input) && fixture.expectedHash === hash(fixture.expected)), "all fixtures are deterministic identity upcasts with verified hashes");
check("ENTITLEMENT-OPENAPI-CHAIN", entitlementContract.amendmentVersion === "1.24.0" && entitlementContract.baseContractVersion === "1.23.0" && entitlementContract.compatibility === "versioned-client-additive", "entitlement operations additively extend the contract control-plane API");
check("ENTITLEMENT-OPERATIONS", entitlementOperationIDs.every((id) => entitlementContract.operations[id]) && Object.keys(entitlementContract.operations).length === 2, "proposal and approval-decision operations are both explicit");
check("ENTITLEMENT-BOUNDARY", entitlementOperationIDs.every((id) => entitlementContract.operations[id].csrf === "required" && entitlementContract.operations[id].idempotency === "required_encrypted_durable_response" && entitlementContract.operations[id].authorization.includes("reauthentication_within_5_minutes") && entitlementContract.operations[id].requiredHeaders.includes("If-Match")), "both mutations require signed identity, CSRF, durable idempotency, fresh reauthentication and CAS");
check("ENTITLEMENT-MEDIA", entitlementContract.operations["admin.entitlements.propose.v2"].requestContentType.includes("entitlement-proposal.v2") && entitlementContract.operations["admin.entitlements.decide.v2"].requestContentType.includes("entitlement-decision.v2") && ["entitlement-proposal.v2", "entitlement-decision.v2"].every((media) => handler.includes(media)), "the frozen vendor media types match the implementation");
check("ENTITLEMENT-CLOSED-SCHEMAS", Object.values(entitlementContract.schemas).every((schema) => schema.type === "object" && schema.additionalProperties === false && schema.required?.length && schema.properties), "every entitlement request and response schema is closed");
check("ENTITLEMENT-KEY-CATALOG", JSON.stringify(entitlementContract.schemas.EntitlementProposalRequestV2.properties.entitlement_key.enum) === JSON.stringify(entitlementKeys) && entitlementKeys.every((key) => migration80.includes(`'${key}'`)) && entitlementKeys.every((key) => entitlements.includes(`"${key}"`)), "API, database and service share one exact entitlement-key catalog");
check("ENTITLEMENT-DECISION-BINDING", entitlementContract.schemas.EntitlementDecisionRequestV2.properties.expected_proposal_version.const === 1 && entitlementContract.schemas.EntitlementDecisionRequestV2.properties.proposal_hash.pattern === "^[0-9a-f]{64}$" && ["target_contract_version", "target_entitlement_version"].every((field) => entitlementContract.schemas.EntitlementDecisionRequestV2.required.includes(field)), "decisions bind proposal, contract and entitlement versions plus immutable hash");
check("ENTITLEMENT-TWO-PERSON", entitlementContract.operations["admin.entitlements.decide.v2"].safety.includes("Exactly two distinct current approvers") && migration80.includes("approvals<>2") && migration80.includes("proposal.initiator_user_id=NEW.approver_user_id") && migration80.includes("approval_event_ids") && migration77.includes("jsonb_array_length(approval_event_ids)=2"), "execution requires exactly two non-initiator current approvals and preserves both event identifiers");
check("ENTITLEMENT-DIRECT-FENCE", migration80.includes("entitlement append requires exact approved proposal") && !migration80.includes("GRANT INSERT ON contracts.contract_entitlements TO lites_contract_service"), "the service role cannot directly append or mutate entitlements");
check("ENTITLEMENT-IMMUTABLE-VERSION", migration80.includes("target_entitlement_version") && migration80.includes("current_version<>proposal.target_entitlement_version") && migration80.includes("current_version+1"), "proposal and execution enforce a gap-free immutable entitlement version chain");
check("ENTITLEMENT-EVENT-CHAIN", entitlementEvents.amendmentVersion === "1.19.0" && entitlementEvents.baseContractVersion === "1.18.0" && entitlementEvents.compatibility === "additive", "entitlement events additively extend the contract lifecycle event catalog");
check("ENTITLEMENT-EVENT-CATALOG", entitlementEventTypes.every((type) => entitlementEvents.schemas[type]?.["x-event-type"] === type && entitlements.includes(`"${type}"`)) && Object.keys(entitlementEvents.schemas).length === 4, "all four implemented entitlement lifecycle events are registered");
check("ENTITLEMENT-EVENT-EVIDENCE", entitlementEventTypes.every((type) => JSON.stringify(entitlementEvents.schemas[type]).includes("proposal_hash")) && ["EntitlementApprovalGranted", "EntitlementApprovalRejected"].every((type) => JSON.stringify(entitlementEvents.schemas[type]).includes("permission_snapshot")) && entitlementEvents.schemas.EntitlementProposalExecuted.properties.payload.properties.approval_count.const === 2, "events bind immutable hash, approval permission snapshots and exact quorum");
check("ENTITLEMENT-REASON-SEPARATION", ["reason_ref", "reason_hash"].every((field) => JSON.stringify(entitlementEvents.schemas.EntitlementProposed).includes(field)) && !JSON.stringify(entitlementEvents).includes('"reason"'), "proposal events expose only encrypted reason references and hashes");
check("ENTITLEMENT-FIXTURES", entitlementFixtures.baseFixtureVersion === "1.18.0" && entitlementEventTypes.every((type) => entitlementFixtures.fixtures.filter((fixture) => fixture.eventType === type).length === 1) && entitlementFixtures.fixtures.every((fixture) => fixture.fromVersion === 1 && fixture.toVersion === 1 && fixture.inputHash === hash(fixture.input) && fixture.expectedHash === hash(fixture.expected)), "all entitlement events have deterministic verified identity fixtures");

const failures = results.filter((result) => result.status === "failed");
const base = { reportVersion: "1.1.0", stage: 5, kind: "contract-control-plane-contract", generatedAt: new Date().toISOString(), status: failures.length ? "failed" : "passed", summary: { checks: results.length, passed: results.length - failures.length, failed: failures.length }, openAPIHash: hash(contract), eventRegistryHash: hash(events), fixtureHash: hash(fixtures), entitlementOpenAPIHash: hash(entitlementContract), entitlementEventRegistryHash: hash(entitlementEvents), entitlementFixtureHash: hash(entitlementFixtures), results };
const report = { ...base, reportHash: hash(base) };
const target = resolve(root, "gate-reports/stage-5/contract-control-plane-contract.json");
await mkdir(dirname(target), { recursive: true });
await writeFile(target, `${JSON.stringify(report, null, 2)}\n`);
console.log(`${report.status}: ${report.summary.passed}/${report.summary.checks} checks; report ${report.reportHash}`);
if (failures.length) {
  for (const failure of failures) console.error(`${failure.id}: ${failure.details}`);
  process.exitCode = 1;
}
