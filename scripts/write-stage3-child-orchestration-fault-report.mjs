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
const failed = records.filter((record) => record.Action === "fail");
const tests = [
  {
    name: "TestChildOrchestrationFaultInjection100",
    source: "internal/execution/postgres/child_orchestration_fault_integration_test.go",
  },
  {
    name: "TestRecursiveRunCancellationFaultInjection100",
    source: "internal/execution/postgres/recursive_cancellation_fault_integration_test.go",
  },
].map((test) => {
  const pass = records.find((record) => record.Package === packageName && record.Test === test.name && record.Action === "pass");
  const metricRecord = records.find((record) => record.Package === packageName && record.Test === test.name && record.Action === "output" && record.Output?.includes("fault_injection="));
  if (!pass || !metricRecord) throw new Error(`${test.name} did not pass or emit fault metrics`);
  const marker = metricRecord.Output.indexOf("fault_injection=") + "fault_injection=".length;
  return { ...test, pass, metrics: JSON.parse(metricRecord.Output.slice(marker).trim()) };
});
if (failed.length) throw new Error("the PostgreSQL suite containing child orchestration fault tests was not completely passing");

const joinAndCleanup = tests[0].metrics;
const recursiveCancellation = tests[1].metrics;
const joinValid = joinAndCleanup.scenario === "child_orchestration_race"
  && joinAndCleanup.repetitions >= 100
  && joinAndCleanup.all + joinAndCleanup.any + joinAndCleanup.quorum === joinAndCleanup.repetitions
  && joinAndCleanup.atomic_spawns === joinAndCleanup.repetitions
  && joinAndCleanup.unique_continuations === joinAndCleanup.repetitions
  && joinAndCleanup.cleanup_commands === joinAndCleanup.any + joinAndCleanup.quorum
  && joinAndCleanup.late_commits_rejected >= joinAndCleanup.repetitions
  && joinAndCleanup.duplicate_continuations === 0
  && joinAndCleanup.quota_leaks === 0
  && joinAndCleanup.lost_event_facts === 0;
const recursiveValid = recursiveCancellation.scenario === "recursive_run_cancellation"
  && recursiveCancellation.repetitions >= 100
  && recursiveCancellation.trees_cancelled === recursiveCancellation.repetitions
  && recursiveCancellation.barriers_settled === recursiveCancellation.repetitions * 3
  && recursiveCancellation.propagated_barriers === recursiveCancellation.repetitions * 2
  && recursiveCancellation.propagation_replays >= recursiveCancellation.repetitions * 2
  && recursiveCancellation.late_commits_rejected === recursiveCancellation.repetitions
  && recursiveCancellation.duplicate_terminal_events === 0
  && recursiveCancellation.unexpected_resumes === 0
  && recursiveCancellation.quota_leaks === 0
  && recursiveCancellation.lost_event_facts === 0;
if (!joinValid || !recursiveValid) throw new Error("child orchestration metrics do not satisfy the Stage 3 zero-tolerance gate");

const reportBase = {
  reportVersion: "1.1.0",
  stage: 3,
  kind: "child-orchestration-fault-injection",
  generatedAt: tests.map((test) => test.pass.Time).sort().at(-1),
  sourceCommit,
  status: "passed",
  evidence: {
    rawGoTestJsonSha256: createHash("sha256").update(raw).digest("hex"),
    testPackage: packageName,
    tests: tests.map((test) => ({ name: test.name, source: test.source })),
    database: "PostgreSQL 16 temporary isolated database",
    executionRole: "NOBYPASSRLS lites_agent_service",
  },
  results: { joinAndCleanup, recursiveCancellation },
  verifiedInvariants: {
    atomicSpawn: "every accepted plan creates all three child Runs, membership rows, jobs, events and root quota reservations atomically",
    joinUniqueness: "concurrent all/any/quorum terminal commits create exactly one continuation and one parent resume fact",
    boundedCleanup: "early any/quorum joins create exactly one cursor-fenced cleanup work item and tolerate completed-work redelivery",
    lateResultFence: "duplicate or no-longer-needed Worker commits are rejected after terminal or cancellation fence ownership changes",
    recursiveCancellation: "root cancellation propagates through every descendant, settles one durable barrier per run and never resumes a cancelled ancestor",
    quotaAndFacts: "all child terminal facts remain durable and root concurrent/budget reservations return to zero",
  },
  zeroToleranceFailures: [],
};
const report = { ...reportBase, reportHash: createHash("sha256").update(JSON.stringify(reportBase)).digest("hex") };
const output = path.resolve("gate-reports/stage-3/child-orchestration-fault-report.json");
await mkdir(path.dirname(output), { recursive: true });
await writeFile(output, `${JSON.stringify(report, null, 2)}\n`);
console.log(`stage-3 child orchestration fault gate: join_repetitions=${joinAndCleanup.repetitions} recursive_repetitions=${recursiveCancellation.repetitions} status=passed report=${report.reportHash}`);
