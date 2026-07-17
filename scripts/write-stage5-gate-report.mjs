import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const option = (name) => { const index = process.argv.indexOf(name); return index >= 0 ? process.argv[index + 1] : undefined; };
const sourceCommit = execFileSync("git", ["rev-parse", "HEAD"], { cwd: root, encoding: "utf8" }).trim();
const worktreeDirty = execFileSync("git", ["status", "--porcelain=v1"], { cwd: root, encoding: "utf8" }).trim().length > 0;
const hash = (value) => createHash("sha256").update(JSON.stringify(value)).digest("hex");
const evidenceFiles = [
  "enterprise-privacy-contract.json",
  "contract-control-plane-contract.json",
  "session-reauthentication-contract.json",
  "support-case-control-plane-contract.json",
  "public-status-control-plane-contract.json",
  "enterprise-completion-contract.json",
  "enterprise-browser-journey.json",
  "membership-csv-replay.json",
  "ledger-model-reconciliation.json",
  "management-control-plane-security.json",
  "private-delivery-definition.json",
];
const evidence = [];
for (const file of evidenceFiles) {
  const raw = await readFile(resolve(root, "gate-reports/stage-5", file), "utf8");
  const report = JSON.parse(raw);
  evidence.push({ file, kind: report.kind, status: report.status, generatedAt: report.generatedAt, reportHash: report.reportHash, fileHash: createHash("sha256").update(raw).digest("hex") });
}
const manifest = JSON.parse(await readFile(resolve(root, "deploy/migrations/manifest.json"), "utf8"));
const latest = manifest.migrations.at(-1);
const macOS = await readFile(resolve(root, "docs/development-macos.md"), "utf8");
const results = [];
const check = (id, passed, details) => results.push({ id, status: passed ? "passed" : "failed", details });
check("EVIDENCE-COMPLETE", evidence.length === evidenceFiles.length && evidence.every((item) => item.status === "passed" && item.reportHash), "all Stage 5 contract, browser, replay, reconciliation, security, and delivery reports pass");
check("MIGRATION-CURRENT", latest?.version === 87 && latest?.name === "product_content_rubric_activation", "database evidence is bound to migration 87");
check("MACOS-FIRECRACKER-EXEMPT", macOS.includes("Firecracker is intentionally outside the macOS local gate") && macOS.includes("production Linux runtime-host implementation remains"), "macOS development explicitly skips Firecracker while Linux production delivery remains in scope");
check("COMMERCIAL-EVIDENCE", evidence.some((item) => item.kind === "contract-control-plane-contract") && evidence.some((item) => item.kind === "private-delivery-definition") && evidence.some((item) => item.kind === "support-case-control-plane-contract"), "contract, entitlements, private delivery, and support evidence are present");
const failures = results.filter((result) => result.status === "failed");
const base = {
  reportVersion: "1.0.0",
  stage: 5,
  kind: "stage5-automated-gate",
  generatedAt: new Date().toISOString(),
  sourceCommit,
  worktreeDirty,
  status: failures.length ? "failed" : "passed",
  summary: { checks: results.length, passed: results.length - failures.length, failed: failures.length, evidenceReports: evidence.length },
  platformExceptions: [{ capability: "Firecracker local installation and validation", platform: "macOS development", status: "not_applicable", productionBoundary: "Linux runtime hosts remain required and validated by delivery evidence" }],
  migration: { version: latest?.version, name: latest?.name, manifestHash: hash(manifest) },
  evidence,
  results,
};
const report = { ...base, reportHash: hash(base) };
const target = resolve(root, option("--output") ?? "gate-reports/stage-5/stage5-automated-gate.json");
await mkdir(dirname(target), { recursive: true });
await writeFile(target, `${JSON.stringify(report, null, 2)}\n`);
console.log(`${report.status}: ${report.summary.passed}/${report.summary.checks} aggregate checks across ${evidence.length} reports; report ${report.reportHash}`);
if (failures.length) process.exitCode = 1;
