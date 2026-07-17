import assert from "node:assert/strict";
import { generateKeyPairSync } from "node:crypto";
import { approvalPayload, calculateApprovalScopeHash, calculateReportHash, signStage6Approval, verifyStage6Approvals } from "./stage6-approval-crypto.mjs";

const roles = ["product", "engineering", "security", "operations", "release"];
const privateKeys = new Map();
const keys = roles.map((role, index) => {
  const pair = generateKeyPairSync("ed25519");
  const keyId = `key-${role}`;
  const approverId = `approver-${index + 1}`;
  privateKeys.set(keyId, pair.privateKey.export({ type: "pkcs8", format: "pem" }));
  return { keyId, approverId, roles: [role], publicKeyPem: pair.publicKey.export({ type: "spki", format: "pem" }), status: "active" };
});
const keyring = { keyringVersion: "1.0.0", status: "active", requiredRoles: { pilot: ["product", "research", "security", "privacy", "qa"], final: roles }, keys };
const scope = { reportVersion: "1.0.0", kind: "final-approval", status: "approved", rcHash: "a".repeat(64), evidenceManifestHash: "b".repeat(64), generatedAt: "2026-01-01T00:00:00.000Z" };
let report = { ...scope, approvalScopeHash: "", signatures: [] };
report.approvalScopeHash = calculateApprovalScopeHash(report);
report.signatures = keys.map((key, index) => signStage6Approval(report, { approvalType: "final", role: roles[index], approverId: key.approverId, keyId: key.keyId, privateKeyPem: privateKeys.get(key.keyId), signedAt: `2026-01-01T00:0${index}:00.000Z` }));
report.reportHash = calculateReportHash(report);
assert.equal(verifyStage6Approvals(report, keyring, "final"), true);
assert.equal(approvalPayload(report, report.signatures[0]).rcHash, report.rcHash);
for (const [name, mutate] of [
  ["RC tamper", (copy) => { copy.rcHash = "c".repeat(64); }],
  ["evidence tamper", (copy) => { copy.evidenceManifestHash = "d".repeat(64); }],
  ["signature tamper", (copy) => { copy.signatures[0].signature.value = Buffer.from("invalid").toString("base64"); copy.reportHash = calculateReportHash(copy); }],
  ["duplicate approver", (copy) => { copy.signatures[1].approverId = copy.signatures[0].approverId; copy.reportHash = calculateReportHash(copy); }],
  ["revoked key", (_copy, ring) => { ring.keys[0].status = "revoked"; }],
]) {
  const copy = structuredClone(report);
  const ring = structuredClone(keyring);
  mutate(copy, ring);
  assert.equal(verifyStage6Approvals(copy, ring, "final"), false, name);
}
assert.equal(verifyStage6Approvals(report, { ...keyring, status: "unassigned", keys: [] }, "final"), false);
console.log("Stage 6 approval crypto self-test passed: five roles accepted; RC/evidence/signature/identity/revocation/unassigned failures rejected");
