import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const profiles = ["route_planner", "daily_planner", "coach", "evaluator", "artifact_builder"];
const sha256 = (value) => createHash("sha256").update(value).digest("hex");
const hex64 = /^[0-9a-f]{64}$/;
const hex40 = /^[0-9a-f]{40}$/;
const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;
const percentile = (values, fraction) => [...values].sort((left, right) => left - right)[Math.ceil(values.length * fraction) - 1];
const average = (values) => values.reduce((sum, value) => sum + value, 0) / values.length;

const sampleMap = (samples, suite) => new Map(samples.map((sample) => [sample.id, {
  id: sample.id,
  suite,
  locale: sample.locale,
  transition: sample.transition ?? (sample.input?.semanticKey ?? "").split(":")[1],
}]));

export function validateProfileEvaluation(records, { profile, sourceCommit, rcHash, profileContract, specialistSamples, sharedSamples, specialistDatasetHash, sharedDatasetHash }) {
  if (!profiles.includes(profile) || !hex40.test(sourceCommit) || !hex64.test(rcHash) || profileContract?.id !== profile) throw new Error("profile evaluation identity is invalid");
  const expected = new Map([...sampleMap(specialistSamples, "specialist"), ...sampleMap(sharedSamples, "shared")]);
  if (expected.size !== specialistSamples.length + sharedSamples.length || !Array.isArray(records) || records.length !== expected.size * 2) throw new Error("candidate and baseline records are required for every frozen sample");
  const dimensions = [...profileContract.rubric].sort();
  const indexed = new Map();
  const attempts = new Set();
  for (const record of records) {
    const sample = expected.get(record.sampleId);
    const expectedDatasetHash = sample?.suite === "specialist" ? specialistDatasetHash : sharedDatasetHash;
    if (!sample || record.schemaVersion !== "1.0.0" || record.profile !== profile || record.suite !== sample.suite || record.locale !== sample.locale || record.transition !== sample.transition || !["baseline", "candidate"].includes(record.variant) || record.sourceCommit !== sourceCommit || record.rcHash !== rcHash || record.datasetHash !== expectedDatasetHash || record.status !== "completed") throw new Error("profile record is not bound to the frozen sample and RC");
    const key = `${record.suite}:${record.sampleId}:${record.variant}`;
    if (indexed.has(key)) throw new Error("duplicate profile sample variant");
    indexed.set(key, record);
    if (!uuid.test(record.providerAttemptId) || attempts.has(record.providerAttemptId)) throw new Error("provider attempt IDs must be unique UUIDs");
    attempts.add(record.providerAttemptId);
    if (![record.behaviorManifestHash, record.profileSnapshotHash, record.modelSnapshotHash, record.promptSnapshotHash, record.toolSnapshotHash, record.policySnapshotHash, record.contextManifestHash, record.outputHash].every((value) => hex64.test(value ?? ""))) throw new Error("profile execution snapshot or output hash is invalid");
    if (!(Number.isFinite(record.costUsd) && record.costUsd >= 0 && Number.isFinite(record.latencyMs) && record.latencyMs > 0)) throw new Error("profile execution cost or latency is invalid");
    if (record.deterministicChecksPassed !== true || record.secretOrPIILeaks !== 0 || record.unauthorizedToolCalls !== 0 || !Array.isArray(record.zeroToleranceFailures) || record.zeroToleranceFailures.length !== 0) throw new Error("profile execution hit a deterministic or zero-tolerance failure");
    if (!Array.isArray(record.reviews) || record.reviews.length !== 2 || new Set(record.reviews.map((review) => review.reviewerId)).size !== 2) throw new Error("exactly two distinct reviewers are required per output");
    for (const review of record.reviews) {
      if (typeof review.reviewerId !== "string" || review.reviewerId.length === 0 || JSON.stringify(Object.keys(review.scores ?? {}).sort()) !== JSON.stringify(dimensions) || Object.values(review.scores).some((score) => !Number.isInteger(score) || score < 1 || score > 5)) throw new Error("reviewer scores do not match the frozen rubric");
    }
  }
  for (const sample of expected.values()) {
    if (!indexed.has(`${sample.suite}:${sample.id}:baseline`) || !indexed.has(`${sample.suite}:${sample.id}:candidate`)) throw new Error("baseline/candidate sample pairing is incomplete");
  }
  const candidate = records.filter((record) => record.variant === "candidate");
  const baseline = records.filter((record) => record.variant === "baseline");
  const scoreValues = (set, dimension, filter = () => true) => set.filter(filter).flatMap((record) => record.reviews.map((review) => review.scores[dimension]));
  const acceptableRate = (values) => values.filter((score) => score >= 4).length / values.length;
  const dimensionResults = {};
  let maximumRegression = 0;
  let maximumLocaleGap = 0;
  for (const dimension of dimensions) {
    const values = scoreValues(candidate, dimension);
    const mean = average(values);
    const rate = acceptableRate(values);
    if (mean < 4 || rate < 0.85) throw new Error(`${dimension} misses the mean or acceptable-rate threshold`);
    const localeRates = Object.fromEntries(["en", "zh-CN"].map((locale) => [locale, acceptableRate(scoreValues(candidate, dimension, (record) => record.locale === locale))]));
    maximumLocaleGap = Math.max(maximumLocaleGap, Math.abs(localeRates.en - localeRates["zh-CN"]) * 100);
    for (const suite of ["specialist", "shared"]) for (const locale of ["en", "zh-CN"]) {
      const filter = (record) => record.suite === suite && record.locale === locale;
      maximumRegression = Math.max(maximumRegression, (acceptableRate(scoreValues(baseline, dimension, filter)) - acceptableRate(scoreValues(candidate, dimension, filter))) * 100);
    }
    dimensionResults[dimension] = { meanScore: mean, acceptableRate: rate, localeRates };
  }
  if (maximumLocaleGap > 5 || maximumRegression > 2) throw new Error("locale parity or baseline regression threshold failed");
  const groupKeys = new Set(candidate.map((record) => `${record.suite}:${record.transition}:${record.locale}`));
  for (const groupKey of groupKeys) {
    const [suite, transition, locale] = groupKey.split(":");
    const reviews = candidate.filter((record) => record.suite === suite && record.transition === transition && record.locale === locale).flatMap((record) => record.reviews);
    const accepted = reviews.filter((review) => dimensions.every((dimension) => review.scores[dimension] >= 4)).length / reviews.length;
    if (accepted < 0.80) throw new Error(`transition group ${groupKey} is below 80%`);
  }
  const agreements = candidate.map((record) => {
    const decisions = record.reviews.map((review) => dimensions.every((dimension) => review.scores[dimension] >= 4));
    return decisions[0] === decisions[1];
  });
  const reviewerAgreement = agreements.filter(Boolean).length / agreements.length;
  if (reviewerAgreement < 0.85) throw new Error("reviewer agreement is below 85%");
  const costs = candidate.map((record) => record.costUsd);
  const latencies = candidate.map((record) => record.latencyMs);
  if (costs.some((cost) => cost > profileContract.maxCostUsd) || percentile(latencies, 0.95) > profileContract.p95LatencyMs) throw new Error("profile cost or p95 latency budget failed");
  return { dimensionResults, maximumRegression: Math.max(0, maximumRegression), maximumLocaleGap, reviewerAgreement, p95LatencyMs: percentile(latencies, 0.95), maximumCostUsd: Math.max(...costs) };
}

export function buildProfileEvaluationReport(records, options) {
  const result = validateProfileEvaluation(records, options);
  if (!options.evidencePath || !hex64.test(options.rawEvidenceSha256) || !hex64.test(options.specialistDatasetHash) || !hex64.test(options.sharedDatasetHash)) throw new Error("profile raw evidence binding is invalid");
  const base = {
    reportVersion: "1.0.0", stage: 6, kind: "profile-eval", status: "passed", generatedAt: options.generatedAt ?? new Date().toISOString(), sourceCommit: options.sourceCommit, worktreeDirty: false, rcHash: options.rcHash, profile: options.profile,
    samples: { specialist: options.specialistSamples.length, shared: options.sharedSamples.length, candidateExecutions: records.filter((record) => record.variant === "candidate").length, baselineExecutions: records.filter((record) => record.variant === "baseline").length, reviewerScoresPerExecution: 2 },
    datasets: { specialistSha256: options.specialistDatasetHash, sharedSha256: options.sharedDatasetHash }, dimensions: result.dimensionResults,
    maximumCoreRegressionPercentagePoints: result.maximumRegression, maximumLocaleGapPercentagePoints: result.maximumLocaleGap, reviewerAgreement: result.reviewerAgreement,
    secretOrPIILeaks: 0, unauthorizedToolCalls: 0, zeroToleranceFailures: 0,
    maximumCostUsd: result.maximumCostUsd, costBudgetUsd: options.profileContract.maxCostUsd, costWithinBudget: true,
    p95LatencyMs: result.p95LatencyMs, p95LatencyBudgetMs: options.profileContract.p95LatencyMs, p95LatencyWithinBudget: true,
    rawEvidence: { path: options.evidencePath, sha256: options.rawEvidenceSha256, records: records.length },
  };
  return { ...base, reportHash: sha256(JSON.stringify(base)) };
}

async function main() {
  const root = resolve(import.meta.dirname, "..");
  const option = (name) => { const index = process.argv.indexOf(name); return index >= 0 ? process.argv[index + 1] : undefined; };
  const profile = option("--profile");
  const sourceCommit = option("--source-commit");
  const rcHash = option("--rc-hash");
  const input = option("--evidence-jsonl");
  if (!input || !profiles.includes(profile)) throw new Error("--profile and --evidence-jsonl are required");
  if (execFileSync("git", ["rev-parse", "HEAD"], { cwd: root, encoding: "utf8" }).trim() !== sourceCommit) throw new Error("profile report source commit must equal HEAD");
  if (execFileSync("git", ["status", "--porcelain=v1"], { cwd: root, encoding: "utf8" }).trim()) throw new Error("profile report requires a clean source worktree");
  const catalog = JSON.parse(await readFile(resolve(root, "contracts/catalog/profile-contracts.json"), "utf8"));
  const profileContract = catalog.profiles.find((item) => item.id === profile);
  const specialist = JSON.parse(await readFile(resolve(root, `profile-evals/${profile}/bilingual-slice.json`), "utf8"));
  const shared = JSON.parse(await readFile(resolve(root, "product-evals/bilingual-transition-evals.json"), "utf8"));
  const specialistDatasetHash = sha256(JSON.stringify(specialist.samples));
  const sharedDatasetHash = sha256(JSON.stringify(shared.samples));
  if (specialist.hash !== specialistDatasetHash || shared.hash !== sharedDatasetHash || specialist.samples.length !== 200 || shared.samples.length !== 600) throw new Error("frozen profile or shared dataset hash is invalid");
  const raw = await readFile(resolve(root, input), "utf8");
  const records = raw.trim().split("\n").filter(Boolean).map((line) => JSON.parse(line));
  const layout = JSON.parse(await readFile(resolve(root, "contracts/release/stage6-evidence-layout.json"), "utf8"));
  const layoutKey = `profile_${profile}`;
  const evidencePath = `${layout.root}/${layout.rawEvidence[layoutKey]}`;
  const evidenceTarget = resolve(root, evidencePath);
  const options = { profile, sourceCommit, rcHash, profileContract, specialistSamples: specialist.samples, sharedSamples: shared.samples, specialistDatasetHash, sharedDatasetHash };
  validateProfileEvaluation(records, options);
  await mkdir(dirname(evidenceTarget), { recursive: true });
  await writeFile(evidenceTarget, raw.endsWith("\n") ? raw : `${raw}\n`);
  const report = buildProfileEvaluationReport(records, { ...options, evidencePath, rawEvidenceSha256: sha256(await readFile(evidenceTarget)) });
  await writeFile(resolve(root, `${layout.root}/${layout.reports[layoutKey]}`), `${JSON.stringify(report, null, 2)}\n`);
  console.log(`profile eval: profile=${profile} samples=800 variants=1600 regression=${report.maximumCoreRegressionPercentagePoints}pp status=passed report=${report.reportHash}`);
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) await main();
