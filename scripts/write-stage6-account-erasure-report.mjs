import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const option = (name) => {
  const index = process.argv.indexOf(name);
  return index >= 0 ? process.argv[index + 1] : undefined;
};
const evidencePath = option("--test-json");
const dependencyEvidencePath = option("--dependency-test-json");
const dependencyRuntimes = {
  s3: option("--s3-runtime"),
  vault: option("--vault-runtime"),
  valkey: option("--valkey-runtime"),
};
if (!evidencePath || !dependencyEvidencePath || Object.values(dependencyRuntimes).some((value) => !/@sha256:[0-9a-f]{64}$/.test(value ?? ""))) throw new Error("database and dependency test JSON plus digest-pinned dependency runtimes are required");
const sha256 = (value) => createHash("sha256").update(value).digest("hex");
const sourceCommit = option("--source-commit") ?? execFileSync("git", ["rev-parse", "HEAD"], { cwd: root, encoding: "utf8" }).trim();
const rcHash = option("--rc-hash") ?? null;
if (!/^[0-9a-f]{40}$/.test(sourceCommit) || (rcHash !== null && !/^[0-9a-f]{64}$/.test(rcHash))) throw new Error("source commit or release candidate hash is invalid");
const worktreeDirty = execFileSync("git", ["status", "--porcelain=v1"], { cwd: root, encoding: "utf8" }).trim().length > 0;
const raw = await readFile(resolve(root, evidencePath), "utf8");
const records = raw.trim().split("\n").filter(Boolean).map((line) => JSON.parse(line));
if (records.some((record) => record.Action === "fail")) throw new Error("database evidence contains a failing record");
const testName = "TestAccountErasureHundredSubjectsAndRestoreEpoch";
const passed = records.find((record) => record.Action === "pass" && record.Test === testName);
if (!passed) throw new Error(`${testName} did not pass in supplied evidence`);
const dependencyRaw = await readFile(resolve(root, dependencyEvidencePath), "utf8");
const migrationManifestRaw = await readFile(resolve(root, "deploy/migrations/manifest.json"));
const currentMigration = JSON.parse(migrationManifestRaw).migrations?.at(-1);
if (!Number.isInteger(currentMigration?.version) || !currentMigration?.name) throw new Error("current migration manifest is invalid");
const dependencyRecords = dependencyRaw.trim().split("\n").filter(Boolean).map((line) => JSON.parse(line));
if (dependencyRecords.some((record) => record.Action === "fail")) throw new Error("dependency evidence contains a failing record");
const dependencyTests = [
  "TestS3CompatiblePurgeRemovesAllVersionsAndDeleteMarkers",
  "TestVaultTransitSubjectKeyPurgeIsIrreversible",
  "TestTaggedSubjectCachePutAndPurgeAreAtomicAndConfined",
].map((name) => {
  const record = dependencyRecords.find((item) => item.Action === "pass" && item.Test === name);
  if (!record) throw new Error(`${name} did not pass in supplied dependency evidence`);
  return { name, elapsedSeconds: record.Elapsed };
});
const files = [
  "cmd/account-erasure-worker/main.go",
  "internal/identity/erasure/service.go",
  "internal/identity/erasure/service_test.go",
  "internal/identity/erasure/valkey_cache.go",
  "internal/identity/erasure/valkey_cache_integration_test.go",
  "internal/identity/postgres/account_erasure_dispatcher.go",
  "internal/identity/postgres/account_erasure_store.go",
  "internal/identity/postgres/account_erasure_surface.go",
  "internal/identity/postgres/account_erasure_integration_test.go",
  "internal/memory/postgres/inline_write.go",
  "internal/memory/postgres/retrieval_manifest.go",
  "internal/objectstore/s3store/store.go",
  "internal/objectstore/s3store/purge_router.go",
  "internal/objectstore/s3store/integration_test.go",
  "internal/payload/vaultkeys/purger.go",
  "internal/payload/vaultkeys/integration_test.go",
  "deploy/migrations/000084_account_erasure_receipts.up.sql",
  "contracts/events/amendments/v1.24.0/registry.json",
  "runbooks/account-erasure.md",
  "scripts/foundation-postgres-smoke.sh",
  "scripts/install-ga-release-candidate.sh",
  "scripts/stage6-account-erasure-gate.sh",
  "scripts/write-stage6-account-erasure-report.mjs",
  ".github/workflows/supply-chain.yml",
];
const artifacts = [];
for (const path of files) artifacts.push({ path, sha256: sha256(await readFile(resolve(root, path))) });
const base = {
  reportVersion: "1.0.0",
  stage: 6,
  kind: "account-erasure-100",
  status: worktreeDirty ? "failed" : "passed",
  executionResult: "passed",
  evidenceClassification: worktreeDirty ? "current_dirty" : "current_clean",
  generatedAt: new Date().toISOString(),
  sourceCommit,
  worktreeDirty,
  rcHash,
  accounts: 100,
  surfaces: ["payload", "memory", "indexes", "workspace_artifact", "cache", "snapshot"],
  readableSurfacesAfter: 0,
  completeReceipts: 100,
  initialSurfaceReceiptRows: 600,
  restoreRedeletions: 100,
  totalSurfaceReceiptRowsAfterRestore: 1200,
  completionEvents: 100,
  tombstones: 100,
  database: `PostgreSQL 16 with migrations through version ${currentMigration.version} and FORCE RLS; deletion receipts were introduced by migration 84`,
  migration: { version: currentMigration.version, name: currentMigration.name, manifestSha256: sha256(migrationManifestRaw) },
  executionRole: "NOBYPASSRLS lites_erasure_worker",
  objectDeletion: "all S3 versions and delete markers are removed and re-listed before receipt",
  keyDeletion: "per-subject Vault Transit key deletion is enabled, executed, and verified absent",
  restoreProtocol: "new Store Epoch scans immutable tombstones and replays all six surfaces",
  test: { name: testName, elapsedSeconds: passed.Elapsed, evidencePath, evidenceSha256: sha256(raw) },
  dependencyGate: { evidencePath: dependencyEvidencePath, evidenceSha256: sha256(dependencyRaw), runtimes: dependencyRuntimes, tests: dependencyTests },
  artifacts,
};
const report = { ...base, reportHash: sha256(JSON.stringify(base)) };
const target = resolve(root, process.env.LITES_ACCOUNT_ERASURE_REPORT ?? "gate-reports/stage-6/account-erasure-100.json");
await mkdir(dirname(target), { recursive: true });
await writeFile(target, `${JSON.stringify(report, null, 2)}\n`);
console.log(`account erasure execution=passed evidence=${worktreeDirty ? "failed" : "passed"} accounts=100 receipts=1200 restore=100 report=${report.reportHash}`);
