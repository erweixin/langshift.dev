#!/usr/bin/env node
import assert from "node:assert/strict";
import { validateEffectLedgerMetrics } from "./write-stage3-effect-ledger-report.mjs";

const automatic = {
  scenario: "outcome_unknown_reconciliation",
  repetitions: 100,
  unknown_results_recorded: 100,
  premature_joins_blocked: 100,
  reconciliations_confirmed: 100,
  unique_continuations: 100,
  stale_results_rejected: 200,
  duplicate_external_effects: 0,
  duplicate_continuations: 0,
  lost_event_facts: 0,
};
const workspace = {
  scenario: "workspace_half_commit",
  repetitions: 100,
  database_commit_crashes: 100,
  atomic_rollbacks: 100,
  external_revisions_written: 1,
  reconciliations_confirmed: 1,
  duplicate_external_writes: 0,
  duplicate_revision_events: 0,
  lost_event_facts: 0,
};
assert.deepEqual(validateEffectLedgerMetrics(automatic, workspace), []);

const mutations = [
  ["automatic repetitions", (a) => { a.repetitions = 99; }],
  ["unknown recording", (a) => { a.unknown_results_recorded = 99; }],
  ["premature join", (a) => { a.premature_joins_blocked = 99; }],
  ["automatic reconciliation", (a) => { a.reconciliations_confirmed = 99; }],
  ["duplicate effect", (a) => { a.duplicate_external_effects = 1; }],
  ["lost fact", (a) => { a.lost_event_facts = 1; }],
  ["workspace repetitions", (_a, w) => { w.repetitions = 99; }],
  ["workspace rollback", (_a, w) => { w.atomic_rollbacks = 99; }],
  ["workspace duplicate write", (_a, w) => { w.duplicate_external_writes = 1; }],
  ["workspace duplicate event", (_a, w) => { w.duplicate_revision_events = 1; }],
];
for (const [name, mutate] of mutations) {
  const changedAutomatic = structuredClone(automatic);
  const changedWorkspace = structuredClone(workspace);
  mutate(changedAutomatic, changedWorkspace);
  assert.notDeepEqual(validateEffectLedgerMetrics(changedAutomatic, changedWorkspace), [], `${name} mutation passed`);
}
console.log(`stage-3 effect-ledger verifier self-test: mutations=${mutations.length} status=passed`);
