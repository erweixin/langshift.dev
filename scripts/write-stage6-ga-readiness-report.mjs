import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import { access, mkdir, readFile, writeFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { verifyStage6Approvals } from "./stage6-approval-crypto.mjs";
import { calculateFinalEvidenceManifestHash, requiredFinalEvidencePaths } from "./stage6-evidence-manifest.mjs";
import { validateLegalGovernance } from "./verify-legal-governance.mjs";

const root = resolve(import.meta.dirname, "..");
const migrationManifest = JSON.parse(await readFile(resolve(root, "deploy/migrations/manifest.json"), "utf8"));
const currentMigration = migrationManifest.migrations.at(-1).version;
const hash = (value) => createHash("sha256").update(JSON.stringify(value)).digest("hex");
const results = [];
const check = (id, status, details, evidence = null) => results.push({ id, status, details, evidence });
async function jsonEvidence(path, predicate = (value) => value.status === "passed") {
  try {
    const raw = await readFile(resolve(root, path), "utf8");
    const value = JSON.parse(raw);
    return { present: true, passed: Boolean(predicate(value)), hash: createHash("sha256").update(raw).digest("hex"), value };
  } catch {
    return { present: false, passed: false, hash: null, value: null };
  }
}
async function fileEvidence(path) {
  try { await access(resolve(root, path)); return true; } catch { return false; }
}
async function blobEvidence(path) {
  try {
    const raw = await readFile(resolve(root, path));
    return { present: true, hash: createHash("sha256").update(raw).digest("hex") };
  } catch {
    return { present: false, hash: null };
  }
}
async function artifactEvidence(path) {
  try {
    const raw = await readFile(resolve(root, path));
    return { present: true, path, sha256: createHash("sha256").update(raw).digest("hex"), sizeBytes: raw.length };
  } catch {
    return { present: false, path, sha256: null, sizeBytes: 0 };
  }
}
function without(value, key) {
  const copy = { ...value };
  delete copy[key];
  return copy;
}
function sourceBlobHash(commit, path) {
  return createHash("sha256").update(execFileSync("git", ["show", `${commit}:${path}`], { cwd: root })).digest("hex");
}
function validSourceArtifacts(artifacts, commit, expectedPaths = null) {
  if (!Array.isArray(artifacts) || artifacts.length === 0) return false;
  const paths = artifacts.map((artifact) => artifact.path);
  if (new Set(paths).size !== paths.length) return false;
  if (expectedPaths && JSON.stringify([...paths].sort()) !== JSON.stringify([...expectedPaths].sort())) return false;
  return artifacts.every((artifact) => /^[0-9a-f]{64}$/.test(artifact.sha256 ?? "") && sourceBlobHash(commit, artifact.path) === artifact.sha256);
}
function verifySigstoreBlob(path, bundle) {
  const identity = privateDelivery.releaseEvidence.cosignCertificateIdentity;
  const issuer = privateDelivery.releaseEvidence.cosignCertificateOIDCIssuer;
  if (!identity || !issuer) return false;
  try {
    execFileSync("cosign", ["verify-blob", "--bundle", resolve(root, bundle), "--certificate-identity", identity, "--certificate-oidc-issuer", issuer, resolve(root, path)], { cwd: root, stdio: "ignore" });
    return true;
  } catch {
    return false;
  }
}
function verifyEvidenceAuthority(path, bundle, authorityClass) {
  const authorities = privateDelivery.stage6EvidenceAuthorities?.[authorityClass] ?? [];
  for (const authority of authorities) {
    try {
      execFileSync("cosign", ["verify-blob", "--bundle", resolve(root, bundle), "--certificate-identity", authority.certificateIdentity, "--certificate-oidc-issuer", authority.certificateOIDCIssuer, resolve(root, path)], { cwd: root, stdio: "ignore" });
      return true;
    } catch {
      // An evidence class may intentionally allow multiple exact authorities.
    }
  }
  return false;
}
function validReportHash(value) {
  return /^[0-9a-f]{64}$/.test(value?.reportHash ?? "") && hash(without(value, "reportHash")) === value.reportHash;
}
async function signedJsonEvidence(path, authorityClass, predicate) {
  const report = await jsonEvidence(path, (value) => validReportHash(value) && predicate(value));
  const bundlePath = path.replace(/\.json$/, ".sigstore.json");
  const signaturePresent = await fileEvidence(bundlePath);
  const signatureVerified = report.present && signaturePresent && verifyEvidenceAuthority(path, bundlePath, authorityClass);
  return { ...report, signaturePresent, signatureVerified };
}
const signedDetails = (report, path) => report.present ? { path, hash: report.hash, status: report.value?.status, signaturePresent: report.signaturePresent, signatureVerified: report.signatureVerified } : null;

const currentCommit = execFileSync("git", ["rev-parse", "HEAD"], { cwd: root, encoding: "utf8" }).trim();
const dirty = execFileSync("git", ["status", "--porcelain=v1"], { cwd: root, encoding: "utf8" }).trim().length > 0;
check("RC-CLEAN-SOURCE", dirty ? "pending" : "passed", dirty ? "a GA RC cannot be cut from the current dirty worktree" : "worktree is clean", { currentCommit });

const legalConfig = JSON.parse(await readFile(resolve(root, "config/legal-governance.json"), "utf8"));
let legalActive = false;
try {
  legalActive = validateLegalGovernance(legalConfig, { requireActive: true }).releaseEligible;
} catch {
  legalActive = false;
}
const legalArtifacts = await Promise.all(["CLA.md", "CONTRIBUTING.md", "TRADEMARKS.md", "SECURITY.md", "NOTICE", "apps/web/src/components/legal-notice.tsx"].map(fileEvidence));
check("LEGAL-GOVERNANCE", legalActive && legalArtifacts.every(Boolean) ? "passed" : "pending", "GA requires a real active rights holder, governing law and CLA submission authority, plus source-shipped contribution, trademark, security, licence and in-product legal notices", { activation: legalConfig.status, structuralArtifactsPresent: legalArtifacts.every(Boolean) });

const privateDelivery = JSON.parse(await readFile(resolve(root, "deploy/private-delivery/manifest.json"), "utf8"));
const stage6ApproverKeyring = JSON.parse(await readFile(resolve(root, "contracts/release/stage6-approver-keyring.json"), "utf8"));
const evidenceLayout = JSON.parse(await readFile(resolve(root, "contracts/release/stage6-evidence-layout.json"), "utf8"));
if (evidenceLayout.layoutVersion !== "1.0.0" || evidenceLayout.root !== "release-evidence/current" || evidenceLayout.signatureSuffix !== ".sigstore.json") throw new Error("Stage 6 evidence layout is invalid");
const releaseEvidencePath = (key) => `${evidenceLayout.root}/${evidenceLayout.reports[key]}`;
const releaseRawPath = (key) => `${evidenceLayout.root}/${evidenceLayout.rawEvidence[key]}`;
const expectedServices = [...privateDelivery.releaseImages, "reference-tool-runtime"].sort();
const expectedRCArtifacts = [...privateDelivery.requiredSourceArtifacts, "deploy/private-delivery/manifest.json", "behavior-manifests/schema.json", "behavior-manifests/impact-matrix.json"].sort();
const rc = await jsonEvidence("release-candidates/current/ga-release-candidate.json", (value) => {
  const services = value.images?.map((image) => image.service).sort() ?? [];
  return value.schemaVersion === "1.0.0" && value.releaseKind === "ga_release_candidate" && value.sourceCommit === currentCommit && value.immutable === true &&
    /^[0-9a-f]{64}$/.test(value.releaseCandidateHash ?? "") && hash(without(value, "releaseCandidateHash")) === value.releaseCandidateHash &&
    JSON.stringify(services) === JSON.stringify(expectedServices) && new Set(services).size === services.length &&
    value.images.every((image) => typeof image.image === "string" && image.image.length > 0 && /^sha256:[0-9a-f]{64}$/.test(image.digest ?? "") && image.sbomAttested === true && image.provenanceMode === "max" && image.signatureVerified === true) &&
    validSourceArtifacts(value.artifacts, currentCommit, expectedRCArtifacts);
});
const behavior = await jsonEvidence("release-candidates/current/product-behavior-manifest.json", (value) => Boolean(rc.value) && value.sourceCommit === currentCommit && value.manifestHash === rc.value.behaviorManifest?.manifestHash && hash(without(value, "manifestHash")) === value.manifestHash && Array.isArray(value.components) && value.components.length > 0);
const rcSignature = await fileEvidence("release-candidates/current/ga-release-candidate.sigstore.json");
const rcSignatureVerified = rcSignature && verifySigstoreBlob("release-candidates/current/ga-release-candidate.json", "release-candidates/current/ga-release-candidate.sigstore.json");
const behaviorBound = behavior.passed && behavior.hash === rc.value?.behaviorManifest?.sha256;
check("SIGNED-UNIQUE-RC", rc.passed && behaviorBound && rcSignatureVerified ? "passed" : "pending", "one commit-bound RC must include every signed image digest, SBOM/provenance assertion, behavior manifest, source artifact and a cryptographically verified Sigstore bundle", rc.present ? { hash: rc.hash, sourceCommit: rc.value?.sourceCommit, behaviorBound, signaturePresent: rcSignature, signatureVerified: rcSignatureVerified } : null);

for (const [stage, path] of [[1, releaseEvidencePath("stage1")], [2, releaseEvidencePath("stage2")], [3, releaseEvidencePath("stage3")], [4, releaseEvidencePath("stage4")], [5, releaseEvidencePath("stage5")]]) {
  const report = await signedJsonEvidence(path, "automatedReports", (value) => Boolean(rc.value) && value.status === "passed" && value.worktreeDirty !== true && (value.sourceCommit === rc.value.sourceCommit || stage === 4 && value.pilotSnapshotHash));
  check(`STAGE-${stage}-RC-GATE`, report.passed && report.signatureVerified ? "passed" : "pending", `Stage ${stage} final gate must be passed, source-bound and signed by an authorized automation identity on the RC (approved pilot evidence may use the behavior-equivalence rule)`, signedDetails(report, path));
}

const penetrationPath = releaseEvidencePath("penetration");
const penetrationRaw = await blobEvidence(releaseRawPath("penetration"));
const penetration = await signedJsonEvidence(penetrationPath, "externalPenetration", (value) => value.reportVersion === "1.0.0" && value.kind === "external-penetration-test" && value.status === "passed" && value.worktreeDirty === false && value.sourceCommit === rc.value?.sourceCommit && value.unresolvedCritical === 0 && value.unresolvedHigh === 0 && value.forbiddenUnresolved === 0 && value.forbiddenRiskAccepted === 0 && value.externalProvider?.legalName && /^[0-9a-f]{64}$/.test(value.externalProvider?.independenceAttestationSha256 ?? "") && value.engagement?.testerCount >= 2 && value.engagement?.scope?.length === 14 && /^[0-9a-f]{64}$/.test(value.engagement?.providerReportSha256 ?? "") && value.rawEvidence?.sha256 === penetrationRaw.hash && Number.isFinite(Date.parse(value.generatedAt)) && Date.now() >= Date.parse(value.generatedAt) && Date.now() - Date.parse(value.generatedAt) <= 30 * 24 * 60 * 60 * 1000 && value.rcHash === rc.value?.releaseCandidateHash);
check("EXTERNAL-PENETRATION", penetration.passed && penetration.signatureVerified ? "passed" : "pending", "independent penetration testing must be self-hashed, RC-bound and signed by a source-approved external authority, with zero unresolved Critical/High findings and no accepted cross-tenant, secret, side-effect, corruption, or approval-bypass risk", signedDetails(penetration, penetrationPath));

const profiles = ["route_planner", "daily_planner", "coach", "evaluator", "artifact_builder"];
const sharedEvalDataset = JSON.parse(await readFile(resolve(root, "product-evals/bilingual-transition-evals.json"), "utf8"));
const sharedEvalDatasetHash = hash(sharedEvalDataset.samples);
const profileContracts = JSON.parse(await readFile(resolve(root, "contracts/catalog/profile-contracts.json"), "utf8"));
for (const profile of profiles) {
  const path = releaseEvidencePath(`profile_${profile}`);
  const rawEvidence = await blobEvidence(releaseRawPath(`profile_${profile}`));
  const specialistDataset = JSON.parse(await readFile(resolve(root, `profile-evals/${profile}/bilingual-slice.json`), "utf8"));
  const specialistDatasetHash = hash(specialistDataset.samples);
  const contract = profileContracts.profiles.find((item) => item.id === profile);
  const report = await signedJsonEvidence(path, "automatedReports", (value) => value.reportVersion === "1.0.0" && value.kind === "profile-eval" && value.status === "passed" && value.worktreeDirty === false && value.sourceCommit === rc.value?.sourceCommit && value.profile === profile && value.samples?.specialist === 200 && value.samples?.shared === 600 && value.samples?.candidateExecutions === 800 && value.samples?.baselineExecutions === 800 && value.samples?.reviewerScoresPerExecution === 2 && value.rawEvidence?.records === 1600 && value.rawEvidence?.sha256 === rawEvidence.hash && value.datasets?.specialistSha256 === specialistDatasetHash && value.datasets?.sharedSha256 === sharedEvalDatasetHash && Object.values(value.dimensions ?? {}).every((dimension) => dimension.meanScore >= 4 && dimension.acceptableRate >= 0.85 && dimension.localeRates?.en >= 0.80 && dimension.localeRates?.["zh-CN"] >= 0.80) && value.maximumCoreRegressionPercentagePoints <= 2 && value.maximumLocaleGapPercentagePoints <= 5 && value.reviewerAgreement >= 0.85 && value.secretOrPIILeaks === 0 && value.unauthorizedToolCalls === 0 && value.zeroToleranceFailures === 0 && value.maximumCostUsd <= contract.maxCostUsd && value.costBudgetUsd === contract.maxCostUsd && value.costWithinBudget === true && value.p95LatencyMs <= contract.p95LatencyMs && value.p95LatencyBudgetMs === contract.p95LatencyMs && value.p95LatencyWithinBudget === true && value.rcHash === rc.value?.releaseCandidateHash);
  check(`PROFILE-EVAL-${profile.toUpperCase()}`, report.passed && report.signatureVerified ? "passed" : "pending", `${profile} needs signed bilingual specialist and shared eval evidence with <=2pp regression and zero safety redlines`, signedDetails(report, path));
}

const capacityPath = releaseEvidencePath("capacity");
const capacityRaw = await blobEvidence(releaseRawPath("capacity"));
const capacity = await signedJsonEvidence(capacityPath, "automatedReports", (value) => value.reportVersion === "1.0.0" && value.kind === "official-capacity" && value.status === "passed" && value.worktreeDirty === false && value.sourceCommit === rc.value?.sourceCommit && value.steadyMinutes >= 30 && value.burstMultiplier >= 2 && value.burstMinutes >= 5 && value.backlogRecoveryMinutes > 0 && value.backlogRecoveryMinutes <= 15 && value.sloViolations === 0 && value.rawEvidence?.samples >= 38 && value.rawEvidence?.sha256 === capacityRaw.hash && [value.referenceLoadVectorHash, value.environmentFingerprint, value.configSnapshotHash, value.imageLockHash].every((item) => /^[0-9a-f]{64}$/.test(item ?? "")) && value.rcHash === rc.value?.releaseCandidateHash);
check("OFFICIAL-CAPACITY", capacity.passed && capacity.signatureVerified ? "passed" : "pending", "official cloud must provide signed RC-bound evidence for 30 steady minutes plus a 2x five-minute burst, every SLO, and backlog recovery within 15 minutes", signedDetails(capacity, capacityPath));

const recoveryPath = releaseEvidencePath("recovery");
const recoveryRaw = await blobEvidence(releaseRawPath("recovery"));
const recovery = await signedJsonEvidence(recoveryPath, "automatedReports", (value) => value.reportVersion === "1.0.0" && value.kind === "recovery-drills" && value.status === "passed" && value.worktreeDirty === false && value.sourceCommit === rc.value?.sourceCommit && value.requiredConsecutivePasses === 3 && value.rawEvidence?.records === 9 && value.rawEvidence?.sha256 === recoveryRaw.hash && value.instanceOrAZ?.consecutivePasses === 3 && value.instanceOrAZ?.rpoSeconds === 0 && value.instanceOrAZ?.rtoSeconds <= 300 && value.region?.consecutivePasses === 3 && value.region?.rpoSeconds <= 300 && value.region?.rtoSeconds <= 3600 && value.queueLoss?.consecutivePasses === 3 && value.queueLoss?.rtoSeconds <= 900 && value.rcHash === rc.value?.releaseCandidateHash);
check("RECOVERY-DRILLS", recovery.passed && recovery.signatureVerified ? "passed" : "pending", "instance/AZ, region, and queue-loss recovery must provide signed RC-bound evidence meeting fixed RPO/RTO thresholds", signedDetails(recovery, recoveryPath));

const erasureWorker = await fileEvidence("cmd/account-erasure-worker/main.go");
const deletionDatabaseEvidence = await blobEvidence("release-candidates/current/evidence/account-erasure-integration.json");
const deletionDependencyEvidence = await blobEvidence("release-candidates/current/evidence/account-erasure-dependencies.json");
const requiredErasureArtifacts = ["cmd/account-erasure-worker/main.go", "internal/identity/erasure/service.go", "internal/identity/postgres/account_erasure_surface.go", "deploy/migrations/000084_account_erasure_receipts.up.sql", "scripts/install-ga-release-candidate.sh", "scripts/stage6-account-erasure-gate.sh", "scripts/write-stage6-account-erasure-report.mjs", ".github/workflows/supply-chain.yml"];
const deletion = await jsonEvidence("release-candidates/current/account-erasure-100.json", (value) => Boolean(rc.value) && value.status === "passed" && value.worktreeDirty !== true && value.sourceCommit === rc.value.sourceCommit && value.accounts === 100 && value.readableSurfacesAfter === 0 && value.completeReceipts === 100 && value.restoreRedeletions === 100 && value.totalSurfaceReceiptRowsAfterRestore === 1200 && value.dependencyGate?.tests?.length === 3 && Object.values(value.dependencyGate?.runtimes ?? {}).every((runtime) => /@sha256:[0-9a-f]{64}$/.test(runtime)) && value.rcHash === rc.value.releaseCandidateHash && /^[0-9a-f]{64}$/.test(value.reportHash ?? "") && hash(without(value, "reportHash")) === value.reportHash && deletionDatabaseEvidence.hash === value.test?.evidenceSha256 && deletionDependencyEvidence.hash === value.dependencyGate?.evidenceSha256 && requiredErasureArtifacts.every((path) => value.artifacts?.some((artifact) => artifact.path === path)) && validSourceArtifacts(value.artifacts, value.sourceCommit));
const deletionSignature = await fileEvidence("release-candidates/current/account-erasure-100.sigstore.json");
const deletionSignatureVerified = deletionSignature && verifySigstoreBlob("release-candidates/current/account-erasure-100.json", "release-candidates/current/account-erasure-100.sigstore.json");
check("ACCOUNT-ERASURE-100", erasureWorker && deletion.passed && deletionSignatureVerified ? "passed" : "pending", erasureWorker ? "100 randomly selected accounts need signed, RC-bound cross-surface deletion and restore-time re-deletion evidence" : "the production account-erasure executor is not implemented; request scheduling alone cannot satisfy deletion", deletion.present ? { hash: deletion.hash, signaturePresent: deletionSignature, signatureVerified: deletionSignatureVerified } : null);

for (const target of ["compose", "helm-tofu", "official-cloud"]) {
  const layoutKey = `deployment_${target.replaceAll("-", "_")}`;
  const path = releaseEvidencePath(layoutKey);
  const rawEvidence = await blobEvidence(releaseRawPath(layoutKey));
  const report = await signedJsonEvidence(path, "automatedReports", (value) => value.reportVersion === "1.0.0" && value.kind === "deployment-drill" && value.target === target && value.status === "passed" && value.worktreeDirty === false && value.sourceCommit === rc.value?.sourceCommit && value.currentMigration === currentMigration && value.requiredConsecutivePasses === 3 && value.rawEvidence?.records === 15 && value.rawEvidence?.sha256 === rawEvidence.hash && ["freshInstall", "upgrade", "backup", "restore", "rollback"].every((operation) => value.operations?.[operation]?.consecutivePasses === 3 && value.operations[operation].attempts?.length === 3) && value.rcHash === rc.value?.releaseCandidateHash);
  check(`DEPLOYMENT-${target.toUpperCase()}`, report.passed && report.signatureVerified ? "passed" : "pending", `${target} needs signed RC-bound evidence for three consecutive fresh-install, upgrade, backup, restore, and rollback passes`, signedDetails(report, path));
}

const pilotPath = releaseEvidencePath("pilot");
const equivalencePath = releaseEvidencePath("pilot_equivalence");
const pilotProtocolEvidence = await blobEvidence("pilot-protocols/design-partner-v1.json");
const pilotConsentEvidence = await blobEvidence("pilot-protocols/consent-v1.json");
const pilotCohortEvidence = await jsonEvidence("pilot-reports/cohort-manifest.json", (value) => value.protocolVersion === "1.0.0" && value.participantCount >= 60 && value.localeCounts?.en >= 30 && value.localeCounts?.["zh-CN"] >= 30 && value.stratumCounts?.length >= 6 && value.stratumCounts.every((row) => row.en >= 5 && row["zh-CN"] >= 5));
const pilotManifest = await jsonEvidence("pilot-reports/product-behavior-manifest.json", (value) => value.manifestVersion === "1.0.0" && /^[0-9a-f]{40}$/.test(value.sourceCommit ?? "") && /^[0-9a-f]{64}$/.test(value.manifestHash ?? "") && hash(without(value, "manifestHash")) === value.manifestHash && Array.isArray(value.components) && value.components.length > 0);
const pilotManifestEvidence = await blobEvidence("pilot-reports/product-behavior-manifest.json");
const pilotMetricsEvidence = await blobEvidence("pilot-reports/evidence/pilot-metrics.json");
const pilotEventsEvidence = await blobEvidence("pilot-reports/evidence/pilot-events.jsonl");
const pilot = await signedJsonEvidence(pilotPath, "pilotReports", (value) => {
  const expectedEvidenceManifest = { protocolSha256: value.provenance?.hashes?.protocol, consentSha256: value.provenance?.hashes?.consent, cohortSha256: value.provenance?.hashes?.cohort, behaviorManifestSha256: value.provenance?.hashes?.behaviorManifest, metricsSha256: value.provenance?.hashes?.metrics, eventDatasetSha256: value.provenance?.hashes?.eventDataset, imageLockSha256: value.provenance?.imageLockSha256, configSnapshotSha256: value.provenance?.configSnapshotSha256, analysisCodeSha256: value.provenance?.analysisCodeSha256, sourceCommit: value.sourceCommit, rcHash: value.rcHash };
  return value.reportVersion === "1.0.0" && value.kind === "design-partner-pilot" && value.status === "approved" && value.worktreeDirty === false && value.rcHash === rc.value?.releaseCandidateHash && value.sourceCommit === pilotManifest.value?.sourceCommit && value.behaviorManifestHash === pilotManifest.value?.manifestHash && value.evidenceManifestHash === hash(expectedEvidenceManifest) && value.durationDays >= 28 && value.participants?.consented >= 60 && value.participants?.localeCounts?.en >= 30 && value.participants?.localeCounts?.["zh-CN"] >= 30 && value.participants?.transitionStrata >= 6 && value.consent?.missingRecords === 0 && value.consent?.prohibitedFieldsFound === 0 && value.consent?.withdrawalDeletionReceipts === value.participants?.withdrawn && Array.isArray(value.metrics) && value.metrics.length === 5 && value.metrics.every((row) => row.passed === true && row.result >= row.threshold) && Object.keys(value.localeMetrics ?? {}).length === 2 && Object.values(value.localeMetrics ?? {}).every((rows) => Array.isArray(rows) && rows.length === 5) && Array.isArray(value.stratumMetrics) && value.stratumMetrics.length >= 6 && value.stratumMetrics.every((row) => row.metrics?.length === 5) && value.maximumGroupGapPoints <= 15 && Object.values(value.zeroTolerance ?? {}).length === 6 && Object.values(value.zeroTolerance ?? {}).every((count) => count === 0) && Array.isArray(value.protocolDeviations) && value.protocolDeviations.length === 0 && value.provenance?.hashes?.protocol === pilotProtocolEvidence.hash && value.provenance?.hashes?.consent === pilotConsentEvidence.hash && value.provenance?.hashes?.cohort === pilotCohortEvidence.hash && value.provenance?.hashes?.behaviorManifest === pilotManifestEvidence.hash && value.provenance?.hashes?.metrics === pilotMetricsEvidence.hash && value.provenance?.hashes?.eventDataset === pilotEventsEvidence.hash && Number.isFinite(Date.parse(value.completedAt)) && Date.now() >= Date.parse(value.completedAt) && Date.now() - Date.parse(value.completedAt) <= 90 * 24 * 60 * 60 * 1000 && verifyStage6Approvals(value, stage6ApproverKeyring, "pilot");
});
const equivalence = await signedJsonEvidence(equivalencePath, "automatedReports", (value) => value.reportVersion === "1.0.0" && value.kind === "pilot-rc-behavior-equivalence" && value.status === "passed" && value.worktreeDirty === false && value.sourceCommit === rc.value?.sourceCommit && value.rcHash === rc.value?.releaseCandidateHash && value.pilot?.reportHash === pilot.value?.reportHash && value.pilot?.manifestHash === pilot.value?.behaviorManifestHash && value.releaseCandidate?.manifestHash === behavior.value?.manifestHash && value.inputs?.pilotReportSha256 === pilot.hash && value.inputs?.pilotManifestSha256 === pilotManifestEvidence.hash && value.inputs?.rcManifestSha256 === behavior.hash && value.inputs?.releaseCandidateSha256 === rc.hash && value.comparison?.mode === "exact_pilot_scope" && value.comparison?.pilotComponents > 0 && value.comparison?.pilotComponents === value.comparison?.rcComponents && value.comparison?.unchangedComponents === value.comparison?.pilotComponents && value.comparison?.impactRulesEqual === true && Array.isArray(value.comparison?.differences) && value.comparison.differences.length === 0);
check("PILOT-EQUIVALENCE", pilot.passed && pilot.signatureVerified && equivalence.passed && equivalence.signatureVerified ? "passed" : "pending", "a source-authorized, five-party pilot report and signed exact pilot-to-RC behavior comparison are required; any affected behavior change requires a new frozen cohort", pilot.present || equivalence.present ? { pilot: signedDetails(pilot, pilotPath), equivalence: signedDetails(equivalence, equivalencePath) } : null);

const approvalPath = releaseEvidencePath("final_approval");
const finalRequiredPaths = requiredFinalEvidencePaths(evidenceLayout);
const finalEvidenceArtifacts = await Promise.all(finalRequiredPaths.map(artifactEvidence));
const finalEvidenceManifest = calculateFinalEvidenceManifestHash({ sourceCommit: currentCommit, rcHash: rc.value?.releaseCandidateHash ?? "", artifacts: finalEvidenceArtifacts.map(({ present: _present, ...artifact }) => artifact) });
const approval = await signedJsonEvidence(approvalPath, "finalApproval", (value) => value.reportVersion === "1.0.0" && value.kind === "final-approval" && value.status === "approved" && value.worktreeDirty === false && value.sourceCommit === currentCommit && value.rcHash === rc.value?.releaseCandidateHash && finalEvidenceArtifacts.every((item) => item.present) && value.evidenceCount === finalRequiredPaths.length && JSON.stringify(value.evidence) === JSON.stringify(finalEvidenceManifest.manifest.artifacts) && value.evidenceManifestHash === finalEvidenceManifest.hash && value.verifiedReports?.length === 18 && value.legalGovernanceActive === true && Number.isFinite(Date.parse(value.generatedAt)) && Date.now() >= Date.parse(value.generatedAt) && Date.now() - Date.parse(value.generatedAt) <= 30 * 24 * 60 * 60 * 1000 && verifyStage6Approvals(value, stage6ApproverKeyring, "final"));
check("FINAL-APPROVAL", approval.passed && approval.signatureVerified ? "passed" : "pending", "final approval must be signed by a source-authorized release authority and bind at least five accountable signatures to the exact RC and all evidence hashes", signedDetails(approval, approvalPath));

const pending = results.filter((result) => result.status !== "passed");
const base = { reportVersion: "1.0.0", stage: 6, kind: "ga-readiness", generatedAt: new Date().toISOString(), status: pending.length ? "pending" : "passed", summary: { checks: results.length, passed: results.length - pending.length, pending: pending.length }, currentCommit, results };
const report = { ...base, reportHash: hash(base) };
const target = resolve(root, releaseEvidencePath("ga_readiness"));
await mkdir(dirname(target), { recursive: true });
await writeFile(target, `${JSON.stringify(report, null, 2)}\n`);
console.log(`${report.status}: ${report.summary.passed}/${report.summary.checks} GA checks; pending=${report.summary.pending}; report=${report.reportHash}`);
if (pending.length) process.exitCode = 1;
