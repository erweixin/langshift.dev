#!/usr/bin/env node
import { createHash } from "node:crypto";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import path from "node:path";

const args = new Map();
for (let index = 2; index < process.argv.length; index += 2) args.set(process.argv[index], process.argv[index + 1]);
const databasePath = args.get("--database");
const sourceCommit = args.get("--source-commit");
if (!databasePath || !/^[0-9a-f]{40}$/.test(sourceCommit ?? "")) throw new Error("usage: write-stage5-management-security-report.mjs --database <go-test.jsonl> --source-commit <40-hex>");

const raw = await readFile(databasePath);
const migrationManifestRaw = await readFile("deploy/migrations/manifest.json");
const currentMigration = JSON.parse(migrationManifestRaw).migrations?.at(-1);
if (!Number.isInteger(currentMigration?.version) || !currentMigration?.name) throw new Error("current migration manifest is invalid");
const records = raw.toString("utf8").split("\n").filter(Boolean).map((line) => JSON.parse(line));
const requiredTests = [
  "TestContractControlPlaneRequiresCurrentTwoPersonApprovalAndSynchronizesSeats",
  "TestControlServiceCommitsIdempotencyEventsContractsAdjustmentsAndUsage",
];
const passed = new Set(records.filter((record) => record.Action === "pass" && record.Test).map((record) => record.Test));
if (records.some((record) => record.Action === "fail") || requiredTests.some((test) => !passed.has(test))) throw new Error("management security database tests did not pass");

const output = records.map((record) => record.Output ?? "").join("");
const extract = (name) => {
  const match = output.match(new RegExp(`${name}=(\\{[^\\n]+\\})`));
  if (!match) throw new Error(`missing ${name} structured evidence`);
  return JSON.parse(match[1]);
};
const contract = extract("management_security");
const service = extract("contract_service_security");
if (contract.attack_cases !== 18 || contract.unexpected_successes !== 0 || contract.direct_mutation_successes !== 0 || contract.audit_completeness_percent !== 100 || contract.seat_overage_successes !== 0) throw new Error("contract security metrics violate a zero-tolerance threshold");
if (service.entitlement_attack_cases !== 3 || service.unexpected_successes !== 0 || service.entitlement_approval_events !== 2 || service.entitlement_audit_completeness_percent !== 100 || service.admin_read_audits !== 3) throw new Error("entitlement or admin-read security metrics violate a zero-tolerance threshold");

const controlSource = await readFile("internal/contracts/postgres/control_plane_integration_test.go", "utf8");
const serviceSource = await readFile("internal/contracts/postgres/control_service_integration_test.go", "utf8");
const requiredGuards = [
  ["unauthorized_approver", "member without contract authority unexpectedly approved"],
  ["proposal_hash_tamper", "different proposal hash unexpectedly succeeded"],
  ["proposal_target_version_tamper", "different target version unexpectedly succeeded"],
  ["approval_expired", "expired proposal unexpectedly succeeded"],
  ["initiator_self_approval", "initiator unexpectedly approved"],
  ["duplicate_principal_vote", "same approval principal unexpectedly voted twice"],
  ["single_approval_execution", "only one approval"],
  ["third_vote_quorum_bypass", "third approval unexpectedly bypassed"],
  ["approver_role_revoked", "approver lost contract authority"],
  ["executed_proposal_replay", "executed proposal replay unexpectedly mutated"],
  ["direct_contract_mutation", "proposal-only mutation boundary"],
  ["seat_overage", "exceeded the active contract seat limit"],
  ["rejected_proposal_execution", "rejected proposal unexpectedly executed"],
  ["stale_contract_version_execution", "stale contract target version unexpectedly executed"],
  ["direct_credit_mutation", "mutated a credit grant without an approved proposal"],
  ["invalid_negative_adjustment", "violates the hard cap unexpectedly succeeded"],
  ["stale_bucket_version_execution", "stale bucket version unexpectedly executed"],
  ["stale_reauthentication", "stale reauthentication unexpectedly succeeded"],
  ["entitlement_initiator_self_approval", "entitlement initiator approval unexpectedly succeeded"],
  ["stale_entitlement_version", "stale entitlement version unexpectedly succeeded"],
  ["direct_entitlement_mutation", "direct contract-service entitlement mutation unexpectedly succeeded"],
];
for (const [, marker] of requiredGuards) {
  if (!controlSource.includes(marker) && !serviceSource.includes(marker)) throw new Error(`missing required guard evidence: ${marker}`);
}

const generatedAt = records.find((record) => record.Action === "pass" && record.Test === requiredTests[0])?.Time;
const base = {
  reportVersion: "1.0.0",
  stage: 5,
  kind: "management-control-plane-security",
  generatedAt,
  sourceCommit,
  status: "passed",
  overallStage5Status: "in_progress",
  evidence: {
    databaseGoTestJsonSha256: createHash("sha256").update(raw).digest("hex"),
    database: `PostgreSQL 16 isolated database with migrations through version ${currentMigration.version}`,
    migration: { version: currentMigration.version, name: currentMigration.name, manifestSha256: createHash("sha256").update(migrationManifestRaw).digest("hex") },
    serviceRole: "NOBYPASSRLS lites_contract_service",
    tests: requiredTests,
  },
  results: {
    attackCases: requiredGuards.map(([id]) => ({ id, unexpectedSuccesses: 0 })),
    totalAttackCases: requiredGuards.length,
    unexpectedSuccesses: 0,
    exactTwoPersonExecutions: contract.exact_two_person_executions,
    directMutationSuccesses: 0,
    auditCompletenessPercent: 100,
    adminReadAuditRows: service.admin_read_audits,
  },
  verifiedBoundaries: [
    "five-minute reauthentication",
    "initiator separation and two distinct current approvers",
    "immutable proposal hash plus proposal, contract, bucket and entitlement version fences",
    "role and session revalidation at execution",
    "append-only contract, entitlement, accounting and administrator-read audit evidence",
    "direct database mutation denied to the contract service role",
  ],
  notClaimedByThisReport: ["external_penetration_test", "complete_admin_console", "complete_stage5_approval"],
};
const report = { ...base, reportHash: createHash("sha256").update(JSON.stringify(base)).digest("hex") };
const target = path.resolve("gate-reports/stage-5/management-control-plane-security.json");
await mkdir(path.dirname(target), { recursive: true });
await writeFile(target, `${JSON.stringify(report, null, 2)}\n`);
console.log(`stage-5 management security gate: attacks=${requiredGuards.length} unexpected=0 audit=100% report=${report.reportHash}`);
