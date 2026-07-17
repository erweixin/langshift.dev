import assert from "node:assert/strict";
import { buildRecoveryReport, validateRecoveryRecords } from "./write-stage6-recovery-report.mjs";

const sourceCommit = "a".repeat(40);
const rcHash = "b".repeat(64);
const scenarios = ["instanceOrAZ", "region", "queueLoss"];
const uuid = (value) => `00000000-0000-4000-8000-${String(value).padStart(12, "0")}`;
const records = scenarios.flatMap((scenario, scenarioIndex) => [1, 2, 3].map((attempt) => {
  const index = scenarioIndex * 3 + attempt;
  const region = scenario === "region";
  const acknowledged = Date.parse("2026-01-01T00:00:00.000Z");
  const rpo = region ? 240 : 0;
  const rto = scenario === "instanceOrAZ" ? 240 : region ? 3000 : 600;
  return { schemaVersion: "1.0.0", sourceCommit, rcHash, scenario, attempt, status: "passed", runId: uuid(index), environmentFingerprint: "1".repeat(64), configSnapshotHash: "2".repeat(64), imageLockHash: "3".repeat(64), transcriptSha256: "4".repeat(64), stateBeforeSha256: "5".repeat(64), stateAfterSha256: "6".repeat(64), logicalDataChecksumBefore: "7".repeat(64), logicalDataChecksumAfter: "7".repeat(64), faultInjectedAt: new Date(acknowledged + 300000).toISOString(), lastAcknowledgedWriteAt: new Date(acknowledged).toISOString(), lastRecoveredWriteAt: new Date(acknowledged - rpo * 1000).toISOString(), trafficResumedAt: new Date(acknowledged + 300000 + rto * 1000).toISOString(), storeEpochBefore: uuid(100 + index), storeEpochAfter: region ? uuid(200 + index) : uuid(100 + index), oldEpochCommandsRejected: region ? 10 : 0, oldEpochCommandsAccepted: 0, lostEventFacts: 0, missingObjectReferences: 0, duplicateExternalEffects: 0, ledgerReconciliationPassed: true, accountErasureReplayPassed: true, healthChecksPassed: true, securityChecksPassed: true, pendingCommandsBeforeFault: scenario === "queueLoss" ? 100 : 0, recreatedCommands: scenario === "queueLoss" ? 100 : 0, duplicateCommandExecutions: 0 };
}));
assert.equal(validateRecoveryRecords(records, { sourceCommit, rcHash }), true);
const report = buildRecoveryReport(records, { sourceCommit, rcHash, evidencePath: "recovery.jsonl", rawEvidenceSha256: "8".repeat(64), generatedAt: "2026-01-01T02:00:00.000Z" });
assert.equal(report.status, "passed");
assert.equal(report.instanceOrAZ.rpoSeconds, 0);
for (const [name, mutate] of [
  ["instance data loss", (copy) => { copy[0].lastRecoveredWriteAt = "2025-12-31T23:59:59.000Z"; }],
  ["region RTO", (copy) => { const item = copy.find((record) => record.scenario === "region"); item.trafficResumedAt = "2026-01-01T02:00:01.000Z"; }],
  ["region epoch reuse", (copy) => { const item = copy.find((record) => record.scenario === "region"); item.storeEpochAfter = item.storeEpochBefore; }],
  ["old command accepted", (copy) => { copy[0].oldEpochCommandsAccepted = 1; }],
  ["queue command missing", (copy) => { const item = copy.find((record) => record.scenario === "queueLoss"); item.recreatedCommands = 99; }],
  ["deletion replay missing", (copy) => { copy[0].accountErasureReplayPassed = false; }],
]) {
  const copy = structuredClone(records);
  mutate(copy);
  assert.throws(() => validateRecoveryRecords(copy, { sourceCommit, rcHash }), undefined, name);
}
console.log("Stage 6 recovery report self-test passed: thresholds accepted; RPO/RTO/epoch/old-command/queue/deletion failures rejected");
