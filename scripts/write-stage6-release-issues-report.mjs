import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { githubIssueQuery } from "./collect-stage6-release-issues.mjs";

const sha256 = (value) => createHash("sha256").update(value).digest("hex");
const hex40 = /^[0-9a-f]{40}$/;
const hex64 = /^[0-9a-f]{64}$/;

export function buildReleaseIssuesReport(snapshot, policy, { snapshotPath, snapshotSha256, generatedAt, worktreeDirty = false }) {
  if (policy?.policyVersion !== "1.0.0" || policy.provider !== "github-graphql" || policy.pullRequestsExcluded !== true || !Number.isInteger(policy.maximumSnapshotAgeHours) || policy.maximumSnapshotAgeHours < 1 || policy.repository !== snapshot?.repository || snapshot.schemaVersion !== "1.0.0" || snapshot.provider !== policy.provider || !hex40.test(snapshot.sourceCommit ?? "") || !hex64.test(snapshot.rcHash ?? "") || snapshot.querySha256 !== sha256(githubIssueQuery) || snapshot.paginationComplete !== true || !Number.isInteger(snapshot.pageCount) || snapshot.pageCount < 1 || !Array.isArray(snapshot.issues) || snapshot.totalCount !== snapshot.issues.length || !hex64.test(snapshotSha256 ?? "") || !Number.isFinite(Date.parse(generatedAt)) || !Number.isFinite(Date.parse(snapshot.capturedAt))) throw new Error("release issue snapshot or policy is invalid");
  const ageMs = Date.parse(generatedAt) - Date.parse(snapshot.capturedAt);
  if (ageMs < 0 || ageMs > policy.maximumSnapshotAgeHours * 60 * 60 * 1000) throw new Error("release issue snapshot is stale or from the future");
  if (new Set(snapshot.issues.map((issue) => issue.nodeId)).size !== snapshot.issues.length || new Set(snapshot.issues.map((issue) => issue.number)).size !== snapshot.issues.length) throw new Error("release issue inventory contains duplicates");
  const severityEntries = Object.entries(policy.severityLabels);
  const open = snapshot.issues.filter((issue) => issue.state === "open").map((issue) => {
    if (typeof issue.nodeId !== "string" || !Number.isInteger(issue.number) || typeof issue.title !== "string" || !/^https:\/\/github\.com\//.test(issue.url ?? "") || !Array.isArray(issue.labels) || !Number.isFinite(Date.parse(issue.updatedAt))) throw new Error("release issue entry is invalid");
    const severities = severityEntries.filter(([, label]) => issue.labels.includes(label)).map(([severity]) => severity);
    return { number: issue.number, nodeId: issue.nodeId, title: issue.title, url: issue.url, updatedAt: issue.updatedAt, severity: severities.length === 1 ? severities[0] : "unclassified" };
  });
  const count = (severity) => open.filter((issue) => issue.severity === severity).length;
  const openP0 = count("P0");
  const openP1 = count("P1");
  const unclassifiedOpen = count("unclassified");
  const passed = !worktreeDirty && openP0 === 0 && openP1 === 0 && unclassifiedOpen === 0;
  const base = { reportVersion: "1.0.0", stage: 6, kind: "release-issues", generatedAt: new Date(generatedAt).toISOString(), status: passed ? "passed" : "failed", sourceCommit: snapshot.sourceCommit, worktreeDirty, rcHash: snapshot.rcHash, repository: snapshot.repository, policyVersion: policy.policyVersion, snapshot: { path: snapshotPath, sha256: snapshotSha256, capturedAt: snapshot.capturedAt, maximumAgeHours: policy.maximumSnapshotAgeHours, provider: snapshot.provider, querySha256: snapshot.querySha256, paginationComplete: true, pages: snapshot.pageCount, totalIssues: snapshot.totalCount }, openP0, openP1, unclassifiedOpen, openCounts: { P0: openP0, P1: openP1, P2: count("P2"), P3: count("P3"), unclassified: unclassifiedOpen, total: open.length }, openIssues: open };
  return { ...base, reportHash: sha256(JSON.stringify(base)) };
}

async function main() {
  const root = resolve(import.meta.dirname, "..");
  const args = new Map();
  for (let index = 2; index < process.argv.length; index += 2) args.set(process.argv[index], process.argv[index + 1]);
  const snapshotPath = args.get("--snapshot");
  const output = args.get("--output");
  if (!snapshotPath || !output) throw new Error("--snapshot and --output are required");
  const raw = await readFile(resolve(root, snapshotPath));
  const snapshot = JSON.parse(raw);
  const policy = JSON.parse(await readFile(resolve(root, "contracts/release/stage6-issue-inventory-policy.json"), "utf8"));
  const currentCommit = execFileSync("git", ["rev-parse", "HEAD"], { cwd: root, encoding: "utf8" }).trim();
  if (snapshot.sourceCommit !== currentCommit) throw new Error("release issue snapshot is not bound to the current source commit");
  const worktreeDirty = execFileSync("git", ["status", "--porcelain=v1"], { cwd: root, encoding: "utf8" }).trim().length > 0;
  const report = buildReleaseIssuesReport(snapshot, policy, { snapshotPath, snapshotSha256: sha256(raw), generatedAt: new Date().toISOString(), worktreeDirty });
  const target = resolve(root, output);
  await mkdir(dirname(target), { recursive: true });
  await writeFile(target, `${JSON.stringify(report, null, 2)}\n`);
  console.log(`${report.status}: openP0=${report.openP0} openP1=${report.openP1} unclassified=${report.unclassifiedOpen} report=${report.reportHash}`);
  if (report.status !== "passed") process.exitCode = 1;
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) await main();
