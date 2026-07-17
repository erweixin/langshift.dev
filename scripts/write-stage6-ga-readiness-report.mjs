import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import { access, mkdir, readFile, writeFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
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

const currentCommit = execFileSync("git", ["rev-parse", "HEAD"], { cwd: root, encoding: "utf8" }).trim();
const dirty = execFileSync("git", ["status", "--porcelain=v1"], { cwd: root, encoding: "utf8" }).trim().length > 0;
check("RC-CLEAN-SOURCE", dirty ? "pending" : "passed", dirty ? "a GA RC cannot be cut from the current dirty worktree" : "worktree is clean", { currentCommit });

const rc = await jsonEvidence("release-candidates/current/ga-release-candidate.json", (value) => value.sourceCommit === currentCommit && value.immutable === true && value.images?.every((image) => image.signatureVerified));
const rcSignature = await fileEvidence("release-candidates/current/ga-release-candidate.sigstore.json");
check("SIGNED-UNIQUE-RC", rc.passed && rcSignature ? "passed" : "pending", "one commit-bound RC must include every signed image digest, SBOM/provenance assertion, behavior manifest, and Sigstore bundle", rc.present ? { hash: rc.hash, sourceCommit: rc.value?.sourceCommit, signaturePresent: rcSignature } : null);

for (const [stage, path] of [[1, "gate-reports/stage-1/gate-report.json"], [2, "gate-reports/stage-2/stage2-acceptance-report.json"], [3, "gate-reports/stage-3/stage3-acceptance-report.json"], [4, "gate-reports/stage-4/stage4-acceptance-report.json"], [5, "gate-reports/stage-5/stage5-automated-gate.json"]]) {
  const report = await jsonEvidence(path, (value) => Boolean(rc.value) && value.status === "passed" && value.worktreeDirty !== true && (value.sourceCommit === rc.value.sourceCommit || stage === 4 && value.pilotSnapshotHash));
  check(`STAGE-${stage}-RC-GATE`, report.passed ? "passed" : "pending", `Stage ${stage} final gate must be passed and bound to the RC (approved pilot evidence may use the behavior-equivalence rule)`, report.present ? { path, hash: report.hash, status: report.value?.status } : null);
}

const penetration = await jsonEvidence("gate-reports/stage-6/external-penetration-test.json", (value) => value.status === "passed" && value.unresolvedCritical === 0 && value.unresolvedHigh === 0 && value.externalProvider && value.rcHash === rc.value?.releaseCandidateHash);
check("EXTERNAL-PENETRATION", penetration.passed ? "passed" : "pending", "independent penetration testing must have zero unresolved Critical/High findings and no accepted cross-tenant, secret, side-effect, corruption, or approval-bypass risk", penetration.present ? { hash: penetration.hash } : null);

const profiles = ["route_planner", "daily_planner", "coach", "evaluator", "artifact_builder"];
for (const profile of profiles) {
  const report = await jsonEvidence(`gate-reports/stage-6/profile-eval-${profile}.json`, (value) => value.status === "passed" && value.profile === profile && value.maximumCoreRegressionPercentagePoints <= 2 && value.secretOrPIILeaks === 0 && value.unauthorizedToolCalls === 0 && value.costWithinBudget && value.p95LatencyWithinBudget && value.rcHash === rc.value?.releaseCandidateHash);
  check(`PROFILE-EVAL-${profile.toUpperCase()}`, report.passed ? "passed" : "pending", `${profile} needs bilingual specialist and shared eval evidence with <=2pp regression and zero safety redlines`, report.present ? { hash: report.hash } : null);
}

const capacity = await jsonEvidence("gate-reports/stage-6/official-capacity.json", (value) => value.status === "passed" && value.steadyMinutes >= 30 && value.burstMultiplier >= 2 && value.burstMinutes >= 5 && value.backlogRecoveryMinutes <= 15 && value.sloViolations === 0 && value.rcHash === rc.value?.releaseCandidateHash);
check("OFFICIAL-CAPACITY", capacity.passed ? "passed" : "pending", "official cloud must sustain 30 minutes plus a 2x five-minute burst, meet every SLO, and recover backlog within 15 minutes", capacity.present ? { hash: capacity.hash } : null);

const recovery = await jsonEvidence("gate-reports/stage-6/recovery-drills.json", (value) => value.status === "passed" && value.instanceOrAZ?.rpoSeconds === 0 && value.instanceOrAZ?.rtoSeconds <= 300 && value.region?.rpoSeconds <= 300 && value.region?.rtoSeconds <= 3600 && value.queueLoss?.rtoSeconds <= 900 && value.rcHash === rc.value?.releaseCandidateHash);
check("RECOVERY-DRILLS", recovery.passed ? "passed" : "pending", "instance/AZ, region, and queue-loss recovery must meet fixed RPO/RTO thresholds on the RC", recovery.present ? { hash: recovery.hash } : null);

const erasureWorker = await fileEvidence("cmd/account-erasure-worker/main.go");
const deletion = await jsonEvidence("gate-reports/stage-6/account-erasure-100.json", (value) => value.status === "passed" && value.accounts === 100 && value.readableSurfacesAfter === 0 && value.completeReceipts === 100 && value.restoreRedeletions === 100 && value.rcHash === rc.value?.releaseCandidateHash);
check("ACCOUNT-ERASURE-100", erasureWorker && deletion.passed ? "passed" : "pending", erasureWorker ? "100 randomly selected accounts need complete cross-surface deletion and restore-time re-deletion evidence" : "the production account-erasure executor is not implemented; request scheduling alone cannot satisfy deletion", deletion.present ? { hash: deletion.hash } : null);

for (const target of ["compose", "helm-tofu", "official-cloud"]) {
  const report = await jsonEvidence(`gate-reports/stage-6/deployment-${target}.json`, (value) => value.status === "passed" && ["freshInstall", "upgrade", "backup", "restore", "rollback"].every((operation) => value.operations?.[operation]?.consecutivePasses >= 3) && value.rcHash === rc.value?.releaseCandidateHash);
  check(`DEPLOYMENT-${target.toUpperCase()}`, report.passed ? "passed" : "pending", `${target} needs three consecutive fresh-install, upgrade, backup, restore, and rollback passes`, report.present ? { hash: report.hash } : null);
}

const pilot = await jsonEvidence("pilot-reports/approved.json", (value) => value.status === "approved" && value.signatures?.length >= 5 && value.ageDays <= 90);
const equivalence = await jsonEvidence("gate-reports/stage-6/pilot-rc-behavior-equivalence.json");
check("PILOT-EQUIVALENCE", pilot.passed && equivalence.passed ? "passed" : "pending", "an approved five-party pilot report and exact pilot-to-RC behavior comparison are required; any affected behavior change requires a new frozen cohort", pilot.present || equivalence.present ? { pilotHash: pilot.hash, equivalenceHash: equivalence.hash } : null);

const issues = await jsonEvidence("gate-reports/stage-6/release-issues.json", (value) => value.status === "passed" && value.openP0 === 0 && value.openP1 === 0 && value.rcHash === rc.value?.releaseCandidateHash);
check("RELEASE-ISSUES", issues.passed ? "passed" : "pending", "the RC issue inventory must contain zero open P0/P1 issues", issues.present ? { hash: issues.hash } : null);
const approval = await jsonEvidence("gate-reports/stage-6/final-approval.json", (value) => value.status === "approved" && value.rcHash === rc.value?.releaseCandidateHash && value.signatures?.length >= 5);
check("FINAL-APPROVAL", approval.passed ? "passed" : "pending", "final approval must bind at least five accountable signatures to the exact RC and all evidence hashes", approval.present ? { hash: approval.hash } : null);

const pending = results.filter((result) => result.status !== "passed");
const base = { reportVersion: "1.0.0", stage: 6, kind: "ga-readiness", generatedAt: new Date().toISOString(), status: pending.length ? "pending" : "passed", summary: { checks: results.length, passed: results.length - pending.length, pending: pending.length }, currentCommit, results };
const report = { ...base, reportHash: hash(base) };
const target = resolve(root, "gate-reports/stage-6/ga-readiness.json");
await mkdir(dirname(target), { recursive: true });
await writeFile(target, `${JSON.stringify(report, null, 2)}\n`);
console.log(`${report.status}: ${report.summary.passed}/${report.summary.checks} GA checks; pending=${report.summary.pending}; report=${report.reportHash}`);
if (pending.length) process.exitCode = 1;
