#!/usr/bin/env node
import { createHash } from "node:crypto";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import path from "node:path";

const args = new Map();
for (let index = 2; index < process.argv.length; index += 2) args.set(process.argv[index], process.argv[index + 1]);
const input = args.get("--input");
const sourceCommit = args.get("--source-commit");
if (!input || !/^[0-9a-f]{40}$/.test(sourceCommit ?? "")) {
  throw new Error("usage: write-stage3-anonymous-claim-repair-report.mjs --input <go-test.jsonl> --source-commit <40-hex>");
}

const raw = await readFile(input);
const records = raw.toString("utf8").split("\n").filter(Boolean).map((line) => JSON.parse(line));
const packageName = "github.com/langshift/lites/internal/identity/postgres";
const scenarioTest = "TestAnonymousClaimRepairFaultScenarios100";
const commandTest = "TestAnonymousClaimStoreConvergesWithRLSAndProtectsReservationFromExpiry";
const scenarioPass = records.find((record) => record.Package === packageName && record.Test === scenarioTest && record.Action === "pass");
const commandPass = records.find((record) => record.Package === packageName && record.Test === commandTest && record.Action === "pass");
const metricRecord = records.find((record) => record.Package === packageName && record.Test === scenarioTest && record.Action === "output" && record.Output?.includes("anonymous_claim_repair_gate="));
const failed = records.filter((record) => record.Action === "fail");
if (!scenarioPass || !commandPass || !metricRecord || failed.length) throw new Error("anonymous claim Repair tests did not pass in a completely passing PostgreSQL suite");
const marker = metricRecord.Output.indexOf("anonymous_claim_repair_gate=") + "anonymous_claim_repair_gate=".length;
const metrics = JSON.parse(metricRecord.Output.slice(marker).trim());
const valid = metrics.scenario === "anonymous_claim_manual_review_repair"
  && metrics.repetitions_per_case >= 100
  && metrics.target_commit_unknown === metrics.repetitions_per_case
  && metrics.target_commit_resolved === metrics.repetitions_per_case
  && metrics.receipt_conflicts === metrics.repetitions_per_case
  && metrics.receipt_conflicts_retained_manual_review === metrics.repetitions_per_case
  && metrics.source_destination_hash_mismatches === metrics.repetitions_per_case
  && metrics.hash_mismatches_retained_manual_review === metrics.repetitions_per_case
  && metrics.erroneous_convergences === 0
  && metrics.duplicate_destination_missions === 0
  && metrics.direct_aggregate_mutations === 0;
if (!valid) throw new Error("anonymous claim Repair metrics do not satisfy the Stage 3 zero-tolerance gate");

const reportBase = {
  reportVersion: "1.0.0",
  stage: 3,
  kind: "anonymous-claim-repair",
  generatedAt: [scenarioPass.Time, commandPass.Time].sort().at(-1),
  sourceCommit,
  status: "passed",
  evidence: {
    rawPostgresGoTestJsonSha256: createHash("sha256").update(raw).digest("hex"),
    database: "PostgreSQL 16 temporary isolated database",
    tests: [
      { package: packageName, name: scenarioTest, source: "internal/identity/postgres/anonymous_claim_repair_test.go" },
      { package: packageName, name: commandTest, source: "internal/identity/postgres/anonymous_claim_store_integration_test.go" },
    ],
    repairControl: "audited proposal, two distinct reauthenticated approvers, immutable proposal hash and target version",
  },
  results: metrics,
  verifiedInvariants: {
    exactFactsOnly: "target state is derived only from the unique claim_key and exact durable Mission, route and event facts",
    conflictFailClosed: "receipt conflicts and source/destination hash mismatches remain manual_review",
    noDirectMutation: "inspection never creates, deletes or changes the source claim or destination Mission",
    twoPersonExecution: "the PostgreSQL integration executes reconciliation only after two distinct eligible approvals",
  },
  zeroToleranceFailures: [],
};
const report = { ...reportBase, reportHash: createHash("sha256").update(JSON.stringify(reportBase)).digest("hex") };
const output = path.resolve("gate-reports/stage-3/anonymous-claim-repair-report.json");
await mkdir(path.dirname(output), { recursive: true });
await writeFile(output, `${JSON.stringify(report, null, 2)}\n`);
console.log(`stage-3 anonymous claim Repair gate: cases=${metrics.repetitions_per_case * 3} status=passed report=${report.reportHash}`);
