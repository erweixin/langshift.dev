import { createHash } from "node:crypto";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

export const githubIssueRequestContract = JSON.stringify({
  method: "GET",
  endpoint: "https://api.github.com/repos/{repository}/issues",
  query: { state: "all", sort: "created", direction: "asc", per_page: 100, page: "monotonic" },
  accept: "application/vnd.github+json",
  apiVersion: "2022-11-28",
  pullRequestsExcluded: true,
});

const sha256 = (value) => createHash("sha256").update(value).digest("hex");
const hex40 = /^[0-9a-f]{40}$/;
const hex64 = /^[0-9a-f]{64}$/;

export function normalizeGitHubIssuePages(pages, { repository, sourceCommit, rcHash, capturedAt }) {
  if (!Array.isArray(pages) || pages.length < 1 || !/^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/.test(repository ?? "") || !hex40.test(sourceCommit ?? "") || !hex64.test(rcHash ?? "") || !Number.isFinite(Date.parse(capturedAt))) throw new Error("invalid issue collection metadata");
  const nodes = [];
  for (const [index, page] of pages.entries()) {
    if (page?.page !== index + 1 || !Array.isArray(page.items) || page.items.length > 100 || typeof page.hasNextPage !== "boolean") throw new Error("GitHub issue page is malformed or out of order");
    if (index < pages.length - 1 && page.hasNextPage !== true) throw new Error("unexpected non-final issue page");
    if (index === pages.length - 1 && page.hasNextPage !== false) throw new Error("issue pagination is incomplete");
    for (const issue of page.items) {
      if (issue?.pull_request) continue;
      if (typeof issue?.node_id !== "string" || !Number.isInteger(issue.number) || issue.number < 1 || typeof issue.title !== "string" || issue.html_url !== `https://github.com/${repository}/issues/${issue.number}` || !["open", "closed"].includes(issue.state) || !Number.isFinite(Date.parse(issue.created_at)) || !Number.isFinite(Date.parse(issue.updated_at)) || !Array.isArray(issue.labels) || issue.labels.some((label) => typeof label?.name !== "string")) throw new Error(`GitHub issue ${issue?.number ?? "unknown"} is incomplete`);
      nodes.push({ nodeId: issue.node_id, number: issue.number, title: issue.title, url: issue.html_url, state: issue.state, createdAt: issue.created_at, updatedAt: issue.updated_at, closedAt: issue.closed_at, labels: issue.labels.map((label) => label.name).sort() });
    }
  }
  if (new Set(nodes.map((issue) => issue.nodeId)).size !== nodes.length || new Set(nodes.map((issue) => issue.number)).size !== nodes.length) throw new Error("issue pagination count or uniqueness check failed");
  nodes.sort((left, right) => left.number - right.number);
  return { schemaVersion: "1.0.0", provider: "github-rest", repository, sourceCommit, rcHash, capturedAt: new Date(capturedAt).toISOString(), querySha256: sha256(githubIssueRequestContract), paginationComplete: true, pageCount: pages.length, totalCount: nodes.length, issues: nodes };
}

function hasNextPage(linkHeader) {
  if (!linkHeader) return false;
  return linkHeader.split(",").some((value) => /;\s*rel="next"\s*$/.test(value.trim()));
}

export async function fetchGitHubIssuePages(repository, { token = process.env.GITHUB_TOKEN ?? process.env.GH_TOKEN, fetchImpl = fetch } = {}) {
  const pages = [];
  for (let page = 1; ; page += 1) {
    const url = new URL(`https://api.github.com/repos/${repository}/issues`);
    for (const [key, value] of Object.entries({ state: "all", sort: "created", direction: "asc", per_page: "100", page: String(page) })) url.searchParams.set(key, value);
    const headers = { Accept: "application/vnd.github+json", "X-GitHub-Api-Version": "2022-11-28", "User-Agent": "lites-engineering-rc-issue-inventory" };
    if (token) headers.Authorization = `Bearer ${token}`;
    let response = await fetchImpl(url, { method: "GET", headers, redirect: "error" });
    if (response.status === 401 && token) {
      delete headers.Authorization;
      response = await fetchImpl(url, { method: "GET", headers, redirect: "error" });
    }
    if (!response.ok) throw new Error(`GitHub issue collection failed with HTTP ${response.status}`);
    const items = await response.json();
    if (!Array.isArray(items)) throw new Error("GitHub issue response is not an array");
    const next = hasNextPage(response.headers.get("link"));
    pages.push({ page, items, hasNextPage: next });
    if (!next) break;
    if (page >= 100) throw new Error("issue pagination exceeds the bounded 100-page contract");
  }
  return pages;
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
  const pages = await fetchGitHubIssuePages(policy.repository);
  const snapshot = normalizeGitHubIssuePages(pages, { repository: policy.repository, sourceCommit, rcHash, capturedAt: new Date().toISOString() });
  const target = resolve(root, output);
  await mkdir(dirname(target), { recursive: true });
  await writeFile(target, `${JSON.stringify(snapshot, null, 2)}\n`);
  console.log(`release issue snapshot: repository=${snapshot.repository} issues=${snapshot.totalCount} pages=${snapshot.pageCount} provider=${snapshot.provider}`);
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) await main();
