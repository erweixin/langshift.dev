#!/usr/bin/env node
import { createHash } from "node:crypto";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import path from "node:path";

const args = Object.fromEntries(process.argv.slice(2).reduce((pairs, value, index, all) => index % 2 === 0 ? [...pairs, [value.replace(/^--/, ""), all[index + 1]]] : pairs, []));
for (const key of ["unit", "integration", "source-commit", "output"]) if (!args[key]) throw new Error(`--${key} is required`);
if (!/^[0-9a-f]{40}$/.test(args["source-commit"])) throw new Error("source commit must be a full SHA");

const sha256 = (value) => createHash("sha256").update(value).digest("hex");
const parse = async (file) => {
  const contents = await readFile(file, "utf8");
  const passed = new Set();
  const failed = [];
  for (const line of contents.split(/\n/)) {
    if (!line.trim()) continue;
    const event = JSON.parse(line);
    if (event.Test && event.Action === "pass") passed.add(event.Test);
    if (event.Test && event.Action === "fail") failed.push(event.Test);
  }
  return { contents, passed, failed };
};
const unit = await parse(args.unit);
const integration = await parse(args.integration);
const passed = new Set([...unit.passed, ...integration.passed]);

const journeys = {
  registration_verification_login: ["TestAuthRegistrationVerificationAndLoginAreDurableIdempotentAndSecretSafe"],
  logout_session_revocation: ["TestSessionLifecycleIsPaginatedOwnedCASIdempotentAndEvented", "TestLogoutRevokesTrustedCurrentSessionAndClearsBothCookies"],
  password_change_reset: ["TestPasswordRecoveryAndChangeAreUniformSingleUseRotatingAndSecretSafe", "TestPasswordChangeReauthenticatesAndRotatesSessionCookies"],
  email_change: ["TestEmailChangeIsVersionedConfirmedRotatingAndSecretSafe"],
  invitation_membership: ["TestInvitationLifecycleIsTenantAdminScopedCapabilityBoundAndIdempotent", "TestMembershipLifecycleIsTenantScopedAuditedCASAndSessionSafe"],
  export_erasure: ["TestAccountExportAndErasureAreReauthenticatedIdempotentAndEvented"],
};
const security = {
  csrf: ["TestGatewayRequiresCSRFAndBindsUnsafeRequest", "TestAnonymousRouteIssuesIsolatedTrustedContextAndRequiresBoundCSRF"],
  session_fixation: ["TestPasswordRecoveryAndChangeAreUniformSingleUseRotatingAndSecretSafe", "TestEmailChangeIsVersionedConfirmedRotatingAndSecretSafe"],
  email_enumeration: ["TestLoginCredentialFailuresAreIndistinguishable", "TestPasswordForgotNormalizesEmailAndEnforcesUniformResponse"],
  password_truncation: ["TestHashVerifyUsesFullUnicodePassword", "TestPasswordPolicyAndStoredParameterBounds"],
  forged_identity_header: ["TestGatewayStripsForgedIdentityAndIssuesServerContext"],
};
const evaluate = (groups) => Object.fromEntries(Object.entries(groups).map(([name, tests]) => [name, { tests, passed: tests.every((test) => passed.has(test)) }]));
const journeyResults = evaluate(journeys);
const securityResults = evaluate(security);
if (unit.failed.length || integration.failed.length || Object.values(journeyResults).some((result) => !result.passed) || Object.values(securityResults).some((result) => !result.passed)) throw new Error("identity acceptance is incomplete or failed");

const report = {
  schemaVersion: "1.0.0",
  stage: 2,
  gate: "identity-e2e",
  status: "passed",
  source: {
    commit: args["source-commit"],
    unitLogSha256: sha256(unit.contents),
    integrationLogSha256: sha256(integration.contents),
  },
  results: {
    passedTests: passed.size,
    failedTests: 0,
    journeys: journeyResults,
    security: securityResults,
    bypasses: 0,
  },
};
const output = path.resolve(args.output);
await mkdir(path.dirname(output), { recursive: true });
await writeFile(output, `${JSON.stringify(report, null, 2)}\n`, { flag: "w" });
console.log(`stage-2 identity e2e: tests=${passed.size} journeys=${Object.keys(journeys).length} security=${Object.keys(security).length} bypasses=0 status=passed`);
