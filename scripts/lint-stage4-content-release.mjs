import { createHash } from "node:crypto";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const releaseRoot = resolve(root, "product-content", "releases", "1.0.0");
const sha256 = (value) => createHash("sha256").update(value).digest("hex");
const load = async (path) => JSON.parse(await readFile(path, "utf8"));
const exactKeys = (value, keys) => JSON.stringify(Object.keys(value).sort()) === JSON.stringify([...keys].sort());
const failures = [];
const check = (id, condition, details) => {
  console.log(`${condition ? "PASS" : "FAIL"} ${id}: ${details}`);
  if (!condition) failures.push(id);
};
const hasLocales = (value) => value && typeof value.en === "string" && value.en.length > 0 && typeof value["zh-CN"] === "string" && value["zh-CN"].length > 0;

const manifest = await load(resolve(releaseRoot, "manifest.json"));
check("MANIFEST-CLOSED", exactKeys(manifest, ["manifestVersion", "releaseVersion", "status", "supportedLocales", "files", "contentRootSha256", "activationRequirements"]), "manifest rejects undeclared release metadata");
check("MANIFEST-RC", manifest.status === "release_candidate" && manifest.activationRequirements.length === 4, "content cannot be mistaken for an activated release");
const currentFiles = [];
for (const declared of manifest.files) {
  const body = await readFile(resolve(releaseRoot, declared.name));
  currentFiles.push({ name: declared.name, sha256: sha256(body), bytes: body.byteLength });
}
check("MANIFEST-FILES", JSON.stringify(currentFiles) === JSON.stringify(manifest.files), `${manifest.files.length} immutable file hashes and sizes match`);
check("MANIFEST-ROOT", sha256(`${JSON.stringify(manifest.files, null, 2)}\n`) === manifest.contentRootSha256, `content root ${manifest.contentRootSha256}`);

const ontology = await load(resolve(releaseRoot, "career-ontology.json"));
const transitionDocument = await load(resolve(releaseRoot, "transition-templates.json"));
const taskDocument = await load(resolve(releaseRoot, "task-templates.json"));
const rubricDocument = await load(resolve(releaseRoot, "rubric-versions.json"));
const sourceDocument = await load(resolve(releaseRoot, "content-sources.json"));
const missionAPI = await load(resolve(root, "contracts", "openapi", "amendments", "v1.15.0", "product-mission-focus.json"));
const roleIds = new Set(ontology.roles.map((item) => item.id));
const capabilityIds = new Set(ontology.capabilities.map((item) => item.id));
const transitionIds = new Set(transitionDocument.transitions.map((item) => item.id));
const taskIds = new Set(taskDocument.tasks.map((item) => item.id));
const rubricIds = new Set(rubricDocument.rubrics.map((item) => item.id));
const sourceIds = new Set(sourceDocument.sources.map((item) => item.id));

check("ONTOLOGY-COUNTS", roleIds.size === ontology.roles.length && capabilityIds.size === ontology.capabilities.length && ontology.roles.length >= 50 && ontology.capabilities.length >= 80, `${ontology.roles.length} roles and ${ontology.capabilities.length} unique capabilities`);
check("ONTOLOGY-BILINGUAL", ontology.roles.every((item) => hasLocales(item.name)) && ontology.capabilities.every((item) => hasLocales(item.name) && hasLocales(item.description)), "every role and capability has English and Simplified Chinese content");
check("ONTOLOGY-EVIDENCE", JSON.stringify(ontology.evidenceLevelOrder) === JSON.stringify(["inferred", "user_confirmed", "demonstrated", "applied", "reviewer_verified"]), "evidence levels avoid false precision and preserve verification state");
check("ROLE-REFERENCES", ontology.roles.every((role) => role.requirementIds.every((item) => capabilityIds.has(item.capabilityId)) && role.sourceIds.every((id) => sourceIds.has(id))), "role requirements and sources resolve");
check("CAPABILITY-SOURCES", ontology.capabilities.every((item) => item.sourceIds.every((id) => sourceIds.has(id))), "capability sources resolve");

check("TRANSITION-COUNT", transitionIds.size === 30 && transitionDocument.transitions.length === 30, "exactly 30 versioned career transitions");
check("TRANSITION-REFERENCES", transitionDocument.transitions.every((item) => roleIds.has(item.sourceRoleId) && roleIds.has(item.targetRoleId) && taskIds.has(item.firstTaskTemplateId) && item.gapCapabilityIds.every((id) => capabilityIds.has(id)) && item.transferBridges.every((bridge) => capabilityIds.has(bridge.fromCapabilityId) && capabilityIds.has(bridge.toCapabilityId) && hasLocales(bridge.rationale)) && item.sourceIds.every((id) => sourceIds.has(id))), "all role, capability, task and source references close");

check("TASK-COUNT", taskIds.size === taskDocument.tasks.length && taskDocument.tasks.length >= 60, `${taskDocument.tasks.length} unique transition-specific task templates`);
check("TASK-KINDS", ["code", "writing", "design"].every((kind) => taskDocument.tasks.some((task) => task.practiceKind === kind)), "code, writing and design practice are represented");
check("TASK-REFERENCES", taskDocument.tasks.every((task) => transitionIds.has(task.transitionId) && capabilityIds.has(task.targetCapabilityId) && rubricIds.has(task.rubricId) && task.sourceIds.every((id) => sourceIds.has(id))), "task transition, capability, rubric and source references resolve");
check("TASK-BILINGUAL", taskDocument.tasks.every((task) => hasLocales(task.title) && hasLocales(task.brief) && task.successCriteria.en.length >= 3 && task.successCriteria["zh-CN"].length === task.successCriteria.en.length), "every task has paired bilingual instructions and success criteria");
check("TASK-EVIDENCE-SAFETY", taskDocument.tasks.every((task) => task.evidenceProposal.automaticCapabilityUpgrade === false && task.evidenceProposal.requiresDeterministicOrHumanReview === true), "templates cannot upgrade capability from model self-assessment");

check("RUBRIC-KINDS", rubricIds.size === 3 && ["code", "writing", "design"].every((kind) => rubricDocument.rubrics.some((rubric) => rubric.practiceKind === kind)), "three practice rubrics are independently versioned");
check("RUBRIC-BILINGUAL", rubricDocument.rubrics.every((rubric) => rubric.scale.acceptableMinimum === 4 && rubric.dimensions.length >= 4 && rubric.dimensions.every((dimension) => hasLocales(dimension.label))), "rubric dimensions are bilingual and retain the fixed acceptance threshold");
check("SOURCE-POLICY", sourceIds.size >= 2 && sourceDocument.updatePolicy.cadenceDays <= 90 && sourceDocument.updatePolicy.requiredReviewRoles.length >= 4 && sourceDocument.updatePolicy.activationRule.includes("immutable"), "sources, review ownership, update triggers, activation and rollback are explicit");
check("MISSION-API-VERSION", missionAPI.amendmentVersion === "1.15.0" && missionAPI.baseContractVersion === "1.14.0" && missionAPI.compatibility === "versioned-client-additive", "Mission/Focus V2 is chained as a versioned additive amendment");
check("MISSION-API-CAS", missionAPI.schemas.MissionStatusChangeRequestV2.required.includes("expected_mission_version") && missionAPI.schemas.MissionStatusChangeRequestV2.required.includes("expected_focus_version") && missionAPI.schemas.MissionFocusChangeRequestV2.required.includes("expected_focus_version"), "status and focus mutations expose every durable CAS token");
check("MISSION-API-FOCUS", missionAPI.schemas.MissionListResponseV2.required.includes("focus") && missionAPI.schemas.MissionMutationResponseV2.required.includes("focus") && missionAPI.schemas.MissionFocusV2.properties.version.minimum === 0, "reads and mutations return the independent Focus version");
check("MISSION-API-BOUNDARY", missionAPI.schemas.MissionFocusChangeRequestV2.properties.replacement_mission_id.type === "null" && missionAPI.operations["missions.update.v2"].replacementRule.includes("required"), "focus target and status replacement semantics are unambiguous");

const productEval = await load(resolve(root, "product-evals", "bilingual-transition-evals.json"));
const evalGroups = Object.groupBy(productEval.samples, (sample) => sample.semanticKey);
check("PRODUCT-EVAL-HASH", sha256(JSON.stringify(productEval.samples)) === productEval.hash, `dataset hash ${productEval.hash}`);
check("PRODUCT-EVAL-COVERAGE", productEval.samples.length === 600 && [...transitionIds].every((id) => productEval.samples.filter((sample) => sample.transition === id).length === 20), "every release transition has ten semantically paired scenarios");
check("PRODUCT-EVAL-PAIRING", Object.values(evalGroups).length === 300 && Object.values(evalGroups).every((samples) => samples.length === 2 && new Set(samples.map((sample) => sample.locale)).size === 2), "300 English/Chinese product scenarios are semantically paired");

for (const profile of ["route_planner", "daily_planner", "coach", "evaluator", "artifact_builder"]) {
  const slice = await load(resolve(root, "profile-evals", profile, "bilingual-slice.json"));
  const pairs = Object.groupBy(slice.samples, (sample) => sample.input.semanticKey);
  check(`PROFILE-${profile.toUpperCase()}-HASH`, sha256(JSON.stringify(slice.samples)) === slice.hash, `dataset hash ${slice.hash}`);
  check(`PROFILE-${profile.toUpperCase()}`, slice.samples.filter((sample) => sample.locale === "en").length === 100 && slice.samples.filter((sample) => sample.locale === "zh-CN").length === 100 && Object.values(pairs).length === 100 && Object.values(pairs).every((samples) => samples.length === 2), "100 paired scenarios per locale");
}

if (failures.length > 0) {
  console.error(`Stage 4 content release gate failed: ${failures.join(", ")}`);
  process.exit(1);
}
const report = {
  reportVersion: "1.0.0",
  reportKind: "stage4_content_release_technical_gate",
  status: "passed",
  overallStage4Status: "in_progress",
  generatedAt: new Date().toISOString(),
  contentRelease: { version: manifest.releaseVersion, status: manifest.status, contentRootSha256: manifest.contentRootSha256, files: manifest.files },
  inventory: { roles: ontology.roles.length, capabilities: ontology.capabilities.length, transitions: transitionDocument.transitions.length, tasks: taskDocument.tasks.length, rubrics: rubricDocument.rubrics.length },
  productEval: { datasetVersion: productEval.datasetVersion, samples: productEval.samples.length, semanticPairs: Object.values(evalGroups).length, hash: productEval.hash },
  profileEvals: Object.fromEntries(await Promise.all(["route_planner", "daily_planner", "coach", "evaluator", "artifact_builder"].map(async (profile) => {
    const slice = await load(resolve(root, "profile-evals", profile, "bilingual-slice.json"));
    return [profile, { datasetVersion: slice.datasetVersion, samples: slice.samples.length, semanticPairs: Object.keys(Object.groupBy(slice.samples, (sample) => sample.input.semanticKey)).length, hash: slice.hash }];
  }))),
  passedChecks: [
    "manifest_integrity", "bilingual_ontology", "referential_integrity", "evidence_upgrade_safety",
    "transition_task_rubric_coverage", "product_eval_bilingual_pairing", "five_profile_bilingual_pairing",
  ],
  notClaimedByThisReport: ["model_outputs_scored_by_two_reviewers", "browser_e2e", "accessibility_manual_review", "28_day_60_participant_pilot", "stage4_approval"],
};
const reportDirectory = resolve(root, "gate-reports", "stage-4");
await mkdir(reportDirectory, { recursive: true });
await writeFile(resolve(reportDirectory, "content-release-report.json"), `${JSON.stringify(report, null, 2)}\n`);
console.log("Stage 4 content release gate passed.");
