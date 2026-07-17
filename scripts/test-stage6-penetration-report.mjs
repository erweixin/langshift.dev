import assert from "node:assert/strict";
import { buildPenetrationReport } from "./write-stage6-penetration-report.mjs";

const requiredScope = ["public_app", "admin_console", "api", "realtime", "tenant_isolation", "auth_session", "share_privacy", "llm_prompt_tool", "provider_egress_ssrf", "runtime_sandbox", "secrets_byok", "side_effect_approval", "recovery_data_integrity", "account_erasure"];
const evidence = { schemaVersion: "1.0.0", engagementId: "00000000-0000-4000-8000-000000000001", provider: { legalName: "Independent Security Laboratory", registrationJurisdiction: "Example jurisdiction", contractSha256: "1".repeat(64), independenceAttestationSha256: "2".repeat(64) }, target: { sourceCommit: "a".repeat(40), rcHash: "b".repeat(64), imageLockSha256: "3".repeat(64), environmentFingerprint: "4".repeat(64) }, startedAt: "2026-01-01T00:00:00.000Z", completedAt: "2026-01-10T00:00:00.000Z", testerCount: 2, methodologies: ["authenticated application assessment", "infrastructure and isolation assessment"], scope: requiredScope, providerReportSha256: "5".repeat(64), scopeArtifactSha256: "6".repeat(64), findings: [{ id: "PEN-1", title: "Resolved tenant boundary defect", severity: "high", status: "resolved", riskClasses: ["cross_tenant"], riskAccepted: false, retestEvidenceSha256: "7".repeat(64), closedAt: "2026-01-09T00:00:00.000Z" }, { id: "PEN-2", title: "Open low availability note", severity: "low", status: "open", riskClasses: ["availability"], riskAccepted: true, retestEvidenceSha256: null, closedAt: null }] };
const options = { evidencePath: "evidence.json", evidenceSha256: "8".repeat(64), generatedAt: "2026-01-11T00:00:00.000Z", worktreeDirty: false };
const build = (mutate = () => {}, changes = {}) => { const copy = structuredClone(evidence); mutate(copy); return buildPenetrationReport(copy, { ...options, ...changes }); };
assert.equal(build().status, "passed");
assert.match(build().reportHash, /^[0-9a-f]{64}$/);
for (const [name, mutate] of [
  ["critical open", (copy) => { copy.findings[0].severity = "critical"; copy.findings[0].status = "open"; copy.findings[0].retestEvidenceSha256 = null; copy.findings[0].closedAt = null; }],
  ["high open", (copy) => { copy.findings[0].status = "open"; copy.findings[0].retestEvidenceSha256 = null; copy.findings[0].closedAt = null; }],
  ["forbidden medium open", (copy) => { copy.findings[0].severity = "medium"; copy.findings[0].status = "open"; copy.findings[0].retestEvidenceSha256 = null; copy.findings[0].closedAt = null; }],
  ["forbidden accepted", (copy) => { copy.findings[0].riskAccepted = true; }],
]) assert.equal(build(mutate).status, "failed", name);
assert.equal(build(() => {}, { worktreeDirty: true }).status, "failed");
assert.throws(() => build((copy) => { copy.scope.pop(); }), /scope/);
assert.throws(() => build((copy) => { copy.findings[0].retestEvidenceSha256 = null; }), /retest/);
assert.throws(() => build((copy) => { copy.provider.independenceAttestationSha256 = null; }), /metadata/);
console.log("Stage 6 penetration self-test passed: complete independent scope accepted; critical/high/forbidden/risk-acceptance/dirty/scope/retest mutations rejected");
