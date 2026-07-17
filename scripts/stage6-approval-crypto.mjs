import { createHash, sign, verify } from "node:crypto";

const sha256 = (value) => createHash("sha256").update(JSON.stringify(value)).digest("hex");
const hex64 = /^[0-9a-f]{64}$/;
const approvalScope = (report) => {
  const { approvalScopeHash: _scope, signatures: _signatures, reportHash: _report, ...scope } = report;
  return scope;
};
export const calculateApprovalScopeHash = (report) => sha256(approvalScope(report));
export const calculateReportHash = (report) => {
  const { reportHash: _report, ...base } = report;
  return sha256(base);
};
export const approvalPayload = (report, signature) => ({
  approvalType: signature.approvalType,
  role: signature.role,
  approverId: signature.approverId,
  keyId: signature.keyId,
  decision: signature.decision,
  rcHash: report.rcHash,
  evidenceManifestHash: report.evidenceManifestHash,
  approvalScopeHash: report.approvalScopeHash,
  signedAt: signature.signedAt,
});

export function signStage6Approval(report, { approvalType, role, approverId, keyId, privateKeyPem, signedAt = new Date().toISOString() }) {
  if (!hex64.test(report.rcHash ?? "") || !hex64.test(report.evidenceManifestHash ?? "") || report.approvalScopeHash !== calculateApprovalScopeHash(report)) throw new Error("approval scope is invalid");
  const signatureBase = { approvalType, role, approverId, keyId, decision: "approved", signedAt };
  const value = sign(null, Buffer.from(JSON.stringify(approvalPayload(report, signatureBase))), privateKeyPem).toString("base64");
  return { ...signatureBase, signature: { kind: "ed25519", value } };
}

export function verifyStage6Approvals(report, keyring, approvalType) {
  try {
    const requiredRoles = keyring.requiredRoles?.[approvalType];
    if (keyring.keyringVersion !== "1.0.0" || keyring.status !== "active" || !Array.isArray(requiredRoles) || requiredRoles.length !== 5 || new Set(requiredRoles).size !== 5 || !Array.isArray(keyring.keys) || keyring.keys.length < 5) return false;
    if (!hex64.test(report.rcHash ?? "") || !hex64.test(report.evidenceManifestHash ?? "") || report.approvalScopeHash !== calculateApprovalScopeHash(report) || report.reportHash !== calculateReportHash(report)) return false;
    if (!Array.isArray(report.signatures) || report.signatures.length !== requiredRoles.length || new Set(report.signatures.map((item) => item.role)).size !== requiredRoles.length || new Set(report.signatures.map((item) => item.approverId)).size !== requiredRoles.length || new Set(report.signatures.map((item) => item.keyId)).size !== requiredRoles.length) return false;
    if (JSON.stringify(report.signatures.map((item) => item.role).sort()) !== JSON.stringify([...requiredRoles].sort())) return false;
    return report.signatures.every((item) => {
      const key = keyring.keys.find((candidate) => candidate.keyId === item.keyId && candidate.approverId === item.approverId && candidate.status === "active" && candidate.roles?.includes(item.role));
      return item.approvalType === approvalType && item.decision === "approved" && item.signature?.kind === "ed25519" && key && Number.isFinite(Date.parse(item.signedAt)) && verify(null, Buffer.from(JSON.stringify(approvalPayload(report, item))), key.publicKeyPem, Buffer.from(item.signature.value, "base64"));
    });
  } catch {
    return false;
  }
}
