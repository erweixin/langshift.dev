import { execFileSync } from "node:child_process";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { calculateApprovalScopeHash, calculateReportHash } from "./stage6-approval-crypto.mjs";
import { calculateFinalEvidenceManifestHash, requiredFinalEvidencePaths, sha256 } from "./stage6-evidence-manifest.mjs";
import { validateLegalGovernance } from "./verify-legal-governance.mjs";

const hex40 = /^[0-9a-f]{40}$/;
const hex64 = /^[0-9a-f]{64}$/;
const without = (value, key) => { const copy = { ...value }; delete copy[key]; return copy; };
const reportKeys = ["stage1", "stage2", "stage3", "stage4", "stage5", "penetration", "profile_route_planner", "profile_daily_planner", "profile_coach", "profile_evaluator", "profile_artifact_builder", "capacity", "recovery", "deployment_compose", "deployment_helm_tofu", "deployment_official_cloud", "pilot", "pilot_equivalence"];

export function buildFinalApprovalReport({ sourceCommit, rcHash, artifacts, reportSummaries, legalActive }, { generatedAt, worktreeDirty = false, layout }) {
  const requiredPaths = requiredFinalEvidencePaths(layout);
  if (!hex40.test(sourceCommit ?? "") || !hex64.test(rcHash ?? "") || !Array.isArray(artifacts) || artifacts.length !== requiredPaths.length || JSON.stringify(artifacts.map((item) => item.path).sort()) !== JSON.stringify(requiredPaths) || new Set(artifacts.map((item) => item.path)).size !== artifacts.length || artifacts.some((item) => !hex64.test(item.sha256 ?? "") || !Number.isInteger(item.sizeBytes) || item.sizeBytes < 1) || !Array.isArray(reportSummaries) || JSON.stringify(reportSummaries.map((item) => item.key).sort()) !== JSON.stringify([...reportKeys].sort()) || reportSummaries.some((item) => item.statusValid !== true || item.reportHashValid !== true || item.signaturePresent !== true || item.bindingValid !== true) || legalActive !== true || !Number.isFinite(Date.parse(generatedAt))) throw new Error("final approval evidence is incomplete, invalid, unsigned, or not source/RC bound");
  const evidence = calculateFinalEvidenceManifestHash({ sourceCommit, rcHash, artifacts });
  const base = { reportVersion: "1.0.0", stage: 6, kind: "final-approval", generatedAt: new Date(generatedAt).toISOString(), status: worktreeDirty ? "failed" : "approved", sourceCommit, worktreeDirty, rcHash, evidenceManifestHash: evidence.hash, evidenceCount: evidence.manifest.artifacts.length, evidence: evidence.manifest.artifacts, verifiedReports: reportSummaries.map((item) => ({ key: item.key, path: item.path, status: item.status, reportHash: item.reportHash })).sort((left, right) => left.key.localeCompare(right.key)), legalGovernanceActive: true, signatures: [] };
  const report = { ...base, approvalScopeHash: calculateApprovalScopeHash(base) };
  report.reportHash = calculateReportHash(report);
  return report;
}

async function main() {
  const root = resolve(import.meta.dirname, "..");
  const layout = JSON.parse(await readFile(resolve(root, "contracts/release/stage6-evidence-layout.json"), "utf8"));
  const currentCommit = execFileSync("git", ["rev-parse", "HEAD"], { cwd: root, encoding: "utf8" }).trim();
  const worktreeDirty = execFileSync("git", ["status", "--porcelain=v1"], { cwd: root, encoding: "utf8" }).trim().length > 0;
  const rcRaw = await readFile(resolve(root, "release-candidates/current/ga-release-candidate.json"));
  const rc = JSON.parse(rcRaw);
  if (rc.schemaVersion !== "1.0.0" || rc.releaseKind !== "ga_release_candidate" || rc.immutable !== true || rc.sourceCommit !== currentCommit || !hex64.test(rc.releaseCandidateHash ?? "") || sha256(JSON.stringify(without(rc, "releaseCandidateHash"))) !== rc.releaseCandidateHash) throw new Error("installed GA RC is invalid or does not match HEAD");
  const legalConfig = JSON.parse(await readFile(resolve(root, "config/legal-governance.json"), "utf8"));
  const legalActive = validateLegalGovernance(legalConfig, { requireActive: true }).releaseEligible;
  const requiredPaths = requiredFinalEvidencePaths(layout);
  const artifacts = await Promise.all(requiredPaths.map(async (path) => { const raw = await readFile(resolve(root, path)); return { path, sha256: sha256(raw), sizeBytes: raw.length }; }));
  const reportSummaries = [];
  for (const key of reportKeys) {
    const path = `${layout.root}/${layout.reports[key]}`;
    const signaturePath = path.replace(/\.json$/, layout.signatureSuffix);
    const [raw, signature] = await Promise.all([readFile(resolve(root, path)), readFile(resolve(root, signaturePath))]);
    const value = JSON.parse(raw);
    const expectedStatus = key === "pilot" ? "approved" : "passed";
    const statusValid = value.status === expectedStatus;
    const reportHashValid = hex64.test(value.reportHash ?? "") && sha256(JSON.stringify(without(value, "reportHash"))) === value.reportHash;
    const sourceBound = value.sourceCommit === currentCommit || key === "stage4" && Boolean(value.pilotSnapshotHash) || key === "pilot" && hex40.test(value.sourceCommit ?? "");
    const rcBound = ["stage1", "stage2", "stage3", "stage4", "stage5"].includes(key) ? true : value.rcHash === rc.releaseCandidateHash;
    const cleanBound = value.worktreeDirty !== true;
    reportSummaries.push({ key, path, status: value.status, reportHash: value.reportHash, statusValid, reportHashValid, signaturePresent: signature.length > 0, bindingValid: sourceBound && rcBound && cleanBound });
  }
  const report = buildFinalApprovalReport({ sourceCommit: currentCommit, rcHash: rc.releaseCandidateHash, artifacts, reportSummaries, legalActive }, { generatedAt: new Date().toISOString(), worktreeDirty, layout });
  const target = resolve(root, `${layout.root}/${layout.reports.final_approval}`);
  await mkdir(dirname(target), { recursive: true });
  await writeFile(target, `${JSON.stringify(report, null, 2)}\n`);
  console.log(`${report.status}: evidence=${report.evidenceCount} reports=${report.verifiedReports.length} report=${report.reportHash}`);
  if (report.status !== "approved") process.exitCode = 1;
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) await main();
