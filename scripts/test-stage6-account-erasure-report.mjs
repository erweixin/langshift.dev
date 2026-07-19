import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { verifyAccountErasureReport } from "./verify-stage6-account-erasure-report.mjs";

const commit = "a".repeat(40);
const rcHash = "b".repeat(64);
const build = (dirty) => {
  const base = {
    executionResult: "passed",
    status: dirty ? "failed" : "passed",
    evidenceClassification: dirty ? "current_dirty" : "current_clean",
    sourceCommit: commit,
    rcHash,
    worktreeDirty: dirty,
    accounts: 100,
    totalSurfaceReceiptRowsAfterRestore: 1200,
    readableSurfacesAfter: 0,
    dependencyGate: { tests: [{}, {}, {}] },
  };
  return { ...base, reportHash: createHash("sha256").update(JSON.stringify(base)).digest("hex") };
};

assert.equal(verifyAccountErasureReport(build(false), { sourceCommit: commit, rcHash, worktreeDirty: false }), true);
assert.equal(verifyAccountErasureReport(build(true), { sourceCommit: commit, rcHash, worktreeDirty: true }), true);
assert.throws(() => verifyAccountErasureReport({ ...build(true), status: "passed" }, { sourceCommit: commit, rcHash, worktreeDirty: true }), /dirty evidence must not pass/);
assert.throws(() => verifyAccountErasureReport({ ...build(false), reportHash: "0".repeat(64) }, { sourceCommit: commit, rcHash, worktreeDirty: false }), /report hash is invalid/);
assert.throws(() => verifyAccountErasureReport({ ...build(false), readableSurfacesAfter: 1 }, { sourceCommit: commit, rcHash, worktreeDirty: false }), /deleted data remains readable/);
console.log("stage-6 account erasure report verifier self-test: status=passed");
