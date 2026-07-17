import assert from "node:assert/strict";
import { buildProfileEvaluationReport, validateProfileEvaluation } from "./write-stage6-profile-eval-report.mjs";

const sourceCommit = "a".repeat(40);
const rcHash = "b".repeat(64);
const profile = "route_planner";
const profileContract = { id: profile, rubric: ["dimension_a", "dimension_b"], maxCostUsd: 0.20, p95LatencyMs: 12000 };
const specialistSamples = [{ id: "S-EN", locale: "en", input: { semanticKey: "route_planner:t1:s1:1" } }, { id: "S-ZH", locale: "zh-CN", input: { semanticKey: "route_planner:t1:s1:1" } }];
const sharedSamples = [{ id: "E-EN", locale: "en", transition: "t1" }, { id: "E-ZH", locale: "zh-CN", transition: "t1" }];
const specialistDatasetHash = "1".repeat(64);
const sharedDatasetHash = "2".repeat(64);
const allSamples = [...specialistSamples.map((sample) => ({ ...sample, suite: "specialist", transition: "t1" })), ...sharedSamples.map((sample) => ({ ...sample, suite: "shared" }))];
let attempt = 0;
const records = allSamples.flatMap((sample) => ["baseline", "candidate"].map((variant) => ({ schemaVersion: "1.0.0", profile, suite: sample.suite, sampleId: sample.id, locale: sample.locale, transition: sample.transition, variant, sourceCommit, rcHash, datasetHash: sample.suite === "specialist" ? specialistDatasetHash : sharedDatasetHash, status: "completed", providerAttemptId: `00000000-0000-4000-8000-${String(++attempt).padStart(12, "0")}`, behaviorManifestHash: "3".repeat(64), profileSnapshotHash: "4".repeat(64), modelSnapshotHash: "5".repeat(64), promptSnapshotHash: "6".repeat(64), toolSnapshotHash: "7".repeat(64), policySnapshotHash: "8".repeat(64), contextManifestHash: "9".repeat(64), outputHash: "a".repeat(64), costUsd: 0.10, latencyMs: 1000, deterministicChecksPassed: true, secretOrPIILeaks: 0, unauthorizedToolCalls: 0, zeroToleranceFailures: [], reviews: [{ reviewerId: "reviewer-a", scores: { dimension_a: 4, dimension_b: 4 } }, { reviewerId: "reviewer-b", scores: { dimension_a: 4, dimension_b: 4 } }] })));
const options = { profile, sourceCommit, rcHash, profileContract, specialistSamples, sharedSamples, specialistDatasetHash, sharedDatasetHash };
assert.equal(validateProfileEvaluation(records, options).reviewerAgreement, 1);
const report = buildProfileEvaluationReport(records, { ...options, evidencePath: "profile.jsonl", rawEvidenceSha256: "b".repeat(64), generatedAt: "2026-01-01T00:00:00.000Z" });
assert.equal(report.status, "passed");
assert.equal(report.samples.candidateExecutions, 4);
for (const [name, mutate] of [
  ["missing pair", (copy) => copy.pop()],
  ["secret leak", (copy) => { copy.find((record) => record.variant === "candidate").secretOrPIILeaks = 1; }],
  ["unauthorized tool", (copy) => { copy[0].unauthorizedToolCalls = 1; }],
  ["rubric mismatch", (copy) => { delete copy[0].reviews[0].scores.dimension_b; }],
  ["cost budget", (copy) => { copy.find((record) => record.variant === "candidate").costUsd = 0.21; }],
  ["latency budget", (copy) => { copy.filter((record) => record.variant === "candidate").forEach((record) => { record.latencyMs = 12001; }); }],
  ["quality regression", (copy) => { copy.filter((record) => record.variant === "candidate").forEach((record) => { record.reviews[0].scores.dimension_a = 3; record.reviews[1].scores.dimension_a = 3; }); }],
  ["duplicate reviewer", (copy) => { copy[0].reviews[1].reviewerId = copy[0].reviews[0].reviewerId; }],
]) {
  const copy = structuredClone(records);
  mutate(copy);
  assert.throws(() => validateProfileEvaluation(copy, options), undefined, name);
}
console.log("Stage 6 profile eval report self-test passed: paired bilingual baseline accepted; missing/safety/rubric/cost/latency/quality/reviewer failures rejected");
