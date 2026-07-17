#!/usr/bin/env node
import { createHash } from "node:crypto";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import path from "node:path";

const args = new Map();
for (let index = 2; index < process.argv.length; index += 2) args.set(process.argv[index], process.argv[index + 1]);
const input = args.get("--input");
const sourceCommit = args.get("--source-commit");
if (!input || !/^[0-9a-f]{40}$/.test(sourceCommit ?? "")) {
  throw new Error("usage: write-stage5-membership-csv-report.mjs --input <go-test.jsonl> --source-commit <40-hex>");
}

const raw = await readFile(input);
const records = raw.toString("utf8").split("\n").filter(Boolean).map((line) => JSON.parse(line));
const packageName = "github.com/langshift/lites/internal/identity/postgres";
const integrationTest = "TestMembershipLifecycleIsTenantScopedAuditedCASAndSessionSafe";
const requiredTests = [integrationTest, "TestMembershipImportCannotReactivateVoluntaryDeparture", "TestParseMembershipCSVNormalizesAndRejectsUnsafeRows"];
const passed = new Set(records.filter((record) => record.Package === packageName && record.Action === "pass" && record.Test).map((record) => record.Test));
const failures = records.filter((record) => record.Action === "fail");
const metricRecord = records.find((record) => record.Package === packageName && record.Test === integrationTest && record.Action === "output" && record.Output?.includes("stage5_membership_csv="));
if (failures.length || requiredTests.some((test) => !passed.has(test)) || !metricRecord) {
  throw new Error("Stage 5 membership CSV suite did not pass with complete evidence");
}
const marker = metricRecord.Output.indexOf("stage5_membership_csv=") + "stage5_membership_csv=".length;
const metrics = JSON.parse(metricRecord.Output.slice(marker).trim());
const valid = metrics.scenario === "membership_csv_replay"
  && metrics.command_replays >= 100
  && metrics.worker_deliveries >= 100
  && metrics.worker_claims === 1
  && metrics.worker_replays === metrics.worker_deliveries - 1
  && metrics.membership_imports === 1
  && metrics.queued_events === 1
  && metrics.completed_events === 1
  && metrics.active_target_memberships === 1
  && metrics.suspended_missing_members === 1
  && metrics.preserved_left_members === 1
  && metrics.seat_allocation_drift === 0
  && metrics.audit_completeness_percent === 100;
if (!valid) throw new Error("membership CSV metrics do not satisfy the Stage 5 replay gate");

const reportBase = {
  reportVersion: "1.0.0",
  stage: 5,
  kind: "membership-csv-replay",
  generatedAt: records.find((record) => record.Package === packageName && record.Test === integrationTest && record.Action === "pass")?.Time,
  sourceCommit,
  status: "passed",
  overallStage5Status: "in_progress",
  evidence: {
    rawGoTestJsonSha256: createHash("sha256").update(raw).digest("hex"),
    testPackage: packageName,
    tests: requiredTests,
    testSource: "internal/identity/postgres/membership_service_integration_test.go",
    database: "PostgreSQL 16 temporary isolated database with migrations through version 76",
    executionRole: "NOBYPASSRLS lites_identity_service",
  },
  results: metrics,
  verifiedInvariants: {
    replay: "100 API command deliveries converge on one import and 100 worker deliveries claim once then replay the terminal result",
    invalidRows: "malformed, duplicate and unsafe rows are rejected without removing valid normalized rows",
    voluntaryDeparture: "a membership in left state cannot be reactivated by an administrator CSV import",
    deactivationSafety: "deactivate_missing is applied only when the CSV has no rejected rows and revokes only enterprise-tenant sessions",
    audit: "one queued event, one completed event and row-level membership events retain the import correlation",
  },
  notClaimedByThisReport: [
    "active_contract_seat_allocation_and_release",
    "contract_ledger_model_based_100000_operation_gate",
    "complete_stage5_approval",
  ],
};
const report = { ...reportBase, reportHash: createHash("sha256").update(JSON.stringify(reportBase)).digest("hex") };
const output = path.resolve("gate-reports/stage-5/membership-csv-replay.json");
await mkdir(path.dirname(output), { recursive: true });
await writeFile(output, `${JSON.stringify(report, null, 2)}\n`);
console.log(`stage-5 membership CSV gate: commands=${metrics.command_replays} deliveries=${metrics.worker_deliveries} status=passed report=${report.reportHash}`);
