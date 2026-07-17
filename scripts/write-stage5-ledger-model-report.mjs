#!/usr/bin/env node
import { createHash } from "node:crypto";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import path from "node:path";

const args = new Map();
for (let index = 2; index < process.argv.length; index += 2) args.set(process.argv[index], process.argv[index + 1]);
const modelPath = args.get("--model");
const databasePath = args.get("--database");
const sourceCommit = args.get("--source-commit");
if (!modelPath || !databasePath || !/^[0-9a-f]{40}$/.test(sourceCommit ?? "")) {
  throw new Error("usage: write-stage5-ledger-model-report.mjs --model <json> --database <go-test.jsonl> --source-commit <40-hex>");
}

const modelRaw = await readFile(modelPath);
const databaseRaw = await readFile(databasePath);
const metrics = JSON.parse(modelRaw.toString("utf8"));
const records = databaseRaw.toString("utf8").split("\n").filter(Boolean).map((line) => JSON.parse(line));
const requiredTests = [
  ["github.com/langshift/lites/internal/contracts/postgres", "TestContractControlPlaneRequiresCurrentTwoPersonApprovalAndSynchronizesSeats"],
  ["github.com/langshift/lites/internal/contracts/postgres", "TestControlServiceCommitsIdempotencyEventsContractsAdjustmentsAndUsage"],
  ["github.com/langshift/lites/internal/billing/postgres", "TestConcurrentReservationsEnforceHardCreditCapAndReplay"],
];
const passed = new Set(records.filter((record) => record.Action === "pass" && record.Test).map((record) => `${record.Package}:${record.Test}`));
const failures = records.filter((record) => record.Action === "fail");
if (failures.length || requiredTests.some(([pkg, test]) => !passed.has(`${pkg}:${test}`))) {
  throw new Error("Stage 5 database accounting suite did not pass with complete evidence");
}
const operationKinds = ["contract", "reserve", "settle", "release", "expiry", "manual_adjustment", "replay", "membership"];
const modelValid = metrics.scenario === "contract_usage_ledger_model"
  && metrics.operations === 100_000
  && operationKinds.every((kind) => metrics.attempted?.[kind] > 0 && metrics.accepted?.[kind] > 0)
  && metrics.negative_balance_violations === 0
  && metrics.seat_overage_violations === 0
  && metrics.duplicate_charge_violations === 0
  && metrics.reconciliation_drift === 0
  && metrics.active_reservation_units === metrics.final_reserved_units
  && metrics.settlement_ledger_units === metrics.final_settled_units
  && metrics.audit_completeness_percent === 100;
if (!modelValid) throw new Error("Stage 5 100,000-operation model metrics violate a zero-tolerance invariant");

const generatedAt = records.find((record) => record.Action === "pass" && record.Test === requiredTests[0][1])?.Time;
const reportBase = {
  reportVersion: "1.0.0",
  stage: 5,
  kind: "contract-ledger-model-and-database-reconciliation",
  generatedAt,
  sourceCommit,
  status: "passed",
  overallStage5Status: "in_progress",
  evidence: {
    referenceModelSha256: createHash("sha256").update(modelRaw).digest("hex"),
    databaseGoTestJsonSha256: createHash("sha256").update(databaseRaw).digest("hex"),
    referenceModelTest: "internal/contracts/postgres/ledger_model_test.go",
    databaseTests: requiredTests.map(([pkg, test]) => ({ package: pkg, test })),
    database: "PostgreSQL 16 temporary isolated database with migrations through version 84",
    serviceRoles: ["NOBYPASSRLS lites_contract_service", "NOBYPASSRLS lites_agent_service"],
  },
  results: metrics,
  verifiedInvariants: {
    deterministicModel: "seed 20260717 executes exactly 100,000 mixed contract, seat, reservation, settlement, release, expiry, replay and adjustment attempts",
    hardCap: "reserved plus settled units never exceed granted units and no balance becomes negative",
    idempotency: "terminal and reservation replays do not alter balances or add a second ledger key",
    reconciliation: "active reservations equal reserved bucket units and immutable settlement entries equal settled bucket units",
    seats: "active contracts have exactly one seat per active membership, suspended contracts never allocate on join, renewal reconciles all active memberships, and termination releases every seat",
    databaseBoundary: "real RLS transactions enforce concurrent credit caps, two-person contract activation and manual adjustment, stale approval fences, direct-mutation denial, complete adjustment audit and seat synchronization",
  },
  notClaimedByThisReport: [
    "complete_contract_entitlement_admin_console",
    "complete_stage5_approval",
  ],
};
const report = { ...reportBase, reportHash: createHash("sha256").update(JSON.stringify(reportBase)).digest("hex") };
const output = path.resolve("gate-reports/stage-5/ledger-model-reconciliation.json");
await mkdir(path.dirname(output), { recursive: true });
await writeFile(output, `${JSON.stringify(report, null, 2)}\n`);
console.log(`stage-5 ledger model gate: operations=${metrics.operations} drift=${metrics.reconciliation_drift} status=passed report=${report.reportHash}`);
