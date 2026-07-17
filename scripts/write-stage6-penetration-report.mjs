import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const sha256 = (value) => createHash("sha256").update(value).digest("hex");
const hex40 = /^[0-9a-f]{40}$/;
const hex64 = /^[0-9a-f]{64}$/;
const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;
const requiredScope = ["public_app", "admin_console", "api", "realtime", "tenant_isolation", "auth_session", "share_privacy", "llm_prompt_tool", "provider_egress_ssrf", "runtime_sandbox", "secrets_byok", "side_effect_approval", "recovery_data_integrity", "account_erasure"];
const forbiddenRiskClasses = ["cross_tenant", "secret_exposure", "unauthorized_side_effect", "data_corruption", "approval_bypass"];
const severities = ["critical", "high", "medium", "low", "informational"];

export function buildPenetrationReport(evidence, { evidencePath, evidenceSha256, generatedAt, worktreeDirty = false }) {
  if (evidence?.schemaVersion !== "1.0.0" || !uuid.test(evidence.engagementId ?? "") || typeof evidence.provider?.legalName !== "string" || evidence.provider.legalName.trim().length < 3 || typeof evidence.provider?.registrationJurisdiction !== "string" || evidence.provider.registrationJurisdiction.trim().length < 2 || ![evidence.provider.contractSha256, evidence.provider.independenceAttestationSha256, evidence.providerReportSha256, evidence.scopeArtifactSha256].every((value) => hex64.test(value ?? "")) || !hex40.test(evidence.target?.sourceCommit ?? "") || ![evidence.target?.rcHash, evidence.target?.imageLockSha256, evidence.target?.environmentFingerprint].every((value) => hex64.test(value ?? "")) || !Number.isFinite(Date.parse(evidence.startedAt)) || !Number.isFinite(Date.parse(evidence.completedAt)) || Date.parse(evidence.completedAt) <= Date.parse(evidence.startedAt) || !Array.isArray(evidence.scope) || JSON.stringify([...evidence.scope].sort()) !== JSON.stringify([...requiredScope].sort()) || new Set(evidence.scope).size !== requiredScope.length || !Array.isArray(evidence.methodologies) || evidence.methodologies.length < 2 || evidence.methodologies.some((value) => typeof value !== "string" || value.trim().length < 3) || !Number.isInteger(evidence.testerCount) || evidence.testerCount < 2 || !Array.isArray(evidence.findings) || !hex64.test(evidenceSha256 ?? "") || !Number.isFinite(Date.parse(generatedAt))) throw new Error("external penetration evidence metadata or scope is invalid");
  if (Date.parse(generatedAt) < Date.parse(evidence.completedAt)) throw new Error("penetration report generation precedes engagement completion");
  const ids = new Set();
  for (const finding of evidence.findings) {
    if (typeof finding.id !== "string" || finding.id.length < 1 || ids.has(finding.id) || typeof finding.title !== "string" || finding.title.length < 3 || !severities.includes(finding.severity) || !["open", "resolved", "not_applicable"].includes(finding.status) || !Array.isArray(finding.riskClasses) || finding.riskClasses.some((value) => ![...forbiddenRiskClasses, "availability", "integrity", "confidentiality", "other"].includes(value)) || typeof finding.riskAccepted !== "boolean") throw new Error("penetration finding is invalid or duplicated");
    ids.add(finding.id);
    if (finding.status === "resolved" && (!hex64.test(finding.retestEvidenceSha256 ?? "") || !Number.isFinite(Date.parse(finding.closedAt)) || Date.parse(finding.closedAt) > Date.parse(evidence.completedAt))) throw new Error("resolved penetration finding lacks valid retest closure evidence");
    if (finding.status !== "resolved" && (finding.retestEvidenceSha256 != null || finding.closedAt != null)) throw new Error("unresolved penetration finding claims closure evidence");
    if (finding.status === "not_applicable" && finding.riskAccepted) throw new Error("not-applicable findings cannot be recorded as risk acceptance");
  }
  const unresolvedCritical = evidence.findings.filter((finding) => finding.severity === "critical" && finding.status !== "resolved").length;
  const unresolvedHigh = evidence.findings.filter((finding) => finding.severity === "high" && finding.status !== "resolved").length;
  const forbiddenUnresolved = evidence.findings.filter((finding) => finding.status !== "resolved" && finding.riskClasses.some((risk) => forbiddenRiskClasses.includes(risk))).length;
  const forbiddenRiskAccepted = evidence.findings.filter((finding) => finding.riskAccepted && finding.riskClasses.some((risk) => forbiddenRiskClasses.includes(risk))).length;
  const passed = !worktreeDirty && unresolvedCritical === 0 && unresolvedHigh === 0 && forbiddenUnresolved === 0 && forbiddenRiskAccepted === 0;
  const count = (severity) => evidence.findings.filter((finding) => finding.severity === severity).length;
  const base = { reportVersion: "1.0.0", stage: 6, kind: "external-penetration-test", generatedAt: new Date(generatedAt).toISOString(), status: passed ? "passed" : "failed", sourceCommit: evidence.target.sourceCommit, worktreeDirty, rcHash: evidence.target.rcHash, engagementId: evidence.engagementId, externalProvider: { legalName: evidence.provider.legalName, registrationJurisdiction: evidence.provider.registrationJurisdiction, contractSha256: evidence.provider.contractSha256, independenceAttestationSha256: evidence.provider.independenceAttestationSha256 }, engagement: { startedAt: evidence.startedAt, completedAt: evidence.completedAt, testerCount: evidence.testerCount, methodologies: [...evidence.methodologies].sort(), scope: [...evidence.scope].sort(), providerReportSha256: evidence.providerReportSha256, scopeArtifactSha256: evidence.scopeArtifactSha256, imageLockSha256: evidence.target.imageLockSha256, environmentFingerprint: evidence.target.environmentFingerprint }, rawEvidence: { path: evidencePath, sha256: evidenceSha256, findings: evidence.findings.length }, findingCounts: Object.fromEntries(severities.map((severity) => [severity, count(severity)])), unresolvedCritical, unresolvedHigh, forbiddenUnresolved, forbiddenRiskAccepted, forbiddenRiskClasses, findings: evidence.findings.map((finding) => ({ id: finding.id, severity: finding.severity, status: finding.status, riskClasses: finding.riskClasses, riskAccepted: finding.riskAccepted, retestEvidenceSha256: finding.retestEvidenceSha256 ?? null })) };
  return { ...base, reportHash: sha256(JSON.stringify(base)) };
}

async function main() {
  const root = resolve(import.meta.dirname, "..");
  const args = new Map();
  for (let index = 2; index < process.argv.length; index += 2) args.set(process.argv[index], process.argv[index + 1]);
  const evidencePath = args.get("--evidence-json");
  const output = args.get("--output");
  if (!evidencePath || !output) throw new Error("--evidence-json and --output are required");
  const raw = await readFile(resolve(root, evidencePath));
  const evidence = JSON.parse(raw);
  const currentCommit = execFileSync("git", ["rev-parse", "HEAD"], { cwd: root, encoding: "utf8" }).trim();
  if (evidence.target?.sourceCommit !== currentCommit) throw new Error("penetration evidence is not bound to the current source commit");
  const worktreeDirty = execFileSync("git", ["status", "--porcelain=v1"], { cwd: root, encoding: "utf8" }).trim().length > 0;
  const report = buildPenetrationReport(evidence, { evidencePath, evidenceSha256: sha256(raw), generatedAt: new Date().toISOString(), worktreeDirty });
  const target = resolve(root, output);
  await mkdir(dirname(target), { recursive: true });
  await writeFile(target, `${JSON.stringify(report, null, 2)}\n`);
  console.log(`${report.status}: critical=${report.unresolvedCritical} high=${report.unresolvedHigh} forbiddenOpen=${report.forbiddenUnresolved} forbiddenAccepted=${report.forbiddenRiskAccepted} report=${report.reportHash}`);
  if (report.status !== "passed") process.exitCode = 1;
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) await main();
