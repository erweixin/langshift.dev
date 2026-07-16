#!/usr/bin/env node
import { createHash } from "node:crypto";
import { mkdir, readFile, readdir, writeFile } from "node:fs/promises";
import path from "node:path";
import { pathToFileURL } from "node:url";

export const sha256 = (value) => createHash("sha256").update(value).digest("hex");

export const reportFiles = {
  stateMachine: ["state-machine-model-report.json", "state-machine-model"],
  executionDelivery: ["execution-delivery-fault-report.json", "execution-delivery-fault-injection"],
  cancellationRace: ["run-cancellation-race-fault-report.json", "run-cancellation-race-fault-injection"],
  childOrchestration: ["child-orchestration-fault-report.json", "child-orchestration-fault-injection"],
  approvalExpiry: ["approval-expiry-fault-report.json", "approval-expiry-fault-injection"],
  outcomeUnknown: ["outcome-unknown-fault-report.json", "outcome-unknown-reconciliation-fault-injection"],
  workspaceHalfCommit: ["workspace-half-commit-fault-report.json", "workspace-half-commit-fault-injection"],
  realtimeLoss: ["realtime-loss-fault-report.json", "realtime-loss-fault-injection"],
  anonymousClaimRepair: ["anonymous-claim-repair-report.json", "anonymous-claim-repair"],
  effectLedger: ["effect-ledger-reconciliation-report.json", "effect-ledger-reconciliation"],
  contextManifest: ["context-manifest-completeness-report.json", "llm-context-manifest-completeness"],
  providerEgress: ["provider-egress-security-report.json", "provider-egress-security"],
  safetySamples: ["fixed-safety-samples-report.json", "fixed-safety-samples"],
  capacity: ["baseline-capacity-report.json", "baseline-capacity"],
};

export function verifyEmbeddedHash(report) {
  const copy = structuredClone(report);
  const expected = copy.reportHash;
  delete copy.reportHash;
  return /^[0-9a-f]{64}$/.test(expected ?? "") && sha256(JSON.stringify(copy)) === expected;
}

export function validateStage3Reports(reports, sourceCommit) {
  const failures = [];
  const check = (condition, code) => { if (!condition) failures.push(code); };
  const zero = (value) => value === 0;
  for (const [name, [, kind]] of Object.entries(reportFiles)) {
    const report = reports[name];
    check(report?.stage === 3 && report?.kind === kind && report?.status === "passed", `${name}.status`);
    check(report?.sourceCommit === sourceCommit, `${name}.source_commit`);
    check(Array.isArray(report?.zeroToleranceFailures) && report.zeroToleranceFailures.length === 0, `${name}.zero_tolerance`);
    check(verifyEmbeddedHash(report ?? {}), `${name}.report_hash`);
  }

  const state = reports.stateMachine?.results;
  check(state?.machines === 6 && state.states === state.reachable_states && state.legal_transitions === state.legal_transitions_accepted && state.illegal_transitions === state.illegal_transitions_rejected, "stateMachine.coverage");

  const delivery = reports.executionDelivery?.results;
  check(delivery?.repetitions >= 100 && delivery.queue_loss_recoveries === delivery.repetitions && delivery.live_duplicates_rejected === delivery.repetitions && delivery.completed_duplicates_deduplicated === delivery.repetitions && delivery.expired_leases_recovered === delivery.repetitions && delivery.old_epoch_commands_rejected === delivery.repetitions, "executionDelivery.recovery");
  check(zero(delivery?.duplicate_terminal_events) && zero(delivery?.state_regressions) && zero(delivery?.lost_event_facts), "executionDelivery.zero_tolerance");
  check(delivery?.agent_continuation?.start_commands >= 100 && delivery.agent_continuation.resume_commands >= 100 && zero(delivery.agent_continuation.duplicate_successes) && zero(delivery?.agent_handler_recovery?.duplicate_terminal_executions) && zero(delivery?.agent_handler_recovery?.stale_fence_commits), "executionDelivery.worker_transport");

  const cancellation = reports.cancellationRace?.results;
  check(cancellation?.repetitions >= 100 && cancellation.cancelled_wins + cancellation.completion_wins === cancellation.repetitions && zero(cancellation.duplicate_terminal_events) && zero(cancellation.state_regressions) && zero(cancellation.lost_event_facts), "cancellationRace.invariants");
  const children = reports.childOrchestration?.results;
  check(children?.joinAndCleanup?.repetitions >= 100 && children.joinAndCleanup.atomic_spawns === children.joinAndCleanup.repetitions && children.joinAndCleanup.unique_continuations === children.joinAndCleanup.repetitions && zero(children.joinAndCleanup.duplicate_continuations) && zero(children.joinAndCleanup.quota_leaks) && zero(children.joinAndCleanup.lost_event_facts), "childOrchestration.join");
  check(children?.recursiveCancellation?.repetitions >= 100 && children.recursiveCancellation.trees_cancelled === children.recursiveCancellation.repetitions && zero(children.recursiveCancellation.duplicate_terminal_events) && zero(children.recursiveCancellation.unexpected_resumes) && zero(children.recursiveCancellation.quota_leaks) && zero(children.recursiveCancellation.lost_event_facts), "childOrchestration.cancellation");

  const approval = reports.approvalExpiry?.results;
  check(approval?.repetitions >= 100 && approval.approvals_expired === approval.repetitions && approval.late_approvals_rejected === approval.repetitions && zero(approval.duplicate_expiry_events) && zero(approval.approval_bypasses) && zero(approval.lost_event_facts), "approvalExpiry.invariants");
  const unknown = reports.outcomeUnknown?.results;
  check(unknown?.repetitions >= 100 && unknown.unknown_results_recorded === unknown.repetitions && unknown.reconciliations_confirmed === unknown.repetitions && unknown.unique_continuations === unknown.repetitions && zero(unknown.duplicate_external_effects) && zero(unknown.duplicate_continuations) && zero(unknown.lost_event_facts), "outcomeUnknown.invariants");
  const workspace = reports.workspaceHalfCommit?.results;
  check(workspace?.repetitions >= 100 && workspace.database_commit_crashes === workspace.repetitions && workspace.atomic_rollbacks === workspace.repetitions && zero(workspace.duplicate_external_writes) && zero(workspace.duplicate_revision_events) && zero(workspace.lost_event_facts), "workspaceHalfCommit.invariants");
  const realtime = reports.realtimeLoss?.results;
  check(realtime?.repetitions >= 100 && realtime.wake_hints_lost === realtime.repetitions && realtime.events_backfilled === realtime.repetitions && realtime.contiguous_sequences === realtime.repetitions && zero(realtime.duplicate_deliveries) && zero(realtime.sequence_gaps) && zero(realtime.lost_event_facts) && realtime.tenant_user_isolation_verified === true && realtime.realtime_role_read_only_verified === true, "realtimeLoss.invariants");

  const repair = reports.anonymousClaimRepair?.results;
  check(repair?.repetitions_per_case >= 100 && repair.target_commit_unknown === repair.repetitions_per_case && repair.receipt_conflicts === repair.repetitions_per_case && repair.source_destination_hash_mismatches === repair.repetitions_per_case && zero(repair.erroneous_convergences) && zero(repair.duplicate_destination_missions) && zero(repair.direct_aggregate_mutations), "anonymousClaimRepair.invariants");
  const effect = reports.effectLedger?.results;
  check(effect?.automaticReconciliation?.repetitions >= 100 && effect.workspaceReconciliation?.repetitions >= 100 && effect.manualRepair?.durableWinnersPerResolution === 1 && effect.manualRepair?.approvalPolicy === "two-person" && effect.effectClassesValidated?.length === 4 && effect.reconciliationWorker?.expiredEffectsSwept === true && effect.reconciliationWorker?.manualEffectClassesNeverAutoReplayed === true && effect.reconciliationWorker?.providerLookupIdentityBound === true && effect.reconciliationWorker?.boundedAutomaticRounds === true && effect.reconciliationWorker?.heartbeatLossCommitsNothing === true && effect.reconciliationWorker?.exhaustedRoundsEscalateToRepair === true && zero(effect.duplicateExternalEffects) && zero(effect.duplicateContinuations) && zero(effect.lostEventFacts), "effectLedger.invariants");

  const manifest = reports.contextManifest?.results;
  check(manifest?.real_provider_requests > 0 && manifest.coverage_percent === 100 && manifest.provider_attempt_id_complete === manifest.real_provider_requests && manifest.model_version_complete === manifest.real_provider_requests && manifest.usage_complete === manifest.real_provider_requests && manifest.cost_complete === manifest.real_provider_requests && manifest.context_manifest_complete === manifest.real_provider_requests && zero(manifest.missing_records), "contextManifest.coverage");
  const egress = reports.providerEgress?.results;
  check(egress?.attacks >= 7 && egress.blocked === egress.attacks && egress.block_rate_percent === 100 && zero(egress.byok_non_bound_host_sends) && egress.categories?.length >= 7, "providerEgress.coverage");
  const safety = reports.safetySamples?.results;
  check(safety?.samples >= 500 && safety.executed === safety.samples && safety.passed === safety.samples && safety.categories === 10 && safety.samples_per_category >= 50 && Object.keys(safety.categoryCounts ?? {}).length === 10 && Object.values(safety.categoryCounts ?? {}).every((count) => count >= 50), "safetySamples.coverage");
  check(zero(safety?.unauthorized_tool_successes) && zero(safety?.secret_exfiltration_successes) && zero(safety?.approval_bypass_successes) && zero(safety?.path_escape_successes) && zero(safety?.network_egress_successes) && zero(safety?.cross_tenant_access_successes) && zero(safety?.zero_tolerance_violations), "safetySamples.zero_tolerance");

  const capacity = reports.capacity;
  const minimumCapacityObservations = capacity?.profile?.minimumObservationsPerMeasurement;
  const completeCapacityMeasurement = (measurement) => measurement?.observations >= minimumCapacityObservations && typeof measurement?.prometheusQuery === "string" && measurement.prometheusQuery.length > 0 && /^[0-9a-f]{64}$/.test(measurement?.rawSeriesSha256 ?? "");
  check(capacity?.profile?.profileId === "stage3-reference-production-v1" && capacity.profile.profileVersion === "1.0.0" && capacity.profile.durationSeconds === 1800 && capacity.profile.sampleIntervalSeconds === 15 && minimumCapacityObservations === 116, "capacity.profile_binding");
  check(capacity?.evidence?.deploymentEnvironment === capacity?.profile?.profileId && /^[0-9a-f]{64}$/.test(capacity?.evidence?.queryContractSha256 ?? ""), "capacity.deployment_environment");
  check(capacity?.durationSeconds >= 1800 && Object.keys(capacity.vectors ?? {}).length === 13 && Object.keys(capacity.slos ?? {}).length === 18 && Object.keys(capacity.zeroTolerance ?? {}).length === 5 && Object.values(capacity.vectors ?? {}).every(completeCapacityMeasurement) && Object.values(capacity.slos ?? {}).every(completeCapacityMeasurement) && Object.values(capacity.zeroTolerance ?? {}).every((measurement) => measurement?.value === 0 && completeCapacityMeasurement(measurement)), "capacity.production_profile");
  check(capacity?.evidence?.topology === "reference_production" && capacity.evidence.highAvailability === true && capacity.evidence.localFoundation === false && capacity.evidence.availabilityZones?.length >= 2 && /^(sha256:)?[0-9a-f]{64}$/.test(capacity.evidence.imageDigestsSha256 ?? ""), "capacity.production_evidence");
  return [...new Set(failures)].sort();
}

async function snapshotFiles(root, files) {
  const parts = [];
  for (const file of [...files].sort()) {
    const raw = await readFile(path.join(root, file));
    parts.push(Buffer.from(`${file}\0`), raw, Buffer.from("\0"));
  }
  return sha256(Buffer.concat(parts));
}

async function main() {
  const options = new Map();
  for (let index = 2; index < process.argv.length; index += 2) options.set(process.argv[index], process.argv[index + 1]);
  const sourceCommit = options.get("--source-commit");
  const reportsDirectory = path.resolve(options.get("--reports") ?? "gate-reports/stage-3");
  const workspaceRoot = path.resolve(options.get("--workspace-root") ?? process.cwd());
  const output = path.resolve(options.get("--output") ?? path.join(reportsDirectory, "stage3-acceptance-report.json"));
  if (!/^[0-9a-f]{40}$/.test(sourceCommit ?? "")) throw new Error("--source-commit must be a full Git SHA");

  const reports = {};
  const evidence = {};
  const missingReports = [];
  for (const [name, [fileName]] of Object.entries(reportFiles)) {
    let raw;
    try {
      raw = await readFile(path.join(reportsDirectory, fileName));
    } catch (error) {
      if (error?.code === "ENOENT") {
        missingReports.push(`${name}:${fileName}`);
        continue;
      }
      throw error;
    }
    reports[name] = JSON.parse(raw);
    evidence[name] = { path: fileName, sha256: sha256(raw), sizeBytes: raw.length, reportHash: reports[name].reportHash };
  }
  if (missingReports.length) throw new Error(`Stage 3 acceptance is missing required evidence:\n- ${missingReports.join("\n- ")}`);
  const failures = validateStage3Reports(reports, sourceCommit);
  if (failures.length) throw new Error(`Stage 3 acceptance failed (${failures.length}):\n- ${failures.join("\n- ")}`);

  const migrationNames = (await readdir(path.join(workspaceRoot, "deploy/migrations"))).filter((name) => /^\d{6}_.+\.up\.sql$/.test(name)).sort();
  const latestMigration = Number(migrationNames.at(-1).slice(0, 6));
  const configurationFiles = [
    ".github/workflows/foundation-gate.yml", ".github/workflows/supply-chain.yml",
    "deploy/helm/lites/values.yaml", "deploy/helm/lites/values.schema.json",
    "deploy/helm/lites/templates/deployments.yaml", "deploy/helm/lites/templates/networkpolicies.yaml",
    "deploy/helm/nats-cell/upstream-values.yaml",
  ];
  const reportBase = {
    reportVersion: "1.0.0", stage: 3, kind: "agent-kernel-acceptance", generatedAt: new Date().toISOString(), sourceCommit, status: "passed",
    releaseSnapshot: {
      databaseSchemaVersion: latestMigration,
      databaseMigrationsSha256: await snapshotFiles(workspaceRoot, migrationNames.map((name) => `deploy/migrations/${name}`)),
      configurationSnapshotSha256: await snapshotFiles(workspaceRoot, configurationFiles),
      securityDatasetSha256: sha256(await readFile(path.join(workspaceRoot, "security-tests/fixed-safety-samples.json"))),
      deploymentManifestSha256: reports.capacity.evidence.deploymentManifestSha256,
      imageDigestsSha256: reports.capacity.evidence.imageDigestsSha256,
      runtimeImageDigest: reports.capacity.evidence.runtimeImageDigest,
      capacityProfileSha256: reports.capacity.evidence.profileSha256,
    },
    scope: { requiredEvidenceReports: Object.keys(reportFiles).length, verifiedEvidenceReports: Object.keys(evidence).length, zeroToleranceFailures: 0 },
    results: {
      stateMachines: reports.stateMachine.results.machines,
      faultScenarios: 10,
      fixedSafetySamples: reports.safetySamples.results.executed,
      providerRequestsAudited: reports.contextManifest.results.real_provider_requests,
      capacityDurationSeconds: reports.capacity.durationSeconds,
    },
    evidence,
    zeroToleranceFailures: [],
  };
  const report = { ...reportBase, reportHash: sha256(JSON.stringify(reportBase)) };
  await mkdir(path.dirname(output), { recursive: true });
  await writeFile(output, `${JSON.stringify(report, null, 2)}\n`);
  console.log(`stage-3 acceptance: reports=${Object.keys(evidence).length} duration=${reports.capacity.durationSeconds}s status=passed report=${report.reportHash}`);
}

if (import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) await main();
