import assert from "node:assert/strict";
import { buildCapacityReport, validateCapacitySamples } from "./write-stage6-capacity-report.mjs";

const sourceCommit = "a".repeat(40);
const rcHash = "b".repeat(64);
const start = Date.parse("2026-01-01T00:00:00.000Z");
const metrics = { apiAvailability: 1, apiP95Ms: 100, apiP99Ms: 300, eventAppendAvailability: 1, eventAppendP99Ms: 100, queueWaitP95Ms: 500, queueWaitP99Ms: 1000, firstSafeTokenP95Ms: 1000, firstSafeTokenP99Ms: 3000, runDeadlineAdherence: 1, interactiveRunPlatformSuccess: 1, realtimeRecoveryP95Ms: 500, realtimeRecoveryP99Ms: 1000, runtimeColdStartP95Ms: 1000, runtimeColdStartP99Ms: 3000, toolOutcomeUnknownRatio: 0, toolOutcomeUnknownConverged15m: 1, tenantIsolationViolations: 0, legitimateThrottleRatio: 0 };
const samples = Array.from({ length: 38 }, (_, index) => {
  const phase = index < 30 ? "steady" : index < 35 ? "burst" : "recovery";
  return { schemaVersion: "1.0.0", sourceCommit, rcHash, status: "passed", runId: "00000000-0000-4000-8000-000000000001", referenceLoadVectorHash: "1".repeat(64), environmentFingerprint: "2".repeat(64), configSnapshotHash: "3".repeat(64), imageLockHash: "4".repeat(64), phase, loadMultiplier: phase === "burst" ? 2 : 1, windowStart: new Date(start + index * 60000).toISOString(), windowEnd: new Date(start + (index + 1) * 60000).toISOString(), metrics: { ...metrics }, backlogNormalized: phase !== "burst", outboxBacklogAgeSeconds: phase === "burst" ? 30 : 1, queueBacklogAgeSeconds: phase === "burst" ? 60 : 2, sweeperBacklogAgeSeconds: phase === "burst" ? 120 : 10 };
});
assert.deepEqual(validateCapacitySamples(samples, { sourceCommit, rcHash }).seconds, { steady: 1800, burst: 300, recovery: 180 });
const report = buildCapacityReport(samples, { sourceCommit, rcHash, evidencePath: "capacity.jsonl", rawEvidenceSha256: "5".repeat(64), generatedAt: "2026-01-01T01:00:00.000Z" });
assert.equal(report.status, "passed");
assert.equal(report.sloViolations, 0);
for (const [name, mutate] of [
  ["short steady", (copy) => copy.splice(0, 1)],
  ["weak burst", (copy) => { copy[30].loadMultiplier = 1.9; }],
  ["SLO violation", (copy) => { copy[0].metrics.apiP95Ms = 251; }],
  ["isolation violation", (copy) => { copy[0].metrics.tenantIsolationViolations = 1; }],
  ["backlog not recovered", (copy) => { copy.at(-1).backlogNormalized = false; }],
  ["environment drift", (copy) => { copy[0].configSnapshotHash = "6".repeat(64); }],
]) {
  const copy = structuredClone(samples);
  mutate(copy);
  assert.throws(() => validateCapacitySamples(copy, { sourceCommit, rcHash }), undefined, name);
}
console.log("Stage 6 capacity report self-test passed: 30m+2x/5m+recovery accepted; duration/load/SLO/isolation/backlog/config drift rejected");
