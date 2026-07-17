import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { calculateApprovalScopeHash, calculateReportHash } from "./stage6-approval-crypto.mjs";

const sha256 = (value) => createHash("sha256").update(value).digest("hex");
const hex40 = /^[0-9a-f]{40}$/;
const hex64 = /^[0-9a-f]{64}$/;
const metricIds = ["route_preview_reached", "route_understood_and_accepted", "first_loop_within_48h", "three_loops_in_week_one", "bounded_project_by_day_28"];
const zeroIds = ["cross_tenant_access", "private_content_misshare", "ungrounded_capability_upgrade", "unapproved_side_effect", "unresolved_p0", "unresolved_p1"];
const without = (value, key) => { const copy = { ...value }; delete copy[key]; return copy; };

function metricMap(rows, name) {
  if (!Array.isArray(rows) || rows.length !== metricIds.length || new Set(rows.map((row) => row.id)).size !== metricIds.length) throw new Error(`${name} must contain every metric exactly once`);
  const values = new Map();
  for (const row of rows) {
    if (!metricIds.includes(row.id) || !Number.isInteger(row.numerator) || !Number.isInteger(row.denominator) || row.denominator < 1 || row.numerator < 0 || row.numerator > row.denominator) throw new Error(`${name} contains an invalid metric row`);
    values.set(row.id, { id: row.id, numerator: row.numerator, denominator: row.denominator, result: row.numerator / row.denominator });
  }
  return values;
}

function ordered(map) { return metricIds.map((id) => map.get(id)); }

function validateMetricChain(values, population, name) {
  if (values.get("route_preview_reached").denominator > population || values.get("route_understood_and_accepted").denominator > values.get("route_preview_reached").numerator || values.get("first_loop_within_48h").denominator > values.get("route_understood_and_accepted").numerator || values.get("three_loops_in_week_one").denominator > values.get("route_understood_and_accepted").numerator || values.get("bounded_project_by_day_28").denominator > values.get("route_understood_and_accepted").numerator) throw new Error(`${name} metric denominator chain is invalid`);
}

function parsePilotEventDataset(raw, cohort) {
  const text = Buffer.isBuffer(raw) ? raw.toString("utf8") : String(raw ?? "");
  const records = text.trim().split("\n").filter(Boolean).map((line, index) => { try { return JSON.parse(line); } catch { throw new Error(`pilot event outcome row ${index + 1} is not JSON`); } });
  if (records.length !== cohort.participantCount) throw new Error("pilot event outcome dataset must contain exactly one row per consented participant");
  const subjectHashes = new Set();
  const transitions = new Set(cohort.stratumCounts.map((row) => row.transition));
  const allowedTop = ["consentStatus", "locale", "outcomes", "schemaVersion", "startedAnonymousOnboarding", "subjectHash", "transition", "withdrawalDeletionReceiptHash", "withdrawalExcludes", "zeroTolerance"].sort();
  for (const record of records) {
    if (JSON.stringify(Object.keys(record).sort()) !== JSON.stringify(allowedTop) || record.schemaVersion !== "1.0.0" || !hex64.test(record.subjectHash ?? "") || subjectHashes.has(record.subjectHash) || !["en", "zh-CN"].includes(record.locale) || !transitions.has(record.transition) || !["retained", "withdrawn"].includes(record.consentStatus) || typeof record.startedAnonymousOnboarding !== "boolean" || !Array.isArray(record.withdrawalExcludes) || new Set(record.withdrawalExcludes).size !== record.withdrawalExcludes.length || record.withdrawalExcludes.some((id) => !metricIds.slice(1).includes(id)) || (record.consentStatus === "withdrawn" ? !hex64.test(record.withdrawalDeletionReceiptHash ?? "") : record.withdrawalDeletionReceiptHash !== null || record.withdrawalExcludes.length !== 0) || !record.outcomes || JSON.stringify(Object.keys(record.outcomes).sort()) !== JSON.stringify([...metricIds].sort()) || !record.zeroTolerance || JSON.stringify(Object.keys(record.zeroTolerance).sort()) !== JSON.stringify([...zeroIds].sort())) throw new Error("pilot event outcome row violates the deidentified closed contract");
    subjectHashes.add(record.subjectHash);
    for (const id of metricIds) {
      const outcome = record.outcomes[id];
      if (!outcome || JSON.stringify(Object.keys(outcome).sort()) !== JSON.stringify(["achieved", "eligible"]) || typeof outcome.eligible !== "boolean" || typeof outcome.achieved !== "boolean" || outcome.achieved && !outcome.eligible) throw new Error("pilot event outcome eligibility is invalid");
    }
    if (record.outcomes.route_preview_reached.eligible !== record.startedAnonymousOnboarding || record.outcomes.route_understood_and_accepted.eligible !== (record.outcomes.route_preview_reached.achieved && !record.withdrawalExcludes.includes("route_understood_and_accepted")) || record.outcomes.first_loop_within_48h.eligible !== (record.outcomes.route_understood_and_accepted.achieved && !record.withdrawalExcludes.includes("first_loop_within_48h")) || record.outcomes.three_loops_in_week_one.eligible !== (record.outcomes.route_understood_and_accepted.achieved && !record.withdrawalExcludes.includes("three_loops_in_week_one")) || record.outcomes.bounded_project_by_day_28.eligible !== (record.outcomes.route_understood_and_accepted.achieved && !record.withdrawalExcludes.includes("bounded_project_by_day_28"))) throw new Error("pilot event outcome causal denominator chain or withdrawal exclusion is invalid");
    if (zeroIds.some((id) => !Number.isInteger(record.zeroTolerance[id]) || record.zeroTolerance[id] < 0)) throw new Error("pilot event outcome zero-tolerance count is invalid");
  }
  const aggregate = (items) => new Map(metricIds.map((id) => [id, { id, numerator: items.filter((record) => record.outcomes[id].achieved).length, denominator: items.filter((record) => record.outcomes[id].eligible).length }]));
  const locales = { en: aggregate(records.filter((record) => record.locale === "en")), "zh-CN": aggregate(records.filter((record) => record.locale === "zh-CN")) };
  const strata = cohort.stratumCounts.map((row) => ({ transition: row.transition, values: aggregate(records.filter((record) => record.transition === row.transition)) }));
  return { records, overall: aggregate(records), locales, strata, zeroTolerance: Object.fromEntries(zeroIds.map((id) => [id, records.reduce((sum, record) => sum + record.zeroTolerance[id], 0)])), retained: records.filter((record) => record.consentStatus === "retained").length, withdrawn: records.filter((record) => record.consentStatus === "withdrawn").length };
}

function equalMetricCounts(left, right) {
  return metricIds.every((id) => left.get(id).numerator === right.get(id).numerator && left.get(id).denominator === right.get(id).denominator);
}

export function buildPilotReport({ protocol, consentDocument, cohort, behaviorManifest, metrics, eventDatasetRaw }, { paths, inputHashes, generatedAt, worktreeDirty = false }) {
  if (protocol?.protocolVersion !== "1.0.0" || protocol.status !== "frozen_before_recruitment" || protocol.durationDays !== 28 || protocol.minimumConsentedParticipants !== 60 || protocol.localeMinimums?.en !== 30 || protocol.localeMinimums?.["zh-CN"] !== 30 || protocol.transitionGroupMinimum !== 6 || protocol.perGroupPerLocaleMinimum !== 5 || !Array.isArray(protocol.metrics) || protocol.metrics.length !== metricIds.length || protocol.groupGapMaximumPoints !== 15 || typeof protocol.withdrawalDenominatorRule !== "string" || !protocol.withdrawalDenominatorRule.includes("eligibility had not begun") || !Array.isArray(protocol.zeroTolerance) || JSON.stringify([...protocol.zeroTolerance].sort()) !== JSON.stringify([...zeroIds].sort()) || consentDocument?.consentVersion !== "1.0.0" || !Array.isArray(consentDocument.requiredAffirmations) || consentDocument.requiredAffirmations.length < 5) throw new Error("frozen pilot protocol or consent document is invalid");
  if (cohort?.protocolVersion !== protocol.protocolVersion || typeof cohort.cohortId !== "string" || cohort.cohortId.length < 1 || cohort.behaviorManifestHash !== behaviorManifest?.manifestHash || !Number.isFinite(Date.parse(cohort.recruitmentOpenedAt)) || !Number.isFinite(Date.parse(cohort.recruitmentClosedAt)) || Date.parse(cohort.recruitmentClosedAt) <= Date.parse(cohort.recruitmentOpenedAt) || !Number.isInteger(cohort.participantCount) || cohort.participantCount < protocol.minimumConsentedParticipants || !hex64.test(cohort.consentRecordHash ?? "") || !hex64.test(cohort.subjectManifestHash ?? "") || !hex40.test(behaviorManifest?.sourceCommit ?? "") || !hex64.test(behaviorManifest?.manifestHash ?? "") || sha256(JSON.stringify(without(behaviorManifest, "manifestHash"))) !== behaviorManifest.manifestHash) throw new Error("pilot cohort or behavior manifest is invalid");
  if (!Number.isInteger(cohort.localeCounts?.en) || !Number.isInteger(cohort.localeCounts?.["zh-CN"]) || cohort.localeCounts.en < protocol.localeMinimums.en || cohort.localeCounts["zh-CN"] < protocol.localeMinimums["zh-CN"] || cohort.localeCounts.en + cohort.localeCounts["zh-CN"] !== cohort.participantCount) throw new Error("pilot locale counts do not conserve the cohort");
  if (!Array.isArray(cohort.stratumCounts) || cohort.stratumCounts.length < protocol.transitionGroupMinimum || new Set(cohort.stratumCounts.map((row) => row.transition)).size !== cohort.stratumCounts.length || cohort.stratumCounts.some((row) => !Number.isInteger(row.en) || !Number.isInteger(row["zh-CN"]) || row.en < protocol.perGroupPerLocaleMinimum || row["zh-CN"] < protocol.perGroupPerLocaleMinimum) || cohort.stratumCounts.reduce((sum, row) => sum + row.en, 0) !== cohort.localeCounts.en || cohort.stratumCounts.reduce((sum, row) => sum + row["zh-CN"], 0) !== cohort.localeCounts["zh-CN"]) throw new Error("pilot transition strata do not conserve locale membership");
  if (metrics?.schemaVersion !== "1.0.0" || metrics.protocolVersion !== protocol.protocolVersion || metrics.cohortId !== cohort.cohortId || metrics.sourceCommit !== behaviorManifest.sourceCommit || metrics.behaviorManifestHash !== behaviorManifest.manifestHash || !hex64.test(metrics.rcHash ?? "") || ![metrics.imageLockSha256, metrics.configSnapshotSha256, metrics.analysisCodeSha256].every((value) => hex64.test(value ?? "")) || !Number.isFinite(Date.parse(metrics.pilotStartedAt)) || !Number.isFinite(Date.parse(metrics.pilotCompletedAt)) || Date.parse(metrics.pilotStartedAt) < Date.parse(cohort.recruitmentClosedAt) || Date.parse(metrics.pilotCompletedAt) - Date.parse(metrics.pilotStartedAt) < protocol.durationDays * 24 * 60 * 60 * 1000 || !Array.isArray(metrics.protocolDeviations) || metrics.protocolDeviations.length !== 0) throw new Error("pilot metric evidence is not bound to a complete 28-day cohort");
  const derived = parsePilotEventDataset(eventDatasetRaw, cohort);
  const consent = metrics.consent;
  if (!consent || consent.consentedParticipants !== cohort.participantCount || consent.retainedParticipants + consent.withdrawnParticipants !== cohort.participantCount || consent.retainedParticipants !== derived.retained || consent.withdrawnParticipants !== derived.withdrawn || consent.withdrawalDeletionReceipts !== consent.withdrawnParticipants || consent.missingConsentRecords !== 0 || consent.prohibitedFieldsFound !== 0 || consent.consentRecordHash !== cohort.consentRecordHash || consent.subjectManifestHash !== cohort.subjectManifestHash) throw new Error("pilot consent, withdrawal, or data-minimization accounting is invalid");
  const overall = metricMap(metrics.metrics, "overall metrics");
  validateMetricChain(overall, cohort.participantCount, "overall");
  if (!equalMetricCounts(overall, derived.overall)) throw new Error("overall pilot metrics do not match the deidentified event outcome dataset");
  const thresholds = new Map(protocol.metrics.map((row) => [row.id, row.threshold]));
  if (metricIds.some((id) => !Number.isFinite(thresholds.get(id)) || overall.get(id).result < thresholds.get(id))) throw new Error("one or more overall pilot metrics missed the frozen threshold");
  if (!metrics.localeMetrics || JSON.stringify(Object.keys(metrics.localeMetrics).sort()) !== JSON.stringify(["en", "zh-CN"].sort())) throw new Error("pilot locale breakdown is incomplete");
  const localeMaps = Object.fromEntries(Object.entries(metrics.localeMetrics).map(([locale, rows]) => [locale, metricMap(rows, `locale ${locale}`)]));
  validateMetricChain(localeMaps.en, cohort.localeCounts.en, "locale en");
  validateMetricChain(localeMaps["zh-CN"], cohort.localeCounts["zh-CN"], "locale zh-CN");
  if (!equalMetricCounts(localeMaps.en, derived.locales.en) || !equalMetricCounts(localeMaps["zh-CN"], derived.locales["zh-CN"])) throw new Error("locale pilot metrics do not match the deidentified event outcome dataset");
  if (!Array.isArray(metrics.stratumMetrics) || metrics.stratumMetrics.length !== cohort.stratumCounts.length || new Set(metrics.stratumMetrics.map((row) => row.transition)).size !== metrics.stratumMetrics.length || JSON.stringify(metrics.stratumMetrics.map((row) => row.transition).sort()) !== JSON.stringify(cohort.stratumCounts.map((row) => row.transition).sort())) throw new Error("pilot transition breakdown is incomplete");
  const stratumMaps = metrics.stratumMetrics.map((row) => ({ transition: row.transition, values: metricMap(row.metrics, `stratum ${row.transition}`) }));
  for (const row of stratumMaps) {
    const population = cohort.stratumCounts.find((item) => item.transition === row.transition).en + cohort.stratumCounts.find((item) => item.transition === row.transition)["zh-CN"];
    validateMetricChain(row.values, population, `stratum ${row.transition}`);
    if (!equalMetricCounts(row.values, derived.strata.find((item) => item.transition === row.transition).values)) throw new Error("transition pilot metrics do not match the deidentified event outcome dataset");
  }
  for (const id of metricIds) {
    if (localeMaps.en.get(id).numerator + localeMaps["zh-CN"].get(id).numerator !== overall.get(id).numerator || localeMaps.en.get(id).denominator + localeMaps["zh-CN"].get(id).denominator !== overall.get(id).denominator || stratumMaps.reduce((sum, row) => sum + row.values.get(id).numerator, 0) !== overall.get(id).numerator || stratumMaps.reduce((sum, row) => sum + row.values.get(id).denominator, 0) !== overall.get(id).denominator) throw new Error(`pilot ${id} group metrics do not sum to the overall metric`);
  }
  const groupRows = [...Object.entries(localeMaps).map(([group, values]) => ({ kind: "locale", group, values })), ...stratumMaps.map((row) => ({ kind: "transition", group: row.transition, values: row.values }))];
  let maximumGroupGapPoints = 0;
  for (const group of groupRows) for (const id of metricIds) maximumGroupGapPoints = Math.max(maximumGroupGapPoints, (overall.get(id).result - group.values.get(id).result) * 100);
  if (maximumGroupGapPoints > protocol.groupGapMaximumPoints + Number.EPSILON) throw new Error("a locale or transition group missed the frozen maximum gap");
  if (!metrics.zeroTolerance || JSON.stringify(Object.keys(metrics.zeroTolerance).sort()) !== JSON.stringify([...zeroIds].sort()) || zeroIds.some((id) => !Number.isInteger(metrics.zeroTolerance[id]) || metrics.zeroTolerance[id] !== 0 || metrics.zeroTolerance[id] !== derived.zeroTolerance[id])) throw new Error("pilot zero-tolerance audit failed, is incomplete, or does not match the event outcome dataset");
  if (!paths || !inputHashes || ![paths.protocol, paths.consent, paths.cohort, paths.behaviorManifest, paths.metrics, paths.eventDataset].every((value) => typeof value === "string" && value.length > 0) || ![inputHashes.protocol, inputHashes.consent, inputHashes.cohort, inputHashes.behaviorManifest, inputHashes.metrics, inputHashes.eventDataset].every((value) => hex64.test(value ?? "")) || inputHashes.eventDataset !== sha256(eventDatasetRaw) || !Number.isFinite(Date.parse(generatedAt))) throw new Error("pilot report input provenance is invalid");
  const evidenceManifest = { protocolSha256: inputHashes.protocol, consentSha256: inputHashes.consent, cohortSha256: inputHashes.cohort, behaviorManifestSha256: inputHashes.behaviorManifest, metricsSha256: inputHashes.metrics, eventDatasetSha256: inputHashes.eventDataset, imageLockSha256: metrics.imageLockSha256, configSnapshotSha256: metrics.configSnapshotSha256, analysisCodeSha256: metrics.analysisCodeSha256, sourceCommit: metrics.sourceCommit, rcHash: metrics.rcHash };
  const base = { reportVersion: "1.0.0", stage: 6, kind: "design-partner-pilot", generatedAt: new Date(generatedAt).toISOString(), completedAt: new Date(metrics.pilotCompletedAt).toISOString(), status: worktreeDirty ? "failed" : "approved", sourceCommit: metrics.sourceCommit, worktreeDirty, rcHash: metrics.rcHash, cohortId: cohort.cohortId, behaviorManifestHash: behaviorManifest.manifestHash, evidenceManifestHash: sha256(JSON.stringify(evidenceManifest)), durationDays: (Date.parse(metrics.pilotCompletedAt) - Date.parse(metrics.pilotStartedAt)) / (24 * 60 * 60 * 1000), participants: { consented: cohort.participantCount, retained: consent.retainedParticipants, withdrawn: consent.withdrawnParticipants, localeCounts: cohort.localeCounts, transitionStrata: cohort.stratumCounts.length }, consent: { missingRecords: 0, prohibitedFieldsFound: 0, withdrawalDeletionReceipts: consent.withdrawalDeletionReceipts, consentRecordHash: consent.consentRecordHash, subjectManifestHash: consent.subjectManifestHash }, metrics: ordered(overall).map((row) => ({ ...row, threshold: thresholds.get(row.id), passed: row.result >= thresholds.get(row.id) })), localeMetrics: Object.fromEntries(Object.entries(localeMaps).map(([locale, values]) => [locale, ordered(values)])), stratumMetrics: stratumMaps.map((row) => ({ transition: row.transition, metrics: ordered(row.values) })), maximumGroupGapPoints, groupGapThresholdPoints: protocol.groupGapMaximumPoints, zeroTolerance: metrics.zeroTolerance, protocolDeviations: [], provenance: { paths, hashes: inputHashes, imageLockSha256: metrics.imageLockSha256, configSnapshotSha256: metrics.configSnapshotSha256, analysisCodeSha256: metrics.analysisCodeSha256 }, signatures: [] };
  const report = { ...base, approvalScopeHash: calculateApprovalScopeHash(base) };
  report.reportHash = calculateReportHash(report);
  return report;
}

async function main() {
  const root = resolve(import.meta.dirname, "..");
  const args = new Map();
  for (let index = 2; index < process.argv.length; index += 2) args.set(process.argv[index], process.argv[index + 1]);
  const paths = { protocol: "pilot-protocols/design-partner-v1.json", consent: "pilot-protocols/consent-v1.json", cohort: args.get("--cohort"), behaviorManifest: args.get("--behavior-manifest"), metrics: args.get("--metrics"), eventDataset: args.get("--event-dataset") };
  const output = args.get("--output");
  if (!output || Object.values(paths).some((value) => !value)) throw new Error("--cohort, --behavior-manifest, --metrics, --event-dataset and --output are required");
  const entries = await Promise.all(Object.entries(paths).map(async ([key, path]) => { const raw = await readFile(resolve(root, path)); return [key, { raw, value: key === "eventDataset" ? null : JSON.parse(raw) }]; }));
  const inputs = Object.fromEntries(entries);
  const currentCommit = execFileSync("git", ["rev-parse", "HEAD"], { cwd: root, encoding: "utf8" }).trim();
  if (inputs.metrics.value.sourceCommit !== currentCommit) throw new Error("pilot evidence is not bound to the current source commit");
  const worktreeDirty = execFileSync("git", ["status", "--porcelain=v1"], { cwd: root, encoding: "utf8" }).trim().length > 0;
  const hashes = Object.fromEntries(Object.entries(inputs).map(([key, input]) => [key, sha256(input.raw)]));
  const report = buildPilotReport({ protocol: inputs.protocol.value, consentDocument: inputs.consent.value, cohort: inputs.cohort.value, behaviorManifest: inputs.behaviorManifest.value, metrics: inputs.metrics.value, eventDatasetRaw: inputs.eventDataset.raw }, { paths, inputHashes: hashes, generatedAt: new Date().toISOString(), worktreeDirty });
  const target = resolve(root, output);
  await mkdir(dirname(target), { recursive: true });
  await writeFile(target, `${JSON.stringify(report, null, 2)}\n`);
  console.log(`${report.status}: participants=${report.participants.consented} duration=${report.durationDays}d maxGroupGap=${report.maximumGroupGapPoints.toFixed(2)}pp report=${report.reportHash}`);
  if (report.status !== "approved") process.exitCode = 1;
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) await main();
