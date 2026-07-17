import assert from "node:assert/strict";
import { buildDeploymentReport, validateDeploymentRecords } from "./write-stage6-deployment-report.mjs";

const sourceCommit = "a".repeat(40);
const rcHash = "b".repeat(64);
const target = "compose";
const operations = ["freshInstall", "upgrade", "backup", "restore", "rollback"];
const uuid = (value) => `00000000-0000-4000-8000-${String(value).padStart(12, "0")}`;
const records = operations.flatMap((operation, operationIndex) => [1, 2, 3].map((attempt) => {
  const index = operationIndex * 3 + attempt;
  const restore = operation === "restore";
  const schemas = { freshInstall: [0, 84], upgrade: [83, 84], backup: [84, 84], restore: [84, 84], rollback: [84, 84] }[operation];
  return {
    schemaVersion: "1.0.0", target, operation, attempt, status: "passed", runId: uuid(index), sourceCommit, rcHash,
    environmentFingerprint: "c".repeat(64), transcriptSha256: "d".repeat(64), stateBeforeSha256: "e".repeat(64), stateAfterSha256: "f".repeat(64),
    expectedLogicalDataChecksum: "1".repeat(64), logicalDataChecksumBefore: operation === "freshInstall" ? "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" : "1".repeat(64), logicalDataChecksumAfter: "1".repeat(64),
    schemaVersionBefore: schemas[0], schemaVersionAfter: schemas[1], storeEpochBefore: uuid(100 + index), storeEpochAfter: restore ? uuid(200 + index) : uuid(100 + index),
    healthChecksPassed: true, securityChecksPassed: true, dataReconciliationPassed: true, deletionReplayPassed: true,
    durationSeconds: 30, startedAt: `2026-01-01T00:${String(index).padStart(2, "0")}:00.000Z`, completedAt: `2026-01-01T00:${String(index).padStart(2, "0")}:30.000Z`, platformVersion: "Docker Desktop 4.x", runnerIdentity: "fixture-runner",
  };
}));

assert.equal(validateDeploymentRecords(records, { target, sourceCommit, rcHash }), true);
const report = buildDeploymentReport(records, { target, sourceCommit, rcHash, evidencePath: "evidence.jsonl", rawEvidenceSha256: "2".repeat(64), generatedAt: "2026-01-01T01:00:00.000Z" });
assert.equal(report.status, "passed");
assert.equal(report.operations.restore.consecutivePasses, 3);
assert.match(report.reportHash, /^[0-9a-f]{64}$/);

for (const [name, mutate] of [
  ["missing attempt", (copy) => copy.pop()],
  ["failed security check", (copy) => { copy[0].securityChecksPassed = false; }],
  ["restore without epoch rotation", (copy) => { const item = copy.find((record) => record.operation === "restore"); item.storeEpochAfter = item.storeEpochBefore; }],
  ["duplicate run ID", (copy) => { copy[1].runId = copy[0].runId; }],
  ["RC drift", (copy) => { copy[0].rcHash = "3".repeat(64); }],
  ["logical data drift", (copy) => { copy[0].logicalDataChecksumAfter = "4".repeat(64); }],
]) {
  const copy = structuredClone(records);
  mutate(copy);
  assert.throws(() => validateDeploymentRecords(copy, { target, sourceCommit, rcHash }), undefined, name);
}
console.log("Stage 6 deployment report self-test passed: valid=accepted missing/security/epoch/duplicate/RC/data-drift=rejected");
