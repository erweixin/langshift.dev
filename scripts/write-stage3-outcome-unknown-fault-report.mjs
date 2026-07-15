#!/usr/bin/env node
import { createHash } from "node:crypto";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import path from "node:path";

const args = new Map();
for (let index = 2; index < process.argv.length; index += 2) args.set(process.argv[index], process.argv[index + 1]);
const input = args.get("--input");
const sourceCommit = args.get("--source-commit");
if (!input || !/^[0-9a-f]{40}$/.test(sourceCommit ?? "")) {
  throw new Error("usage: write-stage3-outcome-unknown-fault-report.mjs --input <go-test.jsonl> --source-commit <40-hex>");
}

const raw = await readFile(input);
const records = raw.toString("utf8").split("\n").filter(Boolean).map((line) => JSON.parse(line));
const packageName = "github.com/langshift/lites/internal/execution/postgres";
const testName = "TestOutcomeUnknownFaultInjection100";
const testPass = records.find((record) => record.Package === packageName && record.Test === testName && record.Action === "pass");
const metricRecord = records.find((record) => record.Package === packageName && record.Test === testName && record.Action === "output" && record.Output?.includes("fault_injection="));
const failed = records.filter((record) => record.Action === "fail");
if (!testPass || !metricRecord || failed.length) throw new Error("outcome-unknown fault test did not pass in a completely passing PostgreSQL suite");
const marker = metricRecord.Output.indexOf("fault_injection=") + "fault_injection=".length;
const metrics = JSON.parse(metricRecord.Output.slice(marker).trim());
const valid = metrics.scenario === "outcome_unknown_reconciliation"
  && metrics.repetitions >= 100
  && metrics.unknown_results_recorded === metrics.repetitions
  && metrics.premature_joins_blocked === metrics.repetitions
  && metrics.reconciliations_confirmed === metrics.repetitions
  && metrics.unique_continuations === metrics.repetitions
  && metrics.stale_results_rejected >= metrics.repetitions * 2
  && metrics.duplicate_external_effects === 0
  && metrics.duplicate_continuations === 0
  && metrics.lost_event_facts === 0;
if (!valid) throw new Error("outcome-unknown metrics do not satisfy the Stage 3 zero-tolerance gate");

const reportBase = {
  reportVersion: "1.0.0",
  stage: 3,
  kind: "outcome-unknown-reconciliation-fault-injection",
  generatedAt: testPass.Time,
  sourceCommit,
  status: "passed",
  evidence: {
    rawGoTestJsonSha256: createHash("sha256").update(raw).digest("hex"),
    testPackage: packageName,
    testName,
    testSource: "internal/execution/postgres/outcome_unknown_fault_integration_test.go",
    database: "PostgreSQL 16 temporary isolated database",
    executionRole: "NOBYPASSRLS lites_agent_service",
  },
  results: metrics,
  verifiedInvariants: {
    durableUnknown: "an uncertain provider result commits outcome_unknown and one reconciliation job without joining the tool group",
    providerIdentity: "reconciliation is bound to the original provider_request_id, effect id, effect key and immutable command identity",
    noBlindRetry: "the original write execution right is terminal and duplicate Worker results cannot repeat the external mutation",
    atomicResolution: "confirmed effect evidence, ToolCall success, group join, Run resume and the sole continuation commit atomically",
    factCompleteness: "each effect retains one unknown fact, one reconciled terminal fact and one durable effect ledger row",
  },
  zeroToleranceFailures: [],
};
const report = { ...reportBase, reportHash: createHash("sha256").update(JSON.stringify(reportBase)).digest("hex") };
const output = path.resolve("gate-reports/stage-3/outcome-unknown-fault-report.json");
await mkdir(path.dirname(output), { recursive: true });
await writeFile(output, `${JSON.stringify(report, null, 2)}\n`);
console.log(`stage-3 outcome-unknown fault gate: repetitions=${metrics.repetitions} status=passed report=${report.reportHash}`);
