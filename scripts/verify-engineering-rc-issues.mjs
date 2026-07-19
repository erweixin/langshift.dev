import { createHash } from "node:crypto";
import { execFileSync, spawnSync } from "node:child_process";
import { mkdir, readFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const output = resolve(root, process.env.LITES_ENGINEERING_RC_ISSUES_REPORT ?? ".tmp/verification/engineering-rc-issues.json");
const snapshot = resolve(root, process.env.LITES_ENGINEERING_RC_ISSUES_SNAPSHOT ?? ".tmp/verification/engineering-rc-issues-snapshot.json");
const git = (...args) => execFileSync("git", args, { cwd: root, encoding: "utf8" }).trim();
const sourceCommit = git("rev-parse", "HEAD");
if (git("status", "--porcelain=v1", "--untracked-files=all")) throw new Error("release issue inventory requires a clean worktree");
const candidateBinding = createHash("sha256").update(`engineering-rc-issues:${sourceCommit}`).digest("hex");
await mkdir(dirname(output), { recursive: true });

run("scripts/collect-stage6-release-issues.mjs", ["--source-commit", sourceCommit, "--rc-hash", candidateBinding, "--output", relative(snapshot)]);
run("scripts/write-stage6-release-issues-report.mjs", ["--snapshot", relative(snapshot), "--output", relative(output)]);

const raw = await readFile(output, "utf8");
const report = JSON.parse(raw);
const suppliedHash = report.reportHash;
delete report.reportHash;
const expectedHash = createHash("sha256").update(JSON.stringify(report)).digest("hex");
if (suppliedHash !== expectedHash || report.status !== "passed" || report.sourceCommit !== sourceCommit || report.worktreeDirty !== false || report.rcHash !== candidateBinding || report.openP0 !== 0 || report.openP1 !== 0 || report.unclassifiedOpen !== 0) throw new Error("current release issue inventory did not pass Engineering RC policy");
console.log(`Engineering RC issues: openP0=${report.openP0} openP1=${report.openP1} unclassified=${report.unclassifiedOpen} hash=${suppliedHash}`);

function run(script, args) {
  const result = spawnSync(process.execPath, [script, ...args], { cwd: root, env: process.env, stdio: "inherit" });
  if (result.status !== 0) throw new Error(`${script} failed`);
}

function relative(path) {
  return path.startsWith(`${root}/`) ? path.slice(root.length + 1) : path;
}
