import assert from "node:assert/strict";
import { validateLegalGovernance } from "./verify-legal-governance.mjs";

const base = { policyVersion: "1.0.0", projectName: "Lites", repository: "https://github.com/erweixin/langshift.dev", communityLicense: "AGPL-3.0-only", commercialLicensingEnabled: true, claAcceptanceMethod: "signed_document", securityDisclosureChannel: "https://github.com/erweixin/langshift.dev/security/advisories/new" };
assert.deepEqual(validateLegalGovernance({ ...base, status: "identity_required", rightsHolderLegalName: null, rightsHolderAddress: null, governingLaw: null, claSubmissionAddress: null }), { structural: true, active: false, releaseEligible: false });
const active = { ...base, status: "active", rightsHolderLegalName: "Example Rights Holder Ltd.", rightsHolderAddress: "1 Example Road", governingLaw: "Example jurisdiction", claSubmissionAddress: "cla@example.invalid" };
assert.equal(validateLegalGovernance(active, { requireActive: true }).releaseEligible, true);
assert.throws(() => validateLegalGovernance({ ...active, rightsHolderLegalName: null }, { requireActive: true }));
assert.throws(() => validateLegalGovernance({ ...base, status: "identity_required", rightsHolderLegalName: "partial", rightsHolderAddress: null, governingLaw: null, claSubmissionAddress: null }));
console.log("legal governance self-test passed: explicit inactive accepted for development; partial and GA-inactive identities rejected");
