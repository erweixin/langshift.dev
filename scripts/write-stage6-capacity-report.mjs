import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const sha256 = (value) => createHash("sha256").update(value).digest("hex");
const hex64 = /^[0-9a-f]{64}$/;
const hex40 = /^[0-9a-f]{40}$/;
const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;
const phases = ["steady", "burst", "recovery"];

function sampleMeetsSLO(sample) {
  const metric = sample.metrics ?? {};
  return metric.apiAvailability >= 0.9995 && metric.apiP95Ms <= 250 && metric.apiP99Ms <= 1000 &&
    metric.eventAppendAvailability >= 0.9999 && metric.eventAppendP99Ms <= 200 &&
    metric.queueWaitP95Ms <= 2000 && metric.queueWaitP99Ms <= 10000 &&
    metric.firstSafeTokenP95Ms <= 5000 && metric.firstSafeTokenP99Ms <= 15000 &&
    metric.runDeadlineAdherence >= 0.99 && metric.interactiveRunPlatformSuccess >= 0.995 &&
    metric.realtimeRecoveryP95Ms <= 2000 && metric.realtimeRecoveryP99Ms <= 10000 &&
    metric.runtimeColdStartP95Ms <= 5000 && metric.runtimeColdStartP99Ms <= 15000 &&
    metric.toolOutcomeUnknownRatio < 0.001 && metric.toolOutcomeUnknownConverged15m >= 0.99 &&
    metric.tenantIsolationViolations === 0 && metric.legitimateThrottleRatio < 0.001;
}

export function validateCapacitySamples(samples, { sourceCommit, rcHash }) {
  if (!hex40.test(sourceCommit) || !hex64.test(rcHash) || !Array.isArray(samples) || samples.length < 38) throw new Error("capacity evidence identity or sample count is invalid");
  const runIds = new Set(samples.map((sample) => sample.runId));
  const loadVectorHashes = new Set(samples.map((sample) => sample.referenceLoadVectorHash));
  const environmentHashes = new Set(samples.map((sample) => sample.environmentFingerprint));
  const configHashes = new Set(samples.map((sample) => sample.configSnapshotHash));
  const imageLockHashes = new Set(samples.map((sample) => sample.imageLockHash));
  if (runIds.size !== 1 || !uuid.test([...runIds][0]) || [loadVectorHashes, environmentHashes, configHashes, imageLockHashes].some((values) => values.size !== 1 || !hex64.test([...values][0] ?? ""))) throw new Error("capacity samples do not share one immutable run environment");
  let priorEnd = null;
  let priorPhaseIndex = 0;
  const seconds = { steady: 0, burst: 0, recovery: 0 };
  for (const sample of samples) {
    if (sample.schemaVersion !== "1.0.0" || sample.sourceCommit !== sourceCommit || sample.rcHash !== rcHash || sample.status !== "passed" || !phases.includes(sample.phase)) throw new Error("capacity sample is not bound to the passed RC");
    const phaseIndex = phases.indexOf(sample.phase);
    if (phaseIndex < priorPhaseIndex || phaseIndex > priorPhaseIndex + 1) throw new Error("capacity phases are out of order");
    priorPhaseIndex = phaseIndex;
    const start = Date.parse(sample.windowStart);
    const end = Date.parse(sample.windowEnd);
    const duration = (end - start) / 1000;
    if (!Number.isFinite(start) || !Number.isFinite(end) || duration < 55 || duration > 65 || priorEnd !== null && start - priorEnd > 5000 || priorEnd !== null && start < priorEnd) throw new Error("capacity windows are invalid or discontinuous");
    priorEnd = end;
    seconds[sample.phase] += duration;
    if (sample.phase === "steady" && (sample.loadMultiplier < 1 || sample.loadMultiplier > 1.05) || sample.phase === "burst" && sample.loadMultiplier < 2 || sample.phase === "recovery" && sample.loadMultiplier > 1.05) throw new Error("capacity phase load multiplier is invalid");
    if (!sampleMeetsSLO(sample)) throw new Error("one or more production SLOs were violated");
  }
  if (seconds.steady < 1800 || seconds.burst < 300 || seconds.recovery <= 0 || seconds.recovery > 900) throw new Error("steady, burst, or recovery duration is outside the GA threshold");
  const recovery = samples.filter((sample) => sample.phase === "recovery");
  if (recovery.length < 3 || recovery.slice(-3).some((sample) => sample.backlogNormalized !== true || sample.outboxBacklogAgeSeconds > 2 || sample.queueBacklogAgeSeconds > 10 || sample.sweeperBacklogAgeSeconds > 60)) throw new Error("backlog did not return to steady thresholds for three windows");
  return { seconds, burstMultiplier: Math.min(...samples.filter((sample) => sample.phase === "burst").map((sample) => sample.loadMultiplier)) };
}

export function buildCapacityReport(samples, { sourceCommit, rcHash, evidencePath, rawEvidenceSha256, generatedAt = new Date().toISOString() }) {
  const validated = validateCapacitySamples(samples, { sourceCommit, rcHash });
  if (!evidencePath || !hex64.test(rawEvidenceSha256)) throw new Error("capacity raw evidence binding is invalid");
  const exemplar = samples[0];
  const base = {
    reportVersion: "1.0.0", stage: 6, kind: "official-capacity", status: "passed", generatedAt, sourceCommit, worktreeDirty: false, rcHash,
    steadyMinutes: validated.seconds.steady / 60, burstMultiplier: validated.burstMultiplier, burstMinutes: validated.seconds.burst / 60, backlogRecoveryMinutes: validated.seconds.recovery / 60, sloViolations: 0,
    referenceLoadVectorHash: exemplar.referenceLoadVectorHash, environmentFingerprint: exemplar.environmentFingerprint, configSnapshotHash: exemplar.configSnapshotHash, imageLockHash: exemplar.imageLockHash,
    rawEvidence: { path: evidencePath, sha256: rawEvidenceSha256, samples: samples.length },
  };
  return { ...base, reportHash: sha256(JSON.stringify(base)) };
}

async function main() {
  const root = resolve(import.meta.dirname, "..");
  const option = (name) => { const index = process.argv.indexOf(name); return index >= 0 ? process.argv[index + 1] : undefined; };
  const sourceCommit = option("--source-commit");
  const rcHash = option("--rc-hash");
  const input = option("--evidence-jsonl");
  if (!input) throw new Error("--evidence-jsonl is required");
  if (execFileSync("git", ["rev-parse", "HEAD"], { cwd: root, encoding: "utf8" }).trim() !== sourceCommit) throw new Error("capacity report source commit must equal HEAD");
  if (execFileSync("git", ["status", "--porcelain=v1"], { cwd: root, encoding: "utf8" }).trim()) throw new Error("capacity report requires a clean source worktree");
  const raw = await readFile(resolve(root, input), "utf8");
  const samples = raw.trim().split("\n").filter(Boolean).map((line) => JSON.parse(line));
  validateCapacitySamples(samples, { sourceCommit, rcHash });
  const layout = JSON.parse(await readFile(resolve(root, "contracts/release/stage6-evidence-layout.json"), "utf8"));
  const evidencePath = `${layout.root}/${layout.rawEvidence.capacity}`;
  const evidenceTarget = resolve(root, evidencePath);
  await mkdir(dirname(evidenceTarget), { recursive: true });
  await writeFile(evidenceTarget, raw.endsWith("\n") ? raw : `${raw}\n`);
  const installedRaw = await readFile(evidenceTarget);
  const report = buildCapacityReport(samples, { sourceCommit, rcHash, evidencePath, rawEvidenceSha256: sha256(installedRaw) });
  await writeFile(resolve(root, `${layout.root}/${layout.reports.capacity}`), `${JSON.stringify(report, null, 2)}\n`);
  console.log(`official capacity: steady=${report.steadyMinutes}m burst=${report.burstMultiplier}x/${report.burstMinutes}m recovery=${report.backlogRecoveryMinutes}m status=passed report=${report.reportHash}`);
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) await main();
