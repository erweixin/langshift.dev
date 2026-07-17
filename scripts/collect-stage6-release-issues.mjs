import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

export const githubIssueQuery = `query($owner: String!, $name: String!, $endCursor: String) {
  repository(owner: $owner, name: $name) {
    nameWithOwner
    issues(first: 100, after: $endCursor, orderBy: {field: UPDATED_AT, direction: ASC}) {
      totalCount
      nodes {
        id number title url state createdAt updatedAt closedAt
        labels(first: 100) { totalCount nodes { name } }
      }
      pageInfo { hasNextPage endCursor }
    }
  }
}`;

const sha256 = (value) => createHash("sha256").update(value).digest("hex");
const hex40 = /^[0-9a-f]{40}$/;
const hex64 = /^[0-9a-f]{64}$/;

export function normalizeGitHubIssuePages(pages, { repository, sourceCommit, rcHash, capturedAt }) {
  if (!Array.isArray(pages) || pages.length < 1 || !hex40.test(sourceCommit ?? "") || !hex64.test(rcHash ?? "") || !Number.isFinite(Date.parse(capturedAt))) throw new Error("invalid issue collection metadata");
  const expectedRepository = repository;
  const nodes = [];
  let totalCount = null;
  for (const [index, page] of pages.entries()) {
    const value = page?.data?.repository;
    const issues = value?.issues;
    if (value?.nameWithOwner !== expectedRepository || !Number.isInteger(issues?.totalCount) || !Array.isArray(issues?.nodes)) throw new Error("GitHub issue page is malformed or belongs to another repository");
    if (totalCount === null) totalCount = issues.totalCount;
    if (issues.totalCount !== totalCount) throw new Error("GitHub issue total changed during pagination");
    if (index < pages.length - 1 && issues.pageInfo?.hasNextPage !== true) throw new Error("unexpected non-final issue page");
    if (index === pages.length - 1 && issues.pageInfo?.hasNextPage !== false) throw new Error("issue pagination is incomplete");
    for (const issue of issues.nodes) {
      if (typeof issue.id !== "string" || !Number.isInteger(issue.number) || issue.number < 1 || typeof issue.title !== "string" || !/^https:\/\/github\.com\//.test(issue.url ?? "") || !["OPEN", "CLOSED"].includes(issue.state) || !Number.isFinite(Date.parse(issue.createdAt)) || !Number.isFinite(Date.parse(issue.updatedAt)) || !Array.isArray(issue.labels?.nodes) || issue.labels.totalCount !== issue.labels.nodes.length || issue.labels.nodes.some((label) => typeof label.name !== "string")) throw new Error(`GitHub issue ${issue?.number ?? "unknown"} is incomplete`);
      nodes.push({ nodeId: issue.id, number: issue.number, title: issue.title, url: issue.url, state: issue.state.toLowerCase(), createdAt: issue.createdAt, updatedAt: issue.updatedAt, closedAt: issue.closedAt, labels: issue.labels.nodes.map((label) => label.name).sort() });
    }
  }
  if (nodes.length !== totalCount || new Set(nodes.map((issue) => issue.nodeId)).size !== nodes.length || new Set(nodes.map((issue) => issue.number)).size !== nodes.length) throw new Error("issue pagination count or uniqueness check failed");
  nodes.sort((left, right) => left.number - right.number);
  return { schemaVersion: "1.0.0", provider: "github-graphql", repository, sourceCommit, rcHash, capturedAt: new Date(capturedAt).toISOString(), querySha256: sha256(githubIssueQuery), paginationComplete: true, pageCount: pages.length, totalCount, issues: nodes };
}

async function main() {
  const root = resolve(import.meta.dirname, "..");
  const args = new Map();
  for (let index = 2; index < process.argv.length; index += 2) args.set(process.argv[index], process.argv[index + 1]);
  const sourceCommit = args.get("--source-commit");
  const rcHash = args.get("--rc-hash");
  const output = args.get("--output");
  if (!output) throw new Error("--source-commit, --rc-hash and --output are required");
  const policy = JSON.parse(await readFile(resolve(root, "contracts/release/stage6-issue-inventory-policy.json"), "utf8"));
  const [owner, name] = policy.repository.split("/");
  const raw = execFileSync("gh", ["api", "graphql", "--paginate", "--slurp", "-F", `owner=${owner}`, "-F", `name=${name}`, "-f", `query=${githubIssueQuery}`], { cwd: root, encoding: "utf8", maxBuffer: 32 * 1024 * 1024 });
  const snapshot = normalizeGitHubIssuePages(JSON.parse(raw), { repository: policy.repository, sourceCommit, rcHash, capturedAt: new Date().toISOString() });
  const target = resolve(root, output);
  await mkdir(dirname(target), { recursive: true });
  await writeFile(target, `${JSON.stringify(snapshot, null, 2)}\n`);
  console.log(`release issue snapshot: repository=${snapshot.repository} issues=${snapshot.totalCount} pages=${snapshot.pageCount}`);
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) await main();
