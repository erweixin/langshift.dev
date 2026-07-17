import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const scenarios = ["instanceOrAZ", "region", "queueLoss"];
const sha256 = (value) => createHash("sha256").update(value).digest("hex");
const hex64 = /^[0-9a-f]{64}$/;
const hex40 = /^[0-9a-f]{40}$/;
const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;

const secondsBetween = (left, right) => Math.abs(Date.parse(left) - Date.parse(right)) / 1000;
export function validateRecoveryRecords(records, { sourceCommit, rcHash }) {
  if (!hex40.test(sourceCommit) || !hex64.test(rcHash) || !Array.isArray(records) || records.length !== 9) throw new Error("exactly nine RC-bound recovery drill records are required");
  const runIds = new Set();
  for (const scenario of scenarios) {
    const attempts = records.filter((record) => record.scenario === scenario).sort((left, right) => left.attempt - right.attempt);
    if (attempts.length !== 3 || attempts.some((record, index) => record.attempt !== index + 1)) throw new Error(`${scenario} must have three consecutive attempts`);
    for (const record of attempts) {
      if (record.schemaVersion !== "1.0.0" || record.sourceCommit !== sourceCommit || record.rcHash !== rcHash || record.status !== "passed") throw new Error(`${scenario} record is not bound to the passed RC`);
      if (!uuid.test(record.runId) || runIds.has(record.runId)) throw new Error("recovery run IDs must be unique UUIDs");
      runIds.add(record.runId);
      if (![record.environmentFingerprint, record.configSnapshotHash, record.imageLockHash, record.transcriptSha256, record.stateBeforeSha256, record.stateAfterSha256, record.logicalDataChecksumBefore, record.logicalDataChecksumAfter].every((value) => hex64.test(value ?? ""))) throw new Error(`${scenario} contains an invalid evidence hash`);
      if (record.logicalDataChecksumBefore !== record.logicalDataChecksumAfter || record.lostEventFacts !== 0 || record.missingObjectReferences !== 0 || record.duplicateExternalEffects !== 0 || record.ledgerReconciliationPassed !== true || record.accountErasureReplayPassed !== true || record.healthChecksPassed !== true || record.securityChecksPassed !== true) throw new Error(`${scenario} violated a recovery data, effect, security, accounting, or deletion invariant`);
      if (![record.faultInjectedAt, record.lastAcknowledgedWriteAt, record.lastRecoveredWriteAt, record.trafficResumedAt].every((value) => Number.isFinite(Date.parse(value)))) throw new Error(`${scenario} timestamps are invalid`);
      const faultAt = Date.parse(record.faultInjectedAt);
      const resumedAt = Date.parse(record.trafficResumedAt);
      if (resumedAt <= faultAt) throw new Error(`${scenario} recovery completion precedes fault injection`);
      const rpoSeconds = secondsBetween(record.lastAcknowledgedWriteAt, record.lastRecoveredWriteAt);
      const rtoSeconds = (resumedAt - faultAt) / 1000;
      if (scenario === "instanceOrAZ" && (rpoSeconds !== 0 || rtoSeconds > 300) || scenario === "region" && (rpoSeconds > 300 || rtoSeconds > 3600) || scenario === "queueLoss" && rtoSeconds > 900) throw new Error(`${scenario} exceeded its RPO/RTO threshold`);
      if (!uuid.test(record.storeEpochBefore) || !uuid.test(record.storeEpochAfter)) throw new Error(`${scenario} Store Epoch is invalid`);
      if (scenario === "region" ? record.storeEpochBefore === record.storeEpochAfter || record.oldEpochCommandsRejected < 1 || record.oldEpochCommandsAccepted !== 0 : record.storeEpochBefore !== record.storeEpochAfter || record.oldEpochCommandsAccepted !== 0) throw new Error(`${scenario} Store Epoch fence is invalid`);
      if (scenario === "queueLoss" && (record.recreatedCommands !== record.pendingCommandsBeforeFault || record.duplicateCommandExecutions !== 0)) throw new Error("queue loss did not recreate each pending command exactly once at the effect boundary");
    }
  }
  if (records.some((record) => !scenarios.includes(record.scenario))) throw new Error("unknown recovery scenario");
  return true;
}

export function buildRecoveryReport(records, { sourceCommit, rcHash, evidencePath, rawEvidenceSha256, generatedAt = new Date().toISOString() }) {
  validateRecoveryRecords(records, { sourceCommit, rcHash });
  if (!evidencePath || !hex64.test(rawEvidenceSha256)) throw new Error("recovery raw evidence binding is invalid");
  const summary = Object.fromEntries(scenarios.map((scenario) => {
    const attempts = records.filter((record) => record.scenario === scenario);
    return [scenario, {
      consecutivePasses: 3,
      rpoSeconds: Math.max(...attempts.map((record) => secondsBetween(record.lastAcknowledgedWriteAt, record.lastRecoveredWriteAt))),
      rtoSeconds: Math.max(...attempts.map((record) => (Date.parse(record.trafficResumedAt) - Date.parse(record.faultInjectedAt)) / 1000)),
      attempts: attempts.map((record) => ({ runId: record.runId, environmentFingerprint: record.environmentFingerprint, transcriptSha256: record.transcriptSha256, completedAt: record.trafficResumedAt })),
    }];
  }));
  const base = { reportVersion: "1.0.0", stage: 6, kind: "recovery-drills", status: "passed", generatedAt, sourceCommit, worktreeDirty: false, rcHash, requiredConsecutivePasses: 3, rawEvidence: { path: evidencePath, sha256: rawEvidenceSha256, records: records.length }, ...summary };
  return { ...base, reportHash: sha256(JSON.stringify(base)) };
}

async function main() {
  const root = resolve(import.meta.dirname, "..");
  const option = (name) => { const index = process.argv.indexOf(name); return index >= 0 ? process.argv[index + 1] : undefined; };
  const sourceCommit = option("--source-commit");
  const rcHash = option("--rc-hash");
  const input = option("--evidence-jsonl");
  if (!input) throw new Error("--evidence-jsonl is required");
  if (execFileSync("git", ["rev-parse", "HEAD"], { cwd: root, encoding: "utf8" }).trim() !== sourceCommit) throw new Error("recovery report source commit must equal HEAD");
  if (execFileSync("git", ["status", "--porcelain=v1"], { cwd: root, encoding: "utf8" }).trim()) throw new Error("recovery report requires a clean source worktree");
  const raw = await readFile(resolve(root, input), "utf8");
  const records = raw.trim().split("\n").filter(Boolean).map((line) => JSON.parse(line));
  validateRecoveryRecords(records, { sourceCommit, rcHash });
  const layout = JSON.parse(await readFile(resolve(root, "contracts/release/stage6-evidence-layout.json"), "utf8"));
  const evidencePath = `${layout.root}/${layout.rawEvidence.recovery}`;
  const evidenceTarget = resolve(root, evidencePath);
  await mkdir(dirname(evidenceTarget), { recursive: true });
  await writeFile(evidenceTarget, raw.endsWith("\n") ? raw : `${raw}\n`);
  const report = buildRecoveryReport(records, { sourceCommit, rcHash, evidencePath, rawEvidenceSha256: sha256(await readFile(evidenceTarget)) });
  await writeFile(resolve(root, `${layout.root}/${layout.reports.recovery}`), `${JSON.stringify(report, null, 2)}\n`);
  console.log(`recovery drills: instance/AZ=${report.instanceOrAZ.rtoSeconds}s region=${report.region.rtoSeconds}s queue=${report.queueLoss.rtoSeconds}s status=passed report=${report.reportHash}`);
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) await main();
