import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { githubIssueQuery, normalizeGitHubIssuePages } from "./collect-stage6-release-issues.mjs";
import { buildReleaseIssuesReport } from "./write-stage6-release-issues-report.mjs";

const sha256 = (value) => createHash("sha256").update(value).digest("hex");
const sourceCommit = "a".repeat(40);
const rcHash = "b".repeat(64);
const capturedAt = "2026-01-01T00:00:00.000Z";
const issue = (number, state, labels) => ({ id: `I_${number}`, number, title: `Issue ${number}`, url: `https://github.com/erweixin/langshift.dev/issues/${number}`, state, createdAt: capturedAt, updatedAt: capturedAt, closedAt: state === "CLOSED" ? capturedAt : null, labels: { totalCount: labels.length, nodes: labels.map((name) => ({ name })) } });
const pages = [{ data: { repository: { nameWithOwner: "erweixin/langshift.dev", issues: { totalCount: 2, nodes: [issue(1, "OPEN", ["severity:P2"]), issue(2, "CLOSED", ["severity:P0"])], pageInfo: { hasNextPage: false, endCursor: null } } } } }];
const snapshot = normalizeGitHubIssuePages(pages, { repository: "erweixin/langshift.dev", sourceCommit, rcHash, capturedAt });
assert.equal(snapshot.querySha256, sha256(githubIssueQuery));
const policy = { policyVersion: "1.0.0", repository: "erweixin/langshift.dev", provider: "github-graphql", maximumSnapshotAgeHours: 24, severityLabels: { P0: "severity:P0", P1: "severity:P1", P2: "severity:P2", P3: "severity:P3" }, pullRequestsExcluded: true };
const build = (value = snapshot, dirty = false) => buildReleaseIssuesReport(value, policy, { snapshotPath: "snapshot.json", snapshotSha256: "c".repeat(64), generatedAt: "2026-01-01T01:00:00.000Z", worktreeDirty: dirty });
assert.equal(build().status, "passed");
assert.match(build().reportHash, /^[0-9a-f]{64}$/);
for (const [name, mutate, expected] of [
  ["P0 blocks", (copy) => { copy.issues[0].labels = ["severity:P0"]; }, "failed"],
  ["P1 blocks", (copy) => { copy.issues[0].labels = ["severity:P1"]; }, "failed"],
  ["unclassified blocks", (copy) => { copy.issues[0].labels = []; }, "failed"],
  ["multiple severities block", (copy) => { copy.issues[0].labels = ["severity:P2", "severity:P3"]; }, "failed"],
]) {
  const copy = structuredClone(snapshot); mutate(copy); assert.equal(build(copy).status, expected, name);
}
assert.equal(build(snapshot, true).status, "failed");
assert.throws(() => buildReleaseIssuesReport(snapshot, policy, { snapshotPath: "snapshot.json", snapshotSha256: "c".repeat(64), generatedAt: "2026-01-03T00:00:00.000Z" }), /stale/);
const incomplete = structuredClone(pages); incomplete[0].data.repository.issues.pageInfo.hasNextPage = true;
assert.throws(() => normalizeGitHubIssuePages(incomplete, { repository: "erweixin/langshift.dev", sourceCommit, rcHash, capturedAt }), /incomplete/);
console.log("Stage 6 release issue self-test passed: complete pagination accepted; P0/P1/unclassified/multi-label/stale/dirty/incomplete rejected");
