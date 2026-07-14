#!/usr/bin/env node
import { createHash } from "node:crypto";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import path from "node:path";

const options = new Map();
for (let index = 2; index < process.argv.length; index += 2) {
  const key = process.argv[index];
  const value = process.argv[index + 1];
  if (!key?.startsWith("--") || value === undefined) throw new Error("invalid Stage 2 acceptance report arguments");
  options.set(key.slice(2), value);
}

const sourceCommit = options.get("source-commit");
const reportsDirectory = path.resolve(options.get("reports") ?? "gate-reports/stage-2");
const workspaceRoot = path.resolve(options.get("workspace-root") ?? process.cwd());
const output = path.resolve(options.get("output") ?? path.join(reportsDirectory, "stage2-acceptance-report.json"));
if (!/^[0-9a-f]{40}$/.test(sourceCommit ?? "")) throw new Error("--source-commit must be a full Git SHA");

const reportFiles = {
  identity: "identity-e2e-report.json",
  claimConcurrency: "claim-concurrency-report.json",
  claimFaultReconciliation: "claim-saga-fault-report.json",
  permissionMatrix: "permission-matrix-report.json",
  secretScan: "secret-scan-report.json",
  migration: "migration-report.json",
  infrastructure: "infrastructure-smoke-report.json",
};
const sha256 = (value) => createHash("sha256").update(value).digest("hex");
const reports = {};
const evidence = {};
for (const [name, fileName] of Object.entries(reportFiles)) {
  const raw = await readFile(path.join(reportsDirectory, fileName));
  reports[name] = JSON.parse(raw.toString("utf8"));
  evidence[name] = { path: fileName, sha256: sha256(raw), sizeBytes: raw.length };
}

const failures = [];
const check = (condition, code) => { if (!condition) failures.push(code); };
const allZero = (record) => Object.values(record ?? {}).every((value) => value === 0);
const exactKeys = (record, expected) => JSON.stringify(Object.keys(record ?? {}).sort()) === JSON.stringify([...expected].sort());
const passedGroups = (record) => Object.values(record ?? {}).every((entry) => entry?.passed === true);
const sourceIsCurrent = (report) => (report.source?.commit ?? report.sourceCommit ?? report.source_commit) === sourceCommit;
const verifyEmbeddedHash = (report) => {
  const copy = structuredClone(report);
  const expected = copy.reportHash;
  delete copy.reportHash;
  return /^[0-9a-f]{64}$/.test(expected ?? "") && sha256(JSON.stringify(copy)) === expected;
};

const identity = reports.identity;
check(identity.stage === 2 && identity.gate === "identity-e2e" && identity.status === "passed", "identity.status");
check(sourceIsCurrent(identity), "identity.source_commit");
check(identity.results?.passedTests > 0 && identity.results?.failedTests === 0, "identity.test_results");
check(exactKeys(identity.results?.journeys, [
  "registration_verification_login", "logout_session_revocation", "password_change_reset", "email_change", "invitation_membership", "export_erasure",
]) && passedGroups(identity.results?.journeys), "identity.journeys");
check(exactKeys(identity.results?.security, [
  "csrf", "session_fixation", "email_enumeration", "password_truncation", "forged_identity_header",
]) && passedGroups(identity.results?.security), "identity.security_boundaries");
check(identity.results?.bypasses === 0, "identity.bypasses");

const concurrency = reports.claimConcurrency;
check(concurrency.status === "passed" && concurrency.gate === "stage-2-anonymous-claim-concurrency", "claim_concurrency.status");
check(sourceIsCurrent(concurrency), "claim_concurrency.source_commit");
check(concurrency.verification?.deterministic_race_model === "passed" && concurrency.verification?.postgres_nobypassrls_integration === "passed", "claim_concurrency.verification");
check(concurrency.results?.claim_requests >= 10_000 && concurrency.results?.replay_requests >= 10_000 && concurrency.results?.expiry_cleanup_attempts >= 10_000, "claim_concurrency.load");
check(concurrency.results?.concurrent_workers > 1 && concurrency.results?.successful_claim_records === 1 && concurrency.results?.mission_effects === 1, "claim_concurrency.single_effect");
check(concurrency.results?.deletion_receipts === 3 && concurrency.results?.required_deletion_surfaces?.length === 3, "claim_concurrency.deletion_receipts");
check(concurrency.results?.expired_after_reserved === 0 && concurrency.results?.manual_review === 0 && allZero(concurrency.zero_tolerance), "claim_concurrency.zero_tolerance");

const fault = reports.claimFaultReconciliation;
check(fault.status === "passed" && fault.gate === "stage-2-anonymous-claim-fault-reconciliation", "claim_fault.status");
check(sourceIsCurrent(fault), "claim_fault.source_commit");
check(fault.verification?.deterministic_race_model === "passed" && fault.verification?.postgres_nobypassrls_integration === "passed", "claim_fault.verification");
const requiredBoundaries = ["reserved", "destination_mission_commit", "enter_erasing", "deletion_receipt:body_payload", "deletion_receipt:preview_projection", "deletion_receipt:principal_mapping"];
const boundaries = fault.results?.boundaries ?? [];
for (const boundary of requiredBoundaries) {
  for (const position of ["before", "after"]) {
    const result = boundaries.find((entry) => entry.name === boundary && entry.position === position);
    check(result?.iterations >= 100 && result?.recovered === result?.iterations, `claim_fault.boundary:${boundary}:${position}`);
  }
}
check(boundaries.length === requiredBoundaries.length * 2, "claim_fault.boundary_count");
check(fault.results?.total_fault_runs >= 1_200 && fault.results?.reconcile_redeliveries >= fault.results?.total_fault_runs, "claim_fault.reconciliation_load");
check(fault.results?.unknown_commit_results >= 100, "claim_fault.unknown_commit_results");
check(fault.results?.duplicate_missions === 0 && fault.results?.incomplete_receipt_runs === 0 && fault.results?.expired_after_reserved === 0 && fault.results?.manual_review === 0, "claim_fault.zero_tolerance");

const permission = reports.permissionMatrix;
check(permission.stage === 2 && permission.gate === "permission-matrix" && permission.status === "passed", "permission.status");
check(sourceIsCurrent(permission), "permission.source_commit");
const expectedMatrixCases = permission.results?.roles * permission.results?.resources * permission.results?.actions;
check(expectedMatrixCases === 252 && permission.results?.sameTenantCases === expectedMatrixCases && permission.results?.crossTenantCases === expectedMatrixCases, "permission.matrix_coverage");
check(permission.results?.wrongOwnerCases > 0, "permission.owner_coverage");
check(permission.results?.unexpectedAllows === 0 && permission.results?.unexpectedDenies === 0 && permission.results?.crossTenantAllows === 0 && permission.results?.wrongOwnerPrivateAllows === 0, "permission.zero_tolerance");

const secret = reports.secretScan;
check(secret.stage === 2 && secret.gate === "secret-scan" && secret.status === "passed", "secret.status");
check(sourceIsCurrent(secret), "secret.source_commit");
const surfaces = secret.results?.surfaces;
check(exactKeys(surfaces, ["database", "object_storage", "logs_and_metrics", "traces", "opaque_credentials"]), "secret.surface_coverage");
check(Object.values(surfaces ?? {}).every((surface) => surface.passed === true && surface.plaintextOccurrences === 0), "secret.surface_plaintext");
check(secret.results?.repositorySecretFindings === 0 && secret.results?.passwordOccurrences === 0 && secret.results?.rawSessionTokenOccurrences === 0 && secret.results?.verificationTokenOccurrences === 0 && secret.results?.passwordResetTokenOccurrences === 0 && secret.results?.byokPlaintextOccurrences === 0, "secret.zero_tolerance");

const migration = reports.migration;
check(migration.stage === 2 && migration.kind === "database-migration" && migration.status === "passed", "migration.status");
check(sourceIsCurrent(migration), "migration.source_commit");
check(migration.summary?.cycles >= 3 && migration.summary?.emptyInstallsPassed === migration.summary?.cycles && migration.summary?.oneVersionRollbacksPassed === migration.summary?.cycles && migration.summary?.reupgradesPassed === migration.summary?.cycles && migration.summary?.irreversiblePreflightRejectionsPassed === migration.summary?.cycles, "migration.cycles");
check(migration.summary?.dataChecksumMismatches === 0 && migration.zeroToleranceFailures?.length === 0, "migration.zero_tolerance");
check(verifyEmbeddedHash(migration), "migration.report_hash");

const infrastructure = reports.infrastructure;
check(infrastructure.stage === 2 && infrastructure.kind === "infrastructure-foundation-smoke" && infrastructure.status === "passed", "infrastructure.status");
check(sourceIsCurrent(infrastructure), "infrastructure.source_commit");
check(infrastructure.results?.composeContract === "13/13 services validated" && infrastructure.images?.length === 13, "infrastructure.compose_contract");
check(infrastructure.results?.postgres?.temporaryReadWrite === "passed" && infrastructure.results?.jetstream?.streamCreate === "passed" && infrastructure.results?.jetstream?.publish === "passed" && infrastructure.results?.valkey?.authenticatedSetGet === "passed" && infrastructure.results?.objectStorage?.writeRead === "passed" && infrastructure.results?.vault?.kvWriteRead === "passed" && infrastructure.results?.observability?.otlpCollectorToTempoTrace === "passed", "infrastructure.runtime_paths");
check(infrastructure.scope?.hostPublishedPorts === 0 && infrastructure.controls?.noHostPorts === true && infrastructure.controls?.internalNetworkOnly === true && infrastructure.controls?.noLatestTags === true && infrastructure.controls?.immutableImageReferences === 13 && infrastructure.controls?.noNewPrivilegesServices === 13, "infrastructure.security_controls");
check(infrastructure.zeroToleranceFailures?.length === 0 && verifyEmbeddedHash(infrastructure), "infrastructure.zero_tolerance_or_hash");
for (const item of infrastructure.evidence ?? []) {
  const contents = await readFile(path.resolve(workspaceRoot, item.path));
  check(contents.length === item.sizeBytes && sha256(contents) === item.sha256, `infrastructure.evidence:${item.path}`);
}
check(infrastructure.evidence?.length === 10, "infrastructure.evidence_count");

if (failures.length > 0) {
  throw new Error(`Stage 2 acceptance failed (${failures.length}):\n- ${failures.join("\n- ")}`);
}

const report = {
  schemaVersion: "1.0.0",
  stage: 2,
  gate: "foundation-acceptance",
  generatedAt: new Date().toISOString(),
  sourceCommit,
  status: "passed",
  scope: {
    requiredEvidenceReports: Object.keys(reportFiles).length,
    verifiedEvidenceReports: Object.keys(evidence).length,
    zeroToleranceFailures: 0,
    releaseAttestationEvaluated: false,
  },
  results: {
    identityJourneys: Object.keys(identity.results.journeys).length,
    identitySecurityBoundaries: Object.keys(identity.results.security).length,
    claimRequests: concurrency.results.claim_requests,
    claimFaultRuns: fault.results.total_fault_runs,
    permissionCases: permission.results.sameTenantCases + permission.results.crossTenantCases + permission.results.wrongOwnerCases,
    secretSurfaces: Object.keys(surfaces).length,
    migrationCycles: migration.summary.cycles,
    infrastructureServices: infrastructure.images.length,
  },
  evidence,
  notes: [
    "This report evaluates the seven mandatory Stage 2 evidence reports defined by docs/product-implementation-plan.md.",
    "Signed multi-platform release image attestations are evaluated by .github/workflows/supply-chain.yml and are not inferred by this gate.",
  ],
};
report.reportHash = sha256(JSON.stringify(report));
await mkdir(path.dirname(output), { recursive: true });
await writeFile(output, `${JSON.stringify(report, null, 2)}\n`, { mode: 0o644 });
console.log(`stage-2 acceptance: reports=${Object.keys(evidence).length} zero_tolerance_failures=0 status=passed report=${report.reportHash}`);
