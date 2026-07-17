import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const operations = ["freshInstall", "upgrade", "backup", "restore", "rollback"];
const targets = ["compose", "helm-tofu", "official-cloud"];
const sha256 = (value) => createHash("sha256").update(value).digest("hex");
const hex64 = /^[0-9a-f]{64}$/;
const hex40 = /^[0-9a-f]{40}$/;
const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;

export function validateDeploymentRecords(records, { target, sourceCommit, rcHash }) {
  if (!targets.includes(target) || !hex40.test(sourceCommit) || !hex64.test(rcHash)) throw new Error("invalid deployment target, source commit, or RC hash");
  if (!Array.isArray(records) || records.length !== operations.length * 3) throw new Error("exactly 15 deployment drill records are required");
  const runIds = new Set();
  for (const operation of operations) {
    const attempts = records.filter((record) => record.operation === operation).sort((left, right) => left.attempt - right.attempt);
    if (attempts.length !== 3 || attempts.some((record, index) => record.attempt !== index + 1)) throw new Error(`${operation} must have attempts 1, 2, and 3 exactly once`);
    for (const record of attempts) {
      if (record.schemaVersion !== "1.0.0" || record.target !== target || record.sourceCommit !== sourceCommit || record.rcHash !== rcHash || record.status !== "passed") throw new Error(`${operation} record is not bound to the requested passed RC`);
      if (!uuid.test(record.runId) || runIds.has(record.runId)) throw new Error("deployment run IDs must be unique UUIDs");
      runIds.add(record.runId);
      if (![record.environmentFingerprint, record.transcriptSha256, record.stateBeforeSha256, record.stateAfterSha256, record.expectedLogicalDataChecksum, record.logicalDataChecksumBefore, record.logicalDataChecksumAfter].every((value) => hex64.test(value ?? ""))) throw new Error(`${operation} record contains an invalid evidence hash`);
      const emptyDatasetHash = sha256("");
      if (operation === "freshInstall" ? record.logicalDataChecksumBefore !== emptyDatasetHash || record.logicalDataChecksumAfter !== record.expectedLogicalDataChecksum : record.logicalDataChecksumBefore !== record.expectedLogicalDataChecksum || record.logicalDataChecksumAfter !== record.expectedLogicalDataChecksum) throw new Error(`${operation} changed or failed to install the logical validation dataset`);
      if (![record.healthChecksPassed, record.securityChecksPassed, record.dataReconciliationPassed, record.deletionReplayPassed].every((value) => value === true)) throw new Error(`${operation} did not pass every health, security, reconciliation, and deletion replay check`);
      if (!(Number.isFinite(record.durationSeconds) && record.durationSeconds > 0)) throw new Error(`${operation} duration is invalid`);
      const started = Date.parse(record.startedAt);
      const completed = Date.parse(record.completedAt);
      if (!Number.isFinite(started) || !Number.isFinite(completed) || completed <= started) throw new Error(`${operation} timestamps are invalid`);
      const expectedSchema = {
        freshInstall: [0, 84],
        upgrade: [83, 84],
        backup: [84, 84],
        restore: [84, 84],
        rollback: [84, 84],
      }[operation];
      if (record.schemaVersionBefore !== expectedSchema[0] || record.schemaVersionAfter !== expectedSchema[1]) throw new Error(`${operation} schema transition is invalid`);
      if (!uuid.test(record.storeEpochBefore) || !uuid.test(record.storeEpochAfter)) throw new Error(`${operation} Store Epoch is invalid`);
      if (operation === "restore" ? record.storeEpochBefore === record.storeEpochAfter : record.storeEpochBefore !== record.storeEpochAfter) throw new Error(`${operation} Store Epoch transition is invalid`);
      if (typeof record.platformVersion !== "string" || record.platformVersion.length === 0 || typeof record.runnerIdentity !== "string" || record.runnerIdentity.length === 0) throw new Error(`${operation} platform or runner identity is missing`);
    }
  }
  if (records.some((record) => !operations.includes(record.operation))) throw new Error("unknown deployment operation");
  return true;
}

export function buildDeploymentReport(records, { target, sourceCommit, rcHash, evidencePath, rawEvidenceSha256, generatedAt = new Date().toISOString() }) {
  validateDeploymentRecords(records, { target, sourceCommit, rcHash });
  if (!hex64.test(rawEvidenceSha256) || !evidencePath) throw new Error("raw deployment evidence binding is invalid");
  const operationResults = Object.fromEntries(operations.map((operation) => {
    const attempts = records.filter((record) => record.operation === operation).sort((left, right) => left.attempt - right.attempt);
    return [operation, {
      consecutivePasses: 3,
      attempts: attempts.map((record) => ({ runId: record.runId, environmentFingerprint: record.environmentFingerprint, transcriptSha256: record.transcriptSha256, stateBeforeSha256: record.stateBeforeSha256, stateAfterSha256: record.stateAfterSha256, durationSeconds: record.durationSeconds, completedAt: record.completedAt })),
    }];
  }));
  const base = { reportVersion: "1.0.0", stage: 6, kind: "deployment-drill", target, status: "passed", generatedAt, sourceCommit, worktreeDirty: false, rcHash, currentMigration: 84, requiredConsecutivePasses: 3, rawEvidence: { path: evidencePath, sha256: rawEvidenceSha256, records: records.length }, operations: operationResults };
  return { ...base, reportHash: sha256(JSON.stringify(base)) };
}

async function main() {
  const root = resolve(import.meta.dirname, "..");
  const option = (name) => {
    const index = process.argv.indexOf(name);
    return index >= 0 ? process.argv[index + 1] : undefined;
  };
  const target = option("--target");
  const sourceCommit = option("--source-commit");
  const rcHash = option("--rc-hash");
  const evidenceInput = option("--evidence-jsonl");
  if (!evidenceInput) throw new Error("--evidence-jsonl is required");
  if (execFileSync("git", ["rev-parse", "HEAD"], { cwd: root, encoding: "utf8" }).trim() !== sourceCommit) throw new Error("deployment report source commit must equal HEAD");
  if (execFileSync("git", ["status", "--porcelain=v1"], { cwd: root, encoding: "utf8" }).trim()) throw new Error("deployment report requires a clean source worktree");
  const raw = await readFile(resolve(root, evidenceInput), "utf8");
  const records = raw.trim().split("\n").filter(Boolean).map((line) => JSON.parse(line));
  validateDeploymentRecords(records, { target, sourceCommit, rcHash });
  const layout = JSON.parse(await readFile(resolve(root, "contracts/release/stage6-evidence-layout.json"), "utf8"));
  const layoutKey = `deployment_${target.replaceAll("-", "_")}`;
  const evidencePath = `${layout.root}/${layout.rawEvidence[layoutKey]}`;
  const evidenceTarget = resolve(root, evidencePath);
  await mkdir(dirname(evidenceTarget), { recursive: true });
  await writeFile(evidenceTarget, raw.endsWith("\n") ? raw : `${raw}\n`);
  const installedRaw = await readFile(evidenceTarget);
  const report = buildDeploymentReport(records, { target, sourceCommit, rcHash, evidencePath, rawEvidenceSha256: sha256(installedRaw) });
  const reportTarget = resolve(root, `${layout.root}/${layout.reports[layoutKey]}`);
  await writeFile(reportTarget, `${JSON.stringify(report, null, 2)}\n`);
  console.log(`deployment drill: target=${target} operations=5 consecutive=3 status=passed report=${report.reportHash}`);
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) await main();
