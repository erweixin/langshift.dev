#!/usr/bin/env node
import { createHash } from "node:crypto";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import path from "node:path";

const args = new Map();
for (let index = 2; index < process.argv.length; index += 2) args.set(process.argv[index], process.argv[index + 1]);
const input = args.get("--input");
const sourceCommit = args.get("--source-commit");
if (!input || !/^[0-9a-f]{40}$/.test(sourceCommit ?? "")) {
  throw new Error("usage: write-stage3-state-machine-report.mjs --input <go-test.jsonl> --source-commit <40-hex>");
}

const raw = await readFile(input);
const records = raw.toString("utf8").split("\n").filter(Boolean).map((line) => JSON.parse(line));
const packageName = "github.com/langshift/lites/internal/execution/statemachine";
const modelTest = "TestAllLegalAndIllegalTransitionsAreExhaustivelyClassified";
const contractTest = "TestProductionMachinesExactlyMatchFrozenContract";
const modelPass = records.find((record) => record.Package === packageName && record.Test === modelTest && record.Action === "pass");
const contractPass = records.find((record) => record.Package === packageName && record.Test === contractTest && record.Action === "pass");
const metricRecord = records.find((record) => record.Package === packageName && record.Test === modelTest && record.Action === "output" && record.Output?.includes("state_machine_model="));
const failed = records.filter((record) => record.Action === "fail");
if (!modelPass || !contractPass || !metricRecord || failed.length) throw new Error("state-machine tests did not pass in a completely passing suite");
const marker = metricRecord.Output.indexOf("state_machine_model=") + "state_machine_model=".length;
const metrics = JSON.parse(metricRecord.Output.slice(marker).trim());
const valid = metrics.scenario === "execution_state_machine_model"
  && metrics.machines === 6
  && metrics.states > 0
  && metrics.legal_transitions > 0
  && metrics.legal_transitions_accepted === metrics.legal_transitions
  && metrics.illegal_transitions > 0
  && metrics.illegal_transitions_rejected === metrics.illegal_transitions
  && metrics.reachable_states === metrics.states;
if (!valid) throw new Error("state-machine metrics do not satisfy the Stage 3 gate");

const reportBase = {
  reportVersion: "1.0.0",
  stage: 3,
  kind: "state-machine-model",
  generatedAt: [modelPass.Time, contractPass.Time].sort().at(-1),
  sourceCommit,
  status: "passed",
  evidence: {
    rawGoTestJsonSha256: createHash("sha256").update(raw).digest("hex"),
    frozenContract: "contracts/state-machines/state-machines.json",
    tests: [
      { package: packageName, name: modelTest, source: "internal/execution/statemachine/machine_test.go" },
      { package: packageName, name: contractTest, source: "internal/execution/statemachine/contract_test.go" },
    ],
  },
  results: metrics,
  coverage: {
    machines: ["run", "tool_call", "command", "attempt", "approval", "workspace_revision"],
    legalTransitionReachabilityPercent: 100,
    illegalTransitionRejectionPercent: 100,
    frozenContractMatch: true,
  },
  zeroToleranceFailures: [],
};
const report = { ...reportBase, reportHash: createHash("sha256").update(JSON.stringify(reportBase)).digest("hex") };
const output = path.resolve("gate-reports/stage-3/state-machine-model-report.json");
await mkdir(path.dirname(output), { recursive: true });
await writeFile(output, `${JSON.stringify(report, null, 2)}\n`);
console.log(`stage-3 state-machine gate: machines=${metrics.machines} legal=${metrics.legal_transitions} illegal=${metrics.illegal_transitions} status=passed report=${report.reportHash}`);
