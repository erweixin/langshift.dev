#!/usr/bin/env node
import { createHash } from "node:crypto";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import path from "node:path";
import { pathToFileURL } from "node:url";

const sha256 = (value) => createHash("sha256").update(value).digest("hex");

export function validateEffectLedgerMetrics(automatic, workspace) {
  const failures = [];
  const requireMetric = (condition, name) => { if (!condition) failures.push(name); };
  requireMetric(automatic?.scenario === "outcome_unknown_reconciliation", "automatic:scenario");
  requireMetric(Number.isInteger(automatic?.repetitions) && automatic.repetitions >= 100, "automatic:repetitions");
  requireMetric(automatic?.unknown_results_recorded === automatic?.repetitions, "automatic:unknown_results_recorded");
  requireMetric(automatic?.premature_joins_blocked === automatic?.repetitions, "automatic:premature_joins_blocked");
  requireMetric(automatic?.reconciliations_confirmed === automatic?.repetitions, "automatic:reconciliations_confirmed");
  requireMetric(automatic?.unique_continuations === automatic?.repetitions, "automatic:unique_continuations");
  requireMetric(automatic?.stale_results_rejected >= automatic?.repetitions * 2, "automatic:stale_results_rejected");
  requireMetric(automatic?.duplicate_external_effects === 0, "automatic:duplicate_external_effects");
  requireMetric(automatic?.duplicate_continuations === 0, "automatic:duplicate_continuations");
  requireMetric(automatic?.lost_event_facts === 0, "automatic:lost_event_facts");

  requireMetric(workspace?.scenario === "workspace_half_commit", "workspace:scenario");
  requireMetric(Number.isInteger(workspace?.repetitions) && workspace.repetitions >= 100, "workspace:repetitions");
  requireMetric(workspace?.database_commit_crashes === workspace?.repetitions, "workspace:database_commit_crashes");
  requireMetric(workspace?.atomic_rollbacks === workspace?.repetitions, "workspace:atomic_rollbacks");
  requireMetric(workspace?.external_revisions_written === 1, "workspace:external_revisions_written");
  requireMetric(workspace?.reconciliations_confirmed === 1, "workspace:reconciliations_confirmed");
  requireMetric(workspace?.duplicate_external_writes === 0, "workspace:duplicate_external_writes");
  requireMetric(workspace?.duplicate_revision_events === 0, "workspace:duplicate_revision_events");
  requireMetric(workspace?.lost_event_facts === 0, "workspace:lost_event_facts");
  return [...new Set(failures)].sort();
}

async function main() {
  const args = new Map();
  for (let index = 2; index < process.argv.length; index += 2) args.set(process.argv[index], process.argv[index + 1]);
  const input = args.get("--input");
  const sourceCommit = args.get("--source-commit");
  const outputArg = args.get("--output");
  if (!input || !/^[0-9a-f]{40}$/.test(sourceCommit ?? "")) {
    throw new Error("usage: write-stage3-effect-ledger-report.mjs --input <go-test.jsonl> --source-commit <40-hex> [--output <report.json>]");
  }

  const raw = await readFile(input);
  const records = raw.toString("utf8").split("\n").filter(Boolean).map((line) => JSON.parse(line));
  const executionPackage = "github.com/langshift/lites/internal/execution/postgres";
  const toolWorkerPackage = "github.com/langshift/lites/internal/toolworker";
  const toolReconcilerPackage = "github.com/langshift/lites/internal/toolreconciler";
  const requiredTests = [
    { package: executionPackage, name: "TestOutcomeUnknownFaultInjection100", source: "internal/execution/postgres/outcome_unknown_fault_integration_test.go" },
    { package: executionPackage, name: "TestEffectOutcomeUnknownCanDeferThenEscalateToManualRepair", source: "internal/execution/postgres/tool_complete_integration_test.go" },
    { package: executionPackage, name: "TestWorkspacePublishIsFencedHeartbeatCoherentAndAtomicallyCompleted", source: "internal/execution/postgres/workspace_revision_publish_integration_test.go" },
    { package: executionPackage, name: "TestToolClaimHeartbeatAndExpiredReclaimAreFenced", source: "internal/execution/postgres/tool_claim_integration_test.go" },
    { package: executionPackage, name: "TestToolEffectRepairExecutesEveryResolutionExactlyOnce", source: "internal/execution/postgres/repair_integration_test.go" },
    { package: executionPackage, name: "TestToolEffectRepairRejectionIsTerminalAndAudited", source: "internal/execution/postgres/repair_integration_test.go" },
    { package: executionPackage, name: "TestEffectCompletionValues", source: "internal/execution/postgres/tool_complete_test.go" },
    { package: executionPackage, name: "TestSweepExpiredToolEffectValidation", source: "internal/execution/postgres/tool_effect_sweeper_test.go" },
    { package: executionPackage, name: "TestEscalateReconciliationValidation", source: "internal/execution/postgres/reconciliation_escalate_test.go" },
    { package: toolWorkerPackage, name: "TestHandlerPersistsOutcomeUnknownAndSchedulesReconciliation", source: "internal/toolworker/handler_test.go" },
    { package: toolReconcilerPackage, name: "TestSweeperProducesStableEncryptedEvidenceAndReconciliationCommand", source: "internal/toolreconciler/sweeper_test.go" },
    { package: toolReconcilerPackage, name: "TestSweeperTransitionsManualEffectClassesWithoutAutomaticCommand", source: "internal/toolreconciler/sweeper_test.go" },
    { package: toolReconcilerPackage, name: "TestHandlerCompletesConfirmedProviderLookup", source: "internal/toolreconciler/handler_test.go" },
    { package: toolReconcilerPackage, name: "TestHandlerDefersInconclusiveLookupWithBoundedBackoff", source: "internal/toolreconciler/handler_test.go" },
    { package: toolReconcilerPackage, name: "TestHandlerEscalatesAfterMaximumAutomaticRounds", source: "internal/toolreconciler/handler_test.go" },
    { package: toolReconcilerPackage, name: "TestHandlerDoesNotCommitAfterHeartbeatLoss", source: "internal/toolreconciler/handler_test.go" },
    { package: toolReconcilerPackage, name: "TestRegistryLookupRequiresExactCoverageAndRejectsDrift", source: "internal/toolreconciler/handler_test.go" },
    { package: toolReconcilerPackage, name: "TestHTTPLookupBindsProviderIdentityAndResponse", source: "internal/toolreconciler/http_lookup_test.go" },
  ];
  const passedTests = requiredTests.map((test) => ({
    ...test,
    passed: records.some((record) => record.Package === test.package && record.Test === test.name && record.Action === "pass"),
  }));
  const failedRecords = records.filter((record) => record.Action === "fail");
  if (failedRecords.length || passedTests.some((test) => !test.passed)) {
    throw new Error("effect-ledger gate requires a completely passing suite and every protocol test");
  }
  const metricFor = (testName) => {
    const record = records.find((candidate) => candidate.Package === executionPackage && candidate.Test === testName && candidate.Action === "output" && candidate.Output?.includes("fault_injection="));
    if (!record) throw new Error(`missing fault metric for ${testName}`);
    return JSON.parse(record.Output.slice(record.Output.indexOf("fault_injection=") + "fault_injection=".length).trim());
  };
  const automatic = metricFor("TestOutcomeUnknownFaultInjection100");
  const workspace = metricFor("TestWorkspacePublishIsFencedHeartbeatCoherentAndAtomicallyCompleted");
  const metricFailures = validateEffectLedgerMetrics(automatic, workspace);
  if (metricFailures.length) throw new Error(`effect-ledger metrics failed: ${metricFailures.join(", ")}`);

  const passTimes = requiredTests.map((test) => records.find((record) => record.Package === test.package && record.Test === test.name && record.Action === "pass")?.Time).filter(Boolean).sort();
  const reportBase = {
    reportVersion: "1.0.0",
    stage: 3,
    kind: "effect-ledger-reconciliation",
    generatedAt: passTimes.at(-1),
    sourceCommit,
    status: "passed",
    evidence: {
      rawGoTestJsonSha256: sha256(raw),
      database: "PostgreSQL 16 temporary isolated database",
      executionRole: "NOBYPASSRLS lites_agent_service",
      protocolTests: passedTests.map(({ passed, ...test }) => test),
    },
    results: {
      effectClassesValidated: ["idempotent_write", "reconcilable_write", "compensatable_write", "irreversible_write"],
      automaticReconciliation: automatic,
      workspaceReconciliation: workspace,
      manualRepair: {
        resolutionsValidated: ["confirmed_occurred", "confirmed_not_occurred", "accepted_unknown"],
        approvalPolicy: "two-person",
        concurrentExecutorsPerResolution: 32,
        durableWinnersPerResolution: 1,
        terminalRejectionAudited: true,
      },
      reconciliationWorker: {
        expiredEffectsSwept: true,
        manualEffectClassesNeverAutoReplayed: true,
        providerLookupIdentityBound: true,
        boundedAutomaticRounds: true,
        heartbeatLossCommitsNothing: true,
        exhaustedRoundsEscalateToRepair: true,
      },
      duplicateExternalEffects: 0,
      duplicateContinuations: 0,
      lostEventFacts: 0,
    },
    verifiedInvariants: {
      immutableIdentity: "effect scope, key, request hash, provider request id, ToolCall and Run remain bound for the full effect lifecycle",
      fencedOwnership: "claim, heartbeat, expiry and completion require the current attempt, fence, lease token and immutable tool binding",
      noBlindRetry: "uncertain reconcilable, compensatable and irreversible writes enter outcome_unknown instead of reacquiring the original write right",
      automaticReconciliation: "provider evidence resolves the effect, ToolCall, join and sole Run continuation in one transaction",
      automaticReconciliationBounds: "expired reconcilable effects are swept under tenant/shard locks; inconclusive lookups back off to a fixed maximum before audited manual repair",
      workspaceCommit: "a durable commit decision precedes external publication and half commits converge without a second external revision write",
      manualRepair: "exhausted uncertainty requires a two-person, immutable, audited Repair Command with exactly one durable executor",
    },
    zeroToleranceFailures: [],
  };
  const report = { ...reportBase, reportHash: sha256(JSON.stringify(reportBase)) };
  const output = path.resolve(outputArg ?? "gate-reports/stage-3/effect-ledger-reconciliation-report.json");
  await mkdir(path.dirname(output), { recursive: true });
  await writeFile(output, `${JSON.stringify(report, null, 2)}\n`);
  console.log(`stage-3 effect-ledger gate: automatic=${automatic.repetitions} workspace=${workspace.repetitions} status=passed report=${report.reportHash}`);
}

if (import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) await main();
