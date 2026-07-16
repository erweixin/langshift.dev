#!/usr/bin/env node
import assert from "node:assert/strict";
import { reportFiles, sha256, validateStage3Reports } from "./write-stage3-acceptance-report.mjs";

const sourceCommit = "a".repeat(40);
const baseResults = {
  stateMachine: { machines: 6, states: 42, reachable_states: 42, legal_transitions: 74, legal_transitions_accepted: 74, illegal_transitions: 278, illegal_transitions_rejected: 278 },
  executionDelivery: { repetitions: 100, queue_loss_recoveries: 100, live_duplicates_rejected: 100, completed_duplicates_deduplicated: 100, expired_leases_recovered: 100, old_epoch_commands_rejected: 100, duplicate_terminal_events: 0, state_regressions: 0, lost_event_facts: 0, agent_continuation: { start_commands: 100, resume_commands: 100, duplicate_successes: 0 }, agent_handler_recovery: { duplicate_terminal_executions: 0, stale_fence_commits: 0 } },
  cancellationRace: { repetitions: 100, cancelled_wins: 50, completion_wins: 50, duplicate_terminal_events: 0, state_regressions: 0, lost_event_facts: 0 },
  childOrchestration: { joinAndCleanup: { repetitions: 100, atomic_spawns: 100, unique_continuations: 100, duplicate_continuations: 0, quota_leaks: 0, lost_event_facts: 0 }, recursiveCancellation: { repetitions: 100, trees_cancelled: 100, duplicate_terminal_events: 0, unexpected_resumes: 0, quota_leaks: 0, lost_event_facts: 0 } },
  approvalExpiry: { repetitions: 100, approvals_expired: 100, late_approvals_rejected: 100, duplicate_expiry_events: 0, approval_bypasses: 0, lost_event_facts: 0 },
  outcomeUnknown: { repetitions: 100, unknown_results_recorded: 100, reconciliations_confirmed: 100, unique_continuations: 100, duplicate_external_effects: 0, duplicate_continuations: 0, lost_event_facts: 0 },
  workspaceHalfCommit: { repetitions: 100, database_commit_crashes: 100, atomic_rollbacks: 100, duplicate_external_writes: 0, duplicate_revision_events: 0, lost_event_facts: 0 },
  realtimeLoss: { repetitions: 100, wake_hints_lost: 100, events_backfilled: 100, contiguous_sequences: 100, duplicate_deliveries: 0, sequence_gaps: 0, lost_event_facts: 0, tenant_user_isolation_verified: true, realtime_role_read_only_verified: true },
  anonymousClaimRepair: { repetitions_per_case: 100, target_commit_unknown: 100, receipt_conflicts: 100, source_destination_hash_mismatches: 100, erroneous_convergences: 0, duplicate_destination_missions: 0, direct_aggregate_mutations: 0 },
  effectLedger: { effectClassesValidated: [1, 2, 3, 4], automaticReconciliation: { repetitions: 100 }, workspaceReconciliation: { repetitions: 100 }, manualRepair: { durableWinnersPerResolution: 1, approvalPolicy: "two-person" }, reconciliationWorker: { expiredEffectsSwept: true, manualEffectClassesNeverAutoReplayed: true, providerLookupIdentityBound: true, boundedAutomaticRounds: true, heartbeatLossCommitsNothing: true, exhaustedRoundsEscalateToRepair: true }, duplicateExternalEffects: 0, duplicateContinuations: 0, lostEventFacts: 0 },
  contextManifest: { real_provider_requests: 3, coverage_percent: 100, provider_attempt_id_complete: 3, model_version_complete: 3, usage_complete: 3, cost_complete: 3, context_manifest_complete: 3, missing_records: 0 },
  providerEgress: { attacks: 7, blocked: 7, block_rate_percent: 100, byok_non_bound_host_sends: 0, categories: [1, 2, 3, 4, 5, 6, 7] },
  safetySamples: { samples: 500, executed: 500, passed: 500, categories: 10, samples_per_category: 50, categoryCounts: Object.fromEntries(Array.from({ length: 10 }, (_, index) => [`c${index}`, 50])), unauthorized_tool_successes: 0, secret_exfiltration_successes: 0, approval_bypass_successes: 0, path_escape_successes: 0, network_egress_successes: 0, cross_tenant_access_successes: 0, zero_tolerance_violations: 0 },
};
const reports = {};
for (const [name, [, kind]] of Object.entries(reportFiles)) {
  const report = { reportVersion: "1.0.0", stage: 3, kind, generatedAt: "2026-07-16T00:00:00Z", sourceCommit, status: "passed", results: baseResults[name] ?? {}, zeroToleranceFailures: [] };
  if (name === "capacity") {
    const measurement = { observations: 116, prometheusQuery: "sum(metric)", rawSeriesSha256: "c".repeat(64) };
    Object.assign(report, { profile: { profileId: "stage3-reference-production-v1", profileVersion: "1.0.0", durationSeconds: 1800, sampleIntervalSeconds: 15, minimumObservationsPerMeasurement: 116 }, durationSeconds: 1800, vectors: Object.fromEntries(Array.from({ length: 13 }, (_, index) => [`v${index}`, measurement])), slos: Object.fromEntries(Array.from({ length: 18 }, (_, index) => [`s${index}`, measurement])), zeroTolerance: Object.fromEntries(Array.from({ length: 5 }, (_, index) => [`z${index}`, { ...measurement, value: 0 }])), evidence: { topology: "reference_production", deploymentEnvironment: "stage3-reference-production-v1", queryContractSha256: "d".repeat(64), highAvailability: true, localFoundation: false, availabilityZones: ["a", "b"], imageDigestsSha256: "b".repeat(64) } });
  }
  report.reportHash = sha256(JSON.stringify(report));
  reports[name] = report;
}
assert.deepEqual(validateStage3Reports(reports, sourceCommit), []);

const mutations = [
  ["source commit", (x) => { x.stateMachine.sourceCommit = "b".repeat(40); }],
  ["embedded hash", (x) => { x.providerEgress.results.blocked = 6; }],
  ["delivery duplicate", (x) => { x.executionDelivery.results.agent_continuation.duplicate_successes = 1; }],
  ["effect duplicate", (x) => { x.effectLedger.results.duplicateExternalEffects = 1; }],
  ["effect reconciliation bound", (x) => { x.effectLedger.results.reconciliationWorker.boundedAutomaticRounds = false; }],
  ["safety coverage", (x) => { x.safetySamples.results.executed = 499; }],
  ["egress bypass", (x) => { x.providerEgress.results.byok_non_bound_host_sends = 1; }],
  ["capacity duration", (x) => { x.capacity.durationSeconds = 1799; }],
  ["capacity observations", (x) => { x.capacity.vectors.v0.observations = 1; }],
  ["capacity topology", (x) => { x.capacity.evidence.localFoundation = true; }],
];
for (const [name, mutate] of mutations) {
  const changed = structuredClone(reports);
  mutate(changed);
  assert.notDeepEqual(validateStage3Reports(changed, sourceCommit), [], `${name} mutation passed`);
}
console.log(`stage-3 acceptance verifier self-test: mutations=${mutations.length} status=passed`);
