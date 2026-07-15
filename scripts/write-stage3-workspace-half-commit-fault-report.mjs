#!/usr/bin/env node
import { createHash } from "node:crypto";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import path from "node:path";

const args = new Map();
for (let index = 2; index < process.argv.length; index += 2) args.set(process.argv[index], process.argv[index + 1]);
const input = args.get("--input");
const sourceCommit = args.get("--source-commit");
if (!input || !/^[0-9a-f]{40}$/.test(sourceCommit ?? "")) {
  throw new Error("usage: write-stage3-workspace-half-commit-fault-report.mjs --input <go-test.jsonl> --source-commit <40-hex>");
}

const raw = await readFile(input);
const records = raw.toString("utf8").split("\n").filter(Boolean).map((line) => JSON.parse(line));
const packageName = "github.com/langshift/lites/internal/execution/postgres";
const testName = "TestWorkspacePublishIsFencedHeartbeatCoherentAndAtomicallyCompleted";
const testPass = records.find((record) => record.Package === packageName && record.Test === testName && record.Action === "pass");
const metricRecord = records.find((record) => record.Package === packageName && record.Test === testName && record.Action === "output" && record.Output?.includes("fault_injection="));
const failed = records.filter((record) => record.Action === "fail");
if (!testPass || !metricRecord || failed.length) throw new Error("Workspace half-commit fault test did not pass in a completely passing PostgreSQL suite");
const marker = metricRecord.Output.indexOf("fault_injection=") + "fault_injection=".length;
const metrics = JSON.parse(metricRecord.Output.slice(marker).trim());
const valid = metrics.scenario === "workspace_half_commit"
  && metrics.repetitions >= 100
  && metrics.database_commit_crashes === metrics.repetitions
  && metrics.atomic_rollbacks === metrics.repetitions
  && metrics.external_revisions_written === 1
  && metrics.reconciliations_confirmed === 1
  && metrics.duplicate_external_writes === 0
  && metrics.duplicate_revision_events === 0
  && metrics.lost_event_facts === 0;
if (!valid) throw new Error("Workspace half-commit metrics do not satisfy the Stage 3 zero-tolerance gate");

const reportBase = {
  reportVersion: "1.0.0",
  stage: 3,
  kind: "workspace-half-commit-fault-injection",
  generatedAt: testPass.Time,
  sourceCommit,
  status: "passed",
  evidence: {
    rawGoTestJsonSha256: createHash("sha256").update(raw).digest("hex"),
    testPackage: packageName,
    testName,
    testSource: "internal/execution/postgres/workspace_revision_publish_integration_test.go",
    database: "PostgreSQL 16 temporary isolated database",
    executionRole: "NOBYPASSRLS lites_agent_service",
  },
  results: metrics,
  verifiedInvariants: {
    contentAddressedWrite: "the external Workspace revision is immutable and represented by one revision/hash identity across every recovery attempt",
    transactionRollback: "a crash after the Workspace event append and before PostgreSQL commit rolls back revision, ToolCall, effect, inbox, attempt, continuation and event facts together",
    uncertaintyBoundary: "an expired publisher is converted to outcome_unknown instead of blindly writing the external revision again",
    reconciliation: "observed external revision evidence confirms the Workspace revision and its effect ledger atomically",
    noDuplicateFacts: "retries create neither a duplicate external write nor a duplicate WorkspaceRevisionCommitted fact",
  },
  zeroToleranceFailures: [],
};
const report = { ...reportBase, reportHash: createHash("sha256").update(JSON.stringify(reportBase)).digest("hex") };
const output = path.resolve("gate-reports/stage-3/workspace-half-commit-fault-report.json");
await mkdir(path.dirname(output), { recursive: true });
await writeFile(output, `${JSON.stringify(report, null, 2)}\n`);
console.log(`stage-3 Workspace half-commit fault gate: repetitions=${metrics.repetitions} status=passed report=${report.reportHash}`);
