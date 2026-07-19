import { createHash } from "node:crypto";
import { execFileSync, spawnSync } from "node:child_process";
import { mkdir, writeFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { probeDockerDaemon } from "./check-docker-daemon.mjs";

const root = resolve(import.meta.dirname, "..");
const output = resolve(root, process.env.LITES_PRODUCTION_CONTRACT_REPORT ?? ".tmp/verification/production-contracts.json");
const sourceCommit = execFileSync("git", ["rev-parse", "HEAD"], { cwd: root, encoding: "utf8" }).trim();
const worktreeDirty = execFileSync("git", ["status", "--porcelain=v1", "--untracked-files=all"], { cwd: root, encoding: "utf8" }).trim().length > 0;
const checks = [
  ["observability-contract", process.execPath, ["scripts/validate-observability-contract.mjs"], "static_contract"],
  ["helm-render-policy", "bash", ["scripts/stage2-helm-gate.sh"], "render_contract"],
  ["opentofu-render-policy", "bash", ["scripts/stage2-tofu-gate.sh"], "render_contract"],
  ["linux-runtime-host-contract", process.execPath, ["scripts/test-linux-runtime-host-contract.mjs"], "validator_fixture"],
  ["reference-capacity-chart", "bash", ["scripts/stage3-reference-capacity-chart-gate.sh"], "render_contract"],
  ["reference-provider-chart", "bash", ["scripts/stage3-reference-provider-chart-gate.sh"], "render_contract"],
  ["capacity-validator", process.execPath, ["scripts/test-stage6-capacity-report.mjs"], "validator_fixture"],
  ["recovery-validator", process.execPath, ["scripts/test-stage6-recovery-report.mjs"], "validator_fixture"],
  ["deployment-validator", process.execPath, ["scripts/test-stage6-deployment-report.mjs"], "validator_fixture"],
  ["penetration-validator", process.execPath, ["scripts/test-stage6-penetration-report.mjs"], "validator_fixture"],
  ["profile-eval-validator", process.execPath, ["scripts/test-stage6-profile-eval-report.mjs"], "validator_fixture"],
  ["pilot-validator", process.execPath, ["scripts/test-stage6-pilot-report.mjs"], "validator_fixture"],
  ["readiness-rejects-forgery", process.execPath, ["scripts/test-stage6-readiness-verifier.mjs"], "validator_fixture"],
  ["final-approval-rejects-forgery", process.execPath, ["scripts/test-stage6-final-approval.mjs"], "validator_fixture"],
  ["release-bundle-validator", process.execPath, ["scripts/test-ga-release-tools.mjs"], "validator_fixture"],
];

const dockerProbe = probeDockerDaemon({ cwd: root, env: process.env, timeoutMs: 15_000 });
const dockerAvailable = dockerProbe.status === 0;
const dockerFailure = dockerProbe.error?.code === "ETIMEDOUT"
  ? "docker_probe_timeout"
  : dockerProbe.error?.code ?? dockerProbe.stderr?.trim().split("\n").at(-1) ?? "docker_unavailable";

const results = [];
for (const [id, command, args, evidenceKind] of checks) {
  const startedAt = new Date().toISOString();
  if (evidenceKind === "render_contract" && !dockerAvailable) {
    results.push({ id, evidenceKind, result: "failed", exitCode: dockerProbe.status ?? 1, failureReason: dockerFailure, startedAt, completedAt: new Date().toISOString() });
    continue;
  }
  const result = spawnSync(command, args, { cwd: root, env: process.env, stdio: "inherit", timeout: evidenceKind === "render_contract" ? 180_000 : 120_000 });
  const failureReason = result.error?.code === "ETIMEDOUT" ? "verification_timeout" : result.error?.code;
  results.push({ id, evidenceKind, result: result.status === 0 ? "fixture_validated" : "failed", exitCode: result.status ?? 1, ...(failureReason ? { failureReason } : {}), startedAt, completedAt: new Date().toISOString() });
}
const failed = results.filter((item) => item.result === "failed");
const base = {
  reportVersion: "1.0.0",
  kind: "production-contract-verification",
  sourceCommit,
  worktreeDirty,
  generatedAt: new Date().toISOString(),
  result: failed.length ? "failed" : "fixture_validated",
  productionClaims: false,
  toolVersions: { node: process.version, testHarness: "verify-production-contracts@1.2.0", docker: dockerAvailable ? dockerProbe.stdout.trim() : null },
  scope: "Schema, render, policy and rejecting-validator behavior only; no Linux isolation, HA, capacity, recovery, penetration, pilot, SLO, RPO or RTO result is asserted.",
  summary: { checks: results.length, fixtureValidated: results.length - failed.length, failed: failed.length },
  results,
};
const report = { ...base, reportHash: createHash("sha256").update(JSON.stringify(base)).digest("hex") };
await mkdir(dirname(output), { recursive: true });
await writeFile(output, `${JSON.stringify(report, null, 2)}\n`);
console.log(`production contracts: result=${report.result} fixture_validated=${report.summary.fixtureValidated}/${report.summary.checks} report=${output}`);
if (failed.length) process.exitCode = 1;
