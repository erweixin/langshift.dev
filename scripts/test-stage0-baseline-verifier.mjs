import assert from "node:assert/strict";

import { buildStage0Baseline, classifyEvidence, parseGitStatus, parseJSONSequence, resolveOperationOwner, validateDeferredMetadata } from "./verify-stage0-baseline.mjs";

const commit = "a".repeat(40);

assert.deepEqual(
  classifyEvidence({ status: "passed", sourceCommit: commit, worktreeDirty: false }, { currentCommit: commit, currentWorktreeDirty: false }),
  { classification: "current_commit", sourceCommit: commit, declaredResult: "passed", acceptableAsPassed: true, reason: "current_clean_pass" }
);
assert.equal(classifyEvidence({ status: "passed" }, { currentCommit: commit, currentWorktreeDirty: false }).reason, "source_commit_missing");
assert.equal(classifyEvidence({ status: "passed", sourceCommit: "b".repeat(40) }, { currentCommit: commit, currentWorktreeDirty: false }).reason, "source_commit_mismatch");
assert.equal(classifyEvidence({ status: "passed", sourceCommit: commit, worktreeDirty: true }, { currentCommit: commit, currentWorktreeDirty: false }).reason, "dirty_worktree");
assert.equal(classifyEvidence({ status: "fixture_validated", sourceCommit: commit }, { currentCommit: commit, currentWorktreeDirty: false }).classification, "fixture");

assert.equal(resolveOperationOwner("/v1/account", [{ id: "identity", pathPattern: "^/v1/account" }])[0].id, "identity");
assert.equal(resolveOperationOwner("/v1/account", [{ id: "identity", pathPattern: "^/v1/account" }, { id: "all", pathPattern: "^/v1/" }]).length, 2);
assert.deepEqual(parseJSONSequence('{"one":1}\n{"two":[2]}\n'), [{ one: 1 }, { two: [2] }]);
assert.deepEqual(parseGitStatus(" M .github/workflows/contract-gate.yml\n?? docs/releases/new.md\n"), [".github/workflows/contract-gate.yml", "docs/releases/new.md"]);
const deferredErrors = [];
validateDeferredMetadata(deferredErrors, { status: "deferred", reason: "later" }, ["security"], "test deferred");
assert.deepEqual(deferredErrors, ["test deferred: deferred requires an allowed ownerCategory", "test deferred: deferred requires a productionValidationEntry"]);

const report = await buildStage0Baseline({ includeEvidence: false });
assert.deepEqual(report.errors, []);
assert.equal(report.summary.requirements, 43);
assert.equal(report.summary.capabilities, 304);
assert.equal(report.summary.openapiOperations, 172);
assert.equal(report.summary.operationCounts.implemented, 127);
assert.equal(report.summary.operationCounts.deferred, 45);
assert.equal(report.summary.operationCounts.partial, 0);
assert.equal(report.summary.operationCounts.missing, 0);
assert.equal(report.summary.capabilityCounts.implemented, 298);
assert.equal(report.summary.capabilityCounts.partial, 0);
assert.equal(report.summary.capabilityCounts.missing, 0);
assert.equal(report.summary.capabilityCounts.deferred, 6);
assert.equal(report.operations.filter((operation) => operation.owner === "identity-service").length > 0, true);
assert.equal(report.operations.find((operation) => operation.operationId === "claims.revise").status, "implemented");
assert.equal(report.operations.find((operation) => operation.operationId === "internal.usage.release").status, "implemented");
assert.equal(report.operations.find((operation) => operation.operationId === "onboarding.route_preview").evidenceSet, "identity-stage1-production-boundary");
assert.equal(report.operations.find((operation) => operation.operationId === "auth.login").testFiles.includes("internal/identity/postgres/auth_service_integration_test.go"), true);
assert.equal(report.operations.find((operation) => operation.operationId === "reminders.cancel").evidenceSet, "product-current-career-saas-boundary");
assert.equal(report.operations.find((operation) => operation.operationId === "projects.get.v2").evidenceSet, "product-current-career-saas-boundary");
assert.equal(report.operations.find((operation) => operation.operationId === "tasks.generate").status, "deferred");
assert.equal(report.operations.find((operation) => operation.operationId === "runs.cancel").evidenceSet, "agent-stage3-production-boundary");
assert.equal(report.operations.find((operation) => operation.operationId === "realtime.connect").evidenceSet, "realtime-stage3-production-boundary");
assert.equal(report.operations.find((operation) => operation.operationId === "behavior.rollbacks.automatic").evidenceSet, "behavior-stage4-production-boundary");
assert.equal(report.operations.find((operation) => operation.operationId === "admin.contracts.propose.v2").evidenceSet, "contract-stage5-v2-production-boundary");
assert.equal(report.operations.find((operation) => operation.operationId === "admin.contracts.propose").status, "deferred");
const deferredCapability = report.capabilities.find((capability) => capability.capabilityId === "OPS-002-CAP-11");
assert.equal(deferredCapability.status, "deferred");
assert.equal(deferredCapability.deferredOwnerCategory, "delivery-operations");
assert.equal(deferredCapability.productionValidationEntry, "docs/product-implementation-plan.md#六commercial-ga-前置条件");
assert.equal(report.capabilities.find((capability) => capability.capabilityId === "ANON-004-CAP-03").evidenceSet, "stage1-anonymous-acquisition");
assert.equal(report.capabilities.find((capability) => capability.capabilityId === "AGENT-005-CAP-02").evidenceSet, "stage3-agent-kernel-and-providers");
assert.equal(report.capabilities.find((capability) => capability.capabilityId === "ID-002-CAP-09").status, "deferred");
assert.equal(report.capabilities.find((capability) => capability.capabilityId === "ID-003-CAP-01").status, "implemented");
assert.equal(report.capabilities.find((capability) => capability.capabilityId === "GROWTH-009-CAP-08").status, "implemented");

const cleanSourceState = { sourceCommit: commit, worktreeDirty: false, changedPaths: [], gitVersion: "git version test" };
const rcReport = await buildStage0Baseline({ includeEvidence: false, requireEngineeringRC: true, sourceState: cleanSourceState });
assert.equal(rcReport.completionMode, "engineering_rc");
assert.equal(rcReport.result, "passed");
assert.equal(rcReport.worktreeDirty, false);
assert.deepEqual(rcReport.errors, []);
assert.equal(rcReport.errors.some((error) => error.includes("partial/missing operations")), false);
assert.equal(rcReport.errors.some((error) => error.includes("partial/missing capabilities")), false);

const dirtyRCReport = await buildStage0Baseline({
  includeEvidence: false,
  requireEngineeringRC: true,
  sourceState: { ...cleanSourceState, worktreeDirty: true, changedPaths: ["README.md"] }
});
assert.equal(dirtyRCReport.result, "failed");
assert.equal(dirtyRCReport.worktreeDirty, true);
assert.deepEqual(dirtyRCReport.changedPaths, ["README.md"]);

process.stdout.write("stage 0 baseline verifier self-test passed\n");
