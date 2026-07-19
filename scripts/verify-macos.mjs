import { createHash } from "node:crypto";
import { execFileSync, spawnSync } from "node:child_process";
import { mkdir, writeFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const output = resolve(root, process.env.LITES_MACOS_VERIFICATION_REPORT ?? ".tmp/verification/macos-engineering-rc.json");
const sourceCommit = execFileSync("git", ["rev-parse", "HEAD"], { cwd: root, encoding: "utf8" }).trim();
const initialStatus = execFileSync("git", ["status", "--porcelain=v1", "--untracked-files=all"], { cwd: root, encoding: "utf8" }).trim();
const platform = process.platform;
const checks = [];
const verificationReportEnv = { LITES_GATE_REPORT_ROOT: ".tmp/verification/runtime-gate-reports" };
const releaseCandidateHash = createHash("sha256").update(`lites-engineering-rc:${sourceCommit}`).digest("hex");

function run(id, command, args, extraEnv = {}) {
  const startedAt = new Date().toISOString();
  const result = spawnSync(command, args, { cwd: root, env: { ...process.env, ...extraEnv }, stdio: "inherit" });
  checks.push({ id, result: result.status === 0 ? "passed" : "failed", exitCode: result.status ?? 1, startedAt, completedAt: new Date().toISOString() });
  return result.status === 0;
}

if (platform !== "darwin") checks.push({ id: "macos-runner", result: "failed", exitCode: 1, details: `expected darwin, received ${platform}` });
else checks.push({ id: "macos-runner", result: "passed", exitCode: 0, details: platform });
if (initialStatus) checks.push({ id: "clean-source", result: "failed", exitCode: 1, details: "Engineering RC evidence cannot be generated from a dirty worktree." });
else checks.push({ id: "clean-source", result: "passed", exitCode: 0, details: sourceCommit });

// A dirty tree can never produce Engineering RC evidence, but developers must
// still be able to execute every engineering check before asking for a commit.
// Keep clean-source as a hard failing check while collecting the remaining
// results instead of short-circuiting the entire gate.
if (platform === "darwin") {
  run("go-project-packages", "go", ["test", "./cmd/...", "./internal/..."]);
  run("web-lint", "npm", ["--prefix", "apps/web", "run", "lint"]);
  run("web-typecheck", "npm", ["--prefix", "apps/web", "run", "typecheck"]);
  run("web-components", "npm", ["--prefix", "apps/web", "test"]);
  run("web-build", "npm", ["--prefix", "apps/web", "run", "build"]);
  run("real-e2e-boundary", process.execPath, ["scripts/validate-real-e2e-boundary.mjs"]);
  run("observability-contract", process.execPath, ["scripts/validate-observability-contract.mjs"]);
  run("product-scope", process.execPath, ["scripts/validate-product-scope.mjs"]);
  run("product-scope-verifier-self-test", process.execPath, ["scripts/test-product-scope.mjs"]);
  run("stage0-traceability", process.execPath, ["scripts/verify-stage0-baseline.mjs", "--require-engineering-rc"]);
  run("stage0-verifier-self-test", process.execPath, ["scripts/test-stage0-baseline-verifier.mjs"]);
  run("engineering-rc-contract-self-test", process.execPath, ["scripts/test-engineering-rc-contract.mjs"]);
  run("current-contracts", process.execPath, ["scripts/lint-phase1-assets.mjs", "--current-engineering", "--report", ".tmp/verification/current-contract-lint.json"]);
  run("current-openapi-and-web-client", process.execPath, ["scripts/validate-current-openapi.mjs"]);
  run("stage3-contracts", process.execPath, ["scripts/lint-stage3-contract-amendment.mjs"], verificationReportEnv);
  run("behavior-contracts", process.execPath, ["scripts/lint-behavior-control-plane-contract.mjs"], verificationReportEnv);
  run("career-content-and-ai-contracts", process.execPath, ["scripts/lint-stage4-content-release.mjs"], { LITES_STAGE4_CONTENT_REPORT: ".tmp/verification/stage4-content.json" });
  run("enterprise-contracts", "npm", ["run", "contracts:lint:stage5"], verificationReportEnv);
  run("erasure-contracts", "npm", ["run", "contracts:lint:stage6"]);
  run("account-erasure-verifier-self-test", process.execPath, ["scripts/test-stage6-account-erasure-report.mjs"]);
  run("account-erasure-integration", "bash", ["scripts/stage6-account-erasure-gate.sh", sourceCommit, releaseCandidateHash], {
    LITES_ACCOUNT_ERASURE_ALLOW_DIRTY: "true",
    LITES_ACCOUNT_ERASURE_REPORT: ".tmp/verification/account-erasure-100.json",
  });
  run("database-http-integration", "bash", ["scripts/foundation-postgres-smoke.sh"]);
  run("database-contract", "bash", ["scripts/database-contract-smoke.sh"], { DATABASE_SMOKE_REPORT: ".tmp/verification/database-smoke.json" });
  run("no-demo-real-browser-journey", "bash", ["scripts/macos-product-stack.sh"]);
}

const finalStatus = execFileSync("git", ["status", "--porcelain=v1", "--untracked-files=all"], { cwd: root, encoding: "utf8" }).trim();
checks.push({ id: "source-unchanged", result: finalStatus === initialStatus ? "passed" : "failed", exitCode: finalStatus === initialStatus ? 0 : 1, details: finalStatus === initialStatus ? sourceCommit : "Verification mutated tracked source or evidence files." });
const failed = checks.filter((item) => item.result === "failed");
const base = {
  reportVersion: "1.0.0",
  kind: "macos-engineering-release-candidate",
  sourceCommit,
  worktreeDirty: Boolean(initialStatus),
  generatedAt: new Date().toISOString(),
  result: failed.length ? "failed" : "engineering_rc_passed",
  commercialGA: false,
  toolVersions: { node: process.version, testHarness: "verify-macos@1.0.0" },
  summary: { checks: checks.length, passed: checks.length - failed.length, failed: failed.length },
  checks,
};
const report = { ...base, reportHash: createHash("sha256").update(JSON.stringify(base)).digest("hex") };
await mkdir(dirname(output), { recursive: true });
await writeFile(output, `${JSON.stringify(report, null, 2)}\n`);
console.log(`verify:macos result=${report.result} passed=${report.summary.passed}/${report.summary.checks} report=${output}`);
if (failed.length) process.exitCode = 1;
