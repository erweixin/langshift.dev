#!/usr/bin/env node
import { createHash } from "node:crypto";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import path from "node:path";

const args = new Map();
for (let index = 2; index < process.argv.length; index += 2) {
  args.set(process.argv[index], process.argv[index + 1]);
}
const input = args.get("--input");
const sourceCommit = args.get("--source-commit");
if (!input || !/^[0-9a-f]{40}$/.test(sourceCommit ?? "")) {
  throw new Error("usage: write-stage3-cancellation-fault-report.mjs --input <go-test.jsonl> --source-commit <40-hex>");
}

const raw = await readFile(input);
const records = raw.toString("utf8").split("\n").filter(Boolean).map((line) => JSON.parse(line));
const packageName = "github.com/langshift/lites/internal/execution/postgres";
const testName = "TestRunCancellationRaceFaultInjection100";
const testPass = records.find((record) => record.Package === packageName && record.Test === testName && record.Action === "pass");
const metricRecord = records.find((record) => record.Package === packageName && record.Test === testName && record.Action === "output" && record.Output?.includes("fault_injection="));
const failed = records.filter((record) => record.Action === "fail");
if (!testPass || !metricRecord || failed.length) {
  throw new Error("cancellation fault test did not pass in a completely passing PostgreSQL suite");
}
const marker = metricRecord.Output.indexOf("fault_injection=") + "fault_injection=".length;
const metrics = JSON.parse(metricRecord.Output.slice(marker).trim());
if (metrics.scenario !== "run_cancellation_race" || metrics.repetitions < 100 || metrics.cancelled_wins + metrics.completion_wins !== metrics.repetitions || metrics.duplicate_terminal_events !== 0 || metrics.state_regressions !== 0 || metrics.lost_event_facts !== 0) {
  throw new Error("cancellation fault metrics do not satisfy the Stage 3 zero-tolerance gate");
}

const reportBase = {
  reportVersion: "1.0.0",
  stage: 3,
  kind: "run-cancellation-race-fault-injection",
  generatedAt: testPass.Time,
  sourceCommit,
  status: "passed",
  evidence: {
    rawGoTestJsonSha256: createHash("sha256").update(raw).digest("hex"),
    testPackage: packageName,
    testName,
    testSource: "internal/execution/postgres/run_cancellation_fault_integration_test.go",
    database: "PostgreSQL 16 temporary isolated database",
    executionRole: "NOBYPASSRLS lites_agent_service",
  },
  results: metrics,
  verifiedInvariants: {
    optimisticConcurrency: "a stale expected_run_version may conflict only when the competing legal terminal commit wins",
    terminalUniqueness: "exactly one RunSucceeded or RunCancelled fact per injected race",
    executionRightRelease: "active command, attempt, lease hash and lease expiry are cleared in every terminal outcome",
    cancellationDurability: "every cancelled outcome has exactly one settled event-backed cancellation row",
  },
  zeroToleranceFailures: [],
};
const report = { ...reportBase, reportHash: createHash("sha256").update(JSON.stringify(reportBase)).digest("hex") };
const output = path.resolve("gate-reports/stage-3/run-cancellation-race-fault-report.json");
await mkdir(path.dirname(output), { recursive: true });
await writeFile(output, `${JSON.stringify(report, null, 2)}\n`);
console.log(`stage-3 cancellation fault gate: repetitions=${metrics.repetitions} status=passed report=${report.reportHash}`);
