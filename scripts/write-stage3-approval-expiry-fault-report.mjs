#!/usr/bin/env node
import { createHash } from "node:crypto";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import path from "node:path";

const args = new Map();
for (let index = 2; index < process.argv.length; index += 2) args.set(process.argv[index], process.argv[index + 1]);
const input = args.get("--input");
const sourceCommit = args.get("--source-commit");
if (!input || !/^[0-9a-f]{40}$/.test(sourceCommit ?? "")) {
  throw new Error("usage: write-stage3-approval-expiry-fault-report.mjs --input <go-test.jsonl> --source-commit <40-hex>");
}

const raw = await readFile(input);
const records = raw.toString("utf8").split("\n").filter(Boolean).map((line) => JSON.parse(line));
const packageName = "github.com/langshift/lites/internal/execution/postgres";
const testName = "TestApprovalExpiryFaultInjection100";
const testPass = records.find((record) => record.Package === packageName && record.Test === testName && record.Action === "pass");
const metricRecord = records.find((record) => record.Package === packageName && record.Test === testName && record.Action === "output" && record.Output?.includes("fault_injection="));
const failed = records.filter((record) => record.Action === "fail");
if (!testPass || !metricRecord || failed.length) throw new Error("approval expiry fault test did not pass in a completely passing PostgreSQL suite");
const marker = metricRecord.Output.indexOf("fault_injection=") + "fault_injection=".length;
const metrics = JSON.parse(metricRecord.Output.slice(marker).trim());
const valid = metrics.scenario === "approval_expiry"
  && metrics.repetitions >= 100
  && metrics.approvals_expired === metrics.repetitions
  && metrics.expiry_replays === metrics.repetitions
  && metrics.late_approvals_rejected === metrics.repetitions
  && metrics.duplicate_expiry_events === 0
  && metrics.approval_bypasses === 0
  && metrics.lost_event_facts === 0;
if (!valid) throw new Error("approval expiry metrics do not satisfy the Stage 3 zero-tolerance gate");

const reportBase = {
  reportVersion: "1.0.0",
  stage: 3,
  kind: "approval-expiry-fault-injection",
  generatedAt: testPass.Time,
  sourceCommit,
  status: "passed",
  evidence: {
    rawGoTestJsonSha256: createHash("sha256").update(raw).digest("hex"),
    testPackage: packageName,
    testName,
    testSource: "internal/execution/postgres/approval_expiry_fault_integration_test.go",
    database: "PostgreSQL 16 temporary isolated database",
    executionRole: "NOBYPASSRLS lites_agent_service",
  },
  results: metrics,
  verifiedInvariants: {
    expiryDiscovery: "all elapsed pending approvals are discoverable under the current EventStore epoch and tenant scope",
    exactlyOnceExpiry: "expiry and its encrypted ApprovalExpired fact commit once and deterministic redelivery replays that same fact",
    lateDecisionFence: "an approval decision carrying the former pending version cannot grant an expired approval",
    noBypass: "expired approvals retain no approval decision row and cannot authorize the protected tool transition",
  },
  zeroToleranceFailures: [],
};
const report = { ...reportBase, reportHash: createHash("sha256").update(JSON.stringify(reportBase)).digest("hex") };
const output = path.resolve("gate-reports/stage-3/approval-expiry-fault-report.json");
await mkdir(path.dirname(output), { recursive: true });
await writeFile(output, `${JSON.stringify(report, null, 2)}\n`);
console.log(`stage-3 approval expiry fault gate: repetitions=${metrics.repetitions} status=passed report=${report.reportHash}`);
