#!/usr/bin/env node
import { createHash } from "node:crypto";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import path from "node:path";

const args = new Map();
for (let index = 2; index < process.argv.length; index += 2) args.set(process.argv[index], process.argv[index + 1]);
const input = args.get("--input");
const sourceCommit = args.get("--source-commit");
if (!input || !/^[0-9a-f]{40}$/.test(sourceCommit ?? "")) {
  throw new Error("usage: write-stage3-child-orchestration-fault-report.mjs --input <go-test.jsonl> --source-commit <40-hex>");
}

const raw = await readFile(input);
const records = raw.toString("utf8").split("\n").filter(Boolean).map((line) => JSON.parse(line));
const packageName = "github.com/langshift/lites/internal/execution/postgres";
const testName = "TestChildOrchestrationFaultInjection100";
const testPass = records.find((record) => record.Package === packageName && record.Test === testName && record.Action === "pass");
const metricRecord = records.find((record) => record.Package === packageName && record.Test === testName && record.Action === "output" && record.Output?.includes("fault_injection="));
const failed = records.filter((record) => record.Action === "fail");
if (!testPass || !metricRecord || failed.length) throw new Error("child orchestration fault test did not pass in a completely passing PostgreSQL suite");
const marker = metricRecord.Output.indexOf("fault_injection=") + "fault_injection=".length;
const metrics = JSON.parse(metricRecord.Output.slice(marker).trim());
const valid = metrics.scenario === "child_orchestration_race"
  && metrics.repetitions >= 100
  && metrics.all + metrics.any + metrics.quorum === metrics.repetitions
  && metrics.atomic_spawns === metrics.repetitions
  && metrics.unique_continuations === metrics.repetitions
  && metrics.cleanup_commands === metrics.any + metrics.quorum
  && metrics.late_commits_rejected >= metrics.repetitions
  && metrics.duplicate_continuations === 0
  && metrics.quota_leaks === 0
  && metrics.lost_event_facts === 0;
if (!valid) throw new Error("child orchestration metrics do not satisfy the Stage 3 zero-tolerance gate");

const reportBase = {
  reportVersion: "1.0.0",
  stage: 3,
  kind: "child-orchestration-fault-injection",
  generatedAt: testPass.Time,
  sourceCommit,
  status: "passed",
  evidence: {
    rawGoTestJsonSha256: createHash("sha256").update(raw).digest("hex"),
    testPackage: packageName,
    testName,
    testSource: "internal/execution/postgres/child_orchestration_fault_integration_test.go",
    database: "PostgreSQL 16 temporary isolated database",
    executionRole: "NOBYPASSRLS lites_agent_service",
  },
  results: metrics,
  verifiedInvariants: {
    atomicSpawn: "every accepted plan creates all three child Runs, membership rows, jobs, events and root quota reservations atomically",
    joinUniqueness: "concurrent all/any/quorum terminal commits create exactly one continuation and one parent resume fact",
    boundedCleanup: "early any/quorum joins create exactly one cursor-fenced cleanup work item and tolerate completed-work redelivery",
    lateResultFence: "duplicate or no-longer-needed Worker commits are rejected after terminal or cancellation fence ownership changes",
    quotaAndFacts: "all child terminal facts remain durable and root concurrent/budget reservations return to zero",
  },
  zeroToleranceFailures: [],
};
const report = { ...reportBase, reportHash: createHash("sha256").update(JSON.stringify(reportBase)).digest("hex") };
const output = path.resolve("gate-reports/stage-3/child-orchestration-fault-report.json");
await mkdir(path.dirname(output), { recursive: true });
await writeFile(output, `${JSON.stringify(report, null, 2)}\n`);
console.log(`stage-3 child orchestration fault gate: repetitions=${metrics.repetitions} status=passed report=${report.reportHash}`);
