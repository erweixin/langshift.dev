#!/usr/bin/env node
import { createHash } from "node:crypto";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import path from "node:path";

const args = new Map();
for (let index = 2; index < process.argv.length; index += 2) args.set(process.argv[index], process.argv[index + 1]);
const input = args.get("--input");
const sourceCommit = args.get("--source-commit");
if (!input || !/^[0-9a-f]{40}$/.test(sourceCommit ?? "")) {
  throw new Error("usage: write-stage3-execution-delivery-fault-report.mjs --input <go-test.jsonl> --source-commit <40-hex>");
}

const raw = await readFile(input);
const records = raw.toString("utf8").split("\n").filter(Boolean).map((line) => JSON.parse(line));
const packageName = "github.com/langshift/lites/internal/execution/postgres";
const testName = "TestExecutionDeliveryFaultInjection100";
const testPass = records.find((record) => record.Package === packageName && record.Test === testName && record.Action === "pass");
const metricRecord = records.find((record) => record.Package === packageName && record.Test === testName && record.Action === "output" && record.Output?.includes("fault_injection="));
const failed = records.filter((record) => record.Action === "fail");
if (!testPass || !metricRecord || failed.length) throw new Error("execution delivery fault test did not pass in a completely passing PostgreSQL suite");
const marker = metricRecord.Output.indexOf("fault_injection=") + "fault_injection=".length;
const metrics = JSON.parse(metricRecord.Output.slice(marker).trim());
const valid = metrics.scenario === "execution_delivery_recovery"
  && metrics.repetitions >= 100
  && metrics.queue_loss_recoveries === metrics.repetitions
  && metrics.live_duplicates_rejected === metrics.repetitions
  && metrics.completed_duplicates_deduplicated === metrics.repetitions
  && metrics.expired_leases_recovered === metrics.repetitions
  && metrics.stale_owners_rejected === metrics.repetitions
  && metrics.old_epoch_commands_rejected === metrics.repetitions
  && metrics.duplicate_terminal_events === 0
  && metrics.state_regressions === 0
  && metrics.lost_event_facts === 0;
if (!valid) throw new Error("execution delivery metrics do not satisfy the Stage 3 zero-tolerance gate");

const reportBase = {
  reportVersion: "1.0.0",
  stage: 3,
  kind: "execution-delivery-fault-injection",
  generatedAt: testPass.Time,
  sourceCommit,
  status: "passed",
  evidence: {
    rawGoTestJsonSha256: createHash("sha256").update(raw).digest("hex"),
    testPackage: packageName,
    testName,
    testSource: "internal/execution/postgres/execution_delivery_fault_integration_test.go",
    database: "PostgreSQL 16 temporary isolated database",
    executionRole: "NOBYPASSRLS lites_agent_service",
  },
  results: metrics,
  verifiedInvariants: {
    immutableRedelivery: "a lost queue delivery requeues the same command identity with a monotonic queue generation",
    duplicateDelivery: "live duplicate delivery cannot acquire a second execution right and terminal duplicate delivery is durably deduplicated",
    crashRecovery: "an expired Worker lease is replaced by exactly one higher fence and one new attempt without changing the Run business version",
    staleOwnerFence: "the expired owner cannot heartbeat or commit after the replacement execution right is installed",
    restoreEpochFence: "a command from a previous EventStore recovery epoch is rejected before it can mutate durable state",
    factCompleteness: "every recovered Run has one terminal event, two started attempts, two completed attempt facts and no retained execution right",
  },
  zeroToleranceFailures: [],
};
const report = { ...reportBase, reportHash: createHash("sha256").update(JSON.stringify(reportBase)).digest("hex") };
const output = path.resolve("gate-reports/stage-3/execution-delivery-fault-report.json");
await mkdir(path.dirname(output), { recursive: true });
await writeFile(output, `${JSON.stringify(report, null, 2)}\n`);
console.log(`stage-3 execution delivery fault gate: repetitions=${metrics.repetitions} status=passed report=${report.reportHash}`);
