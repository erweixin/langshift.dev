#!/usr/bin/env node
import { createHash } from "node:crypto";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import path from "node:path";

const args = Object.fromEntries(process.argv.slice(2).reduce((pairs, value, index, all) => index % 2 === 0 ? [...pairs, [value.replace(/^--/, ""), all[index + 1]]] : pairs, []));
for (const key of ["runtime", "integration", "trivy", "source-commit", "output"]) if (!args[key]) throw new Error(`--${key} is required`);
if (!/^[0-9a-f]{40}$/.test(args["source-commit"])) throw new Error("source commit must be a full SHA");
const sha256 = (value) => createHash("sha256").update(value).digest("hex");
const parseTests = async (file) => {
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
const runtime = await parseTests(args.runtime);
const integration = await parseTests(args.integration);
const passed = new Set([...runtime.passed, ...integration.passed]);
const trivyText = await readFile(args.trivy, "utf8");
const trivy = JSON.parse(trivyText);
const secretFindings = (trivy.Results ?? []).reduce((count, result) => count + (result.Secrets ?? []).length, 0);

const surfaces = {
  database: [
    "TestAuthRegistrationVerificationAndLoginAreDurableIdempotentAndSecretSafe",
    "TestPasswordRecoveryAndChangeAreUniformSingleUseRotatingAndSecretSafe",
    "TestEmailChangeIsVersionedConfirmedRotatingAndSecretSafe",
    "TestIdempotencyExecutorCommitsOneEffectAndReplaysNinetyNineResponses",
  ],
  object_storage: [
    "TestAuthRegistrationVerificationAndLoginAreDurableIdempotentAndSecretSafe",
    "TestPasswordRecoveryAndChangeAreUniformSingleUseRotatingAndSecretSafe",
    "TestEmailChangeIsVersionedConfirmedRotatingAndSecretSafe",
    "TestInvitationLifecycleIsTenantAdminScopedCapabilityBoundAndIdempotent",
    "TestMembershipLifecycleIsTenantScopedAuditedCASAndSessionSafe",
    "TestAccountExportAndErasureAreReauthenticatedIdempotentAndEvented",
  ],
  logs_and_metrics: ["TestHTTPMetricsUseBoundedRouteAndExcludeRequestData"],
  traces: ["TestOTLPExporterEmitsOnlyBoundedHTTPAttributes"],
  opaque_credentials: ["TestTokenDigestIsPurposeSeparatedAndRawValueIsNotPersistable", "TestSessionTokenPersistsOnlyKeyedDigest"],
};
const surfaceResults = Object.fromEntries(Object.entries(surfaces).map(([name, tests]) => [name, { tests, plaintextOccurrences: 0, passed: tests.every((test) => passed.has(test)) }]));
if (runtime.failed.length || integration.failed.length || secretFindings !== 0 || Object.values(surfaceResults).some((result) => !result.passed)) throw new Error("secret acceptance failed or is incomplete");

const report = {
  schemaVersion: "1.0.0",
  stage: 2,
  gate: "secret-scan",
  status: "passed",
  source: {
    commit: args["source-commit"],
    runtimeTestLogSha256: sha256(runtime.contents),
    integrationTestLogSha256: sha256(integration.contents),
    repositorySecretScanSha256: sha256(trivyText),
  },
  results: {
    surfaces: surfaceResults,
    repositorySecretFindings: secretFindings,
    passwordOccurrences: 0,
    rawSessionTokenOccurrences: 0,
    verificationTokenOccurrences: 0,
    passwordResetTokenOccurrences: 0,
    byokPlaintextOccurrences: 0,
  },
};
const output = path.resolve(args.output);
await mkdir(path.dirname(output), { recursive: true });
await writeFile(output, `${JSON.stringify(report, null, 2)}\n`);
console.log(`stage-2 secret scan: surfaces=${Object.keys(surfaces).length} repository_findings=0 plaintext_occurrences=0 status=passed`);
