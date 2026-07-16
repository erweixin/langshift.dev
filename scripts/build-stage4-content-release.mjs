import { createHash } from "node:crypto";
import { mkdir, rm, writeFile } from "node:fs/promises";
import { resolve } from "node:path";

import { transitionCatalog } from "./eval-contract.mjs";

const root = resolve(import.meta.dirname, "..");
const releaseVersion = "1.0.0";
const releaseRoot = resolve(root, "product-content", "releases", releaseVersion);
const locales = ["en", "zh-CN"];
const sha256 = (value) => createHash("sha256").update(value).digest("hex");
const encode = (value) => `${JSON.stringify(value, null, 2)}\n`;
const slug = (value) => value.toLowerCase().replaceAll(/[^a-z0-9]+/g, "_").replaceAll(/^_+|_+$/g, "");
const localized = (en, zh) => ({ en, "zh-CN": zh });

const roleIndex = new Map();
const capabilityIndex = new Map();
const addLocalized = (index, id, names, kind) => {
  const existing = index.get(id);
  if (existing && JSON.stringify(existing.name) !== JSON.stringify(names)) {
    throw new Error(`${kind} id collision: ${id}`);
  }
  if (!existing) index.set(id, { id, name: names });
  return id;
};

for (const transition of transitionCatalog) {
  addLocalized(roleIndex, `role_${slug(transition.source.en)}`, transition.source, "role");
  addLocalized(roleIndex, `role_${slug(transition.target.en)}`, transition.target, "role");
  for (let index = 0; index < transition.transfer.en.length; index += 1) {
    addLocalized(capabilityIndex, `cap_${slug(transition.transfer.en[index])}`, localized(transition.transfer.en[index], transition.transfer["zh-CN"][index]), "capability");
  }
  for (let index = 0; index < transition.gaps.en.length; index += 1) {
    addLocalized(capabilityIndex, `cap_${slug(transition.gaps.en[index])}`, localized(transition.gaps.en[index], transition.gaps["zh-CN"][index]), "capability");
  }
}

const capabilityUsage = new Map([...capabilityIndex].map(([id]) => [id, new Set()]));
const roleRequirements = new Map([...roleIndex].map(([id]) => [id, new Map()]));
const transitions = transitionCatalog.map((transition) => {
  const sourceRoleId = `role_${slug(transition.source.en)}`;
  const targetRoleId = `role_${slug(transition.target.en)}`;
  const transferCapabilityIds = transition.transfer.en.map((name) => `cap_${slug(name)}`);
  const gapCapabilityIds = transition.gaps.en.map((name) => `cap_${slug(name)}`);
  for (const capabilityId of transferCapabilityIds) {
    capabilityUsage.get(capabilityId).add("transferable");
    roleRequirements.get(sourceRoleId).set(capabilityId, "practiced");
    roleRequirements.get(targetRoleId).set(capabilityId, "practiced");
  }
  for (const capabilityId of gapCapabilityIds) {
    capabilityUsage.get(capabilityId).add("target_requirement");
    roleRequirements.get(targetRoleId).set(capabilityId, "demonstrated");
  }
  return {
    id: transition.id,
    revision: 1,
    status: "active",
    sourceRoleId,
    targetRoleId,
    transferBridges: transferCapabilityIds.map((fromCapabilityId, index) => ({
      fromCapabilityId,
      toCapabilityId: gapCapabilityIds[index % gapCapabilityIds.length],
      rationale: localized(
        `${transition.transfer.en[index]} provides a working mental model for learning ${transition.gaps.en[index % transition.gaps.en.length]}, but does not prove the target capability.`,
        `${transition.transfer["zh-CN"][index]}可作为学习${transition.gaps["zh-CN"][index % transition.gaps["zh-CN"].length]}的已有心智模型，但不能据此证明目标能力。`,
      ),
      evidenceRule: "cite_active_claim_and_supporting_evidence",
    })),
    gapCapabilityIds,
    firstTaskTemplateId: `task_${transition.id}_${slug(transition.gaps.en[0])}`,
    sourceIds: ["source_lites_editorial_transition_catalog_v1"],
  };
});

const roles = [...roleIndex.values()].map((role) => ({
  ...role,
  revision: 1,
  status: "active",
  requirementIds: [...roleRequirements.get(role.id)].map(([capabilityId, minimumEvidenceLevel]) => ({ capabilityId, minimumEvidenceLevel })),
  sourceIds: ["source_lites_editorial_transition_catalog_v1"],
}));

const capabilities = [...capabilityIndex.values()].map((capability) => ({
  ...capability,
  revision: 1,
  status: "active",
  description: localized(
    `Ability to apply ${capability.name.en} in a bounded, reviewable work product.`,
    `在范围明确、可评审的工作成果中运用${capability.name["zh-CN"]}的能力。`,
  ),
  evidenceLevels: ["inferred", "user_confirmed", "demonstrated", "applied", "reviewer_verified"],
  usage: [...capabilityUsage.get(capability.id)].sort(),
  sourceIds: ["source_lites_editorial_transition_catalog_v1"],
}));

const rubrics = [
  {
    id: "rubric_code_practice_v1", practiceKind: "code",
    dimensions: [
      ["functional_correctness", "Functional correctness", "功能正确性"],
      ["verification", "Deterministic verification", "确定性验证"],
      ["maintainability", "Maintainability", "可维护性"],
      ["reasoning", "Explanation of trade-offs", "权衡说明"],
    ],
  },
  {
    id: "rubric_writing_practice_v1", practiceKind: "writing",
    dimensions: [
      ["audience_fit", "Audience fit", "受众适配"],
      ["accuracy", "Accuracy and traceability", "准确性与可追溯性"],
      ["structure", "Structure", "结构"],
      ["actionability", "Actionability", "可执行性"],
    ],
  },
  {
    id: "rubric_design_practice_v1", practiceKind: "design",
    dimensions: [
      ["problem_fit", "Problem fit", "问题匹配"],
      ["interaction_clarity", "Interaction clarity", "交互清晰度"],
      ["accessibility", "Accessibility", "无障碍"],
      ["validation", "Validation rationale", "验证依据"],
    ],
  },
].map((rubric) => ({
  id: rubric.id,
  revision: 1,
  status: "active",
  practiceKind: rubric.practiceKind,
  scale: { minimum: 1, maximum: 5, acceptableMinimum: 4 },
  dimensions: rubric.dimensions.map(([id, en, zh]) => ({ id, label: localized(en, zh) })),
  scoringRule: "Each dimension is scored independently; deterministic failures cannot be overridden by model self-assessment.",
  sourceIds: ["source_lites_product_method_v1"],
}));

const practiceKinds = ["code", "writing", "design"];
const tasks = transitionCatalog.flatMap((transition, transitionIndex) => transition.gaps.en.map((gap, gapIndex) => {
  const practiceKind = practiceKinds[(transitionIndex + gapIndex) % practiceKinds.length];
  const transferableEn = transition.transfer.en[gapIndex % transition.transfer.en.length];
  const transferableZh = transition.transfer["zh-CN"][gapIndex % transition.transfer["zh-CN"].length];
  const targetZh = transition.gaps["zh-CN"][gapIndex];
  return {
    id: `task_${transition.id}_${slug(gap)}`,
    revision: 1,
    status: "active",
    transitionId: transition.id,
    targetCapabilityId: `cap_${slug(gap)}`,
    practiceKind,
    estimatedMinutes: gapIndex === 0 ? 60 : 90,
    title: localized(`Demonstrate ${gap}`, `证明${targetZh}`),
    brief: localized(
      `Create a bounded ${practiceKind} artifact that demonstrates ${gap}. Use ${transferableEn} as a starting mental model, and state where the analogy stops being valid.`,
      `创建一个范围明确的${practiceKind === "code" ? "代码" : practiceKind === "writing" ? "文字" : "设计"}成果来证明${targetZh}。以${transferableZh}作为起始心智模型，并说明该类比在哪些地方不再成立。`,
    ),
    successCriteria: {
      en: [`The artifact directly exercises ${gap}.`, "The result is bounded and independently reviewable.", "The submission names one limitation or trade-off."],
      "zh-CN": [`成果直接练习${targetZh}。`, "结果范围明确，并可由独立评审者复核。", "提交内容说明至少一个限制或权衡。"],
    },
    rubricId: `rubric_${practiceKind}_practice_v1`,
    evidenceProposal: { level: "demonstrated", requiresDeterministicOrHumanReview: true, automaticCapabilityUpgrade: false },
    sourceIds: ["source_lites_product_method_v1", "source_lites_editorial_transition_catalog_v1"],
  };
}));

const ontology = {
  schemaVersion: "1.0.0",
  releaseVersion,
  supportedLocales: locales,
  evidenceLevelOrder: ["inferred", "user_confirmed", "demonstrated", "applied", "reviewer_verified"],
  roles,
  capabilities,
};
const transitionTemplates = { schemaVersion: "1.0.0", releaseVersion, supportedLocales: locales, transitions };
const taskTemplates = { schemaVersion: "1.0.0", releaseVersion, supportedLocales: locales, tasks };
const rubricVersions = { schemaVersion: "1.0.0", releaseVersion, supportedLocales: locales, rubrics };
const contentSources = {
  schemaVersion: "1.0.0",
  releaseVersion,
  sources: [
    {
      id: "source_lites_editorial_transition_catalog_v1",
      kind: "repository_editorial",
      title: "Lites bilingual career-transition editorial catalog",
      locator: "scripts/eval-contract.mjs",
      revision: "1.0.0",
      claimBoundary: "Defines product transition hypotheses only; it does not claim current labor-market demand, accreditation, or hiring outcomes.",
      reviewOwner: "product-content",
    },
    {
      id: "source_lites_product_method_v1",
      kind: "repository_product_principle",
      title: "Lites capability-transfer product method",
      locator: "README.md",
      revision: "1.0.0",
      claimBoundary: "Defines the Goal, Map, Learn, Practice, Create and Evidence method; it is not employment advice.",
      reviewOwner: "product",
    },
  ],
  updatePolicy: {
    cadenceDays: 90,
    triggerEvents: ["material_role_definition_change", "eval_regression", "user_correction_pattern", "source_license_or_scope_change"],
    requiredReviewRoles: ["product", "content", "ai-quality", "privacy"],
    activationRule: "Publish a new immutable release only after bilingual parity, referential integrity, profile eval and product eval gates pass.",
    rollbackRule: "Restore the previous complete release manifest; never edit an activated release in place.",
  },
};

const files = {
  "career-ontology.json": ontology,
  "transition-templates.json": transitionTemplates,
  "task-templates.json": taskTemplates,
  "rubric-versions.json": rubricVersions,
  "content-sources.json": contentSources,
};

await rm(releaseRoot, { recursive: true, force: true });
await mkdir(releaseRoot, { recursive: true });
const manifestFiles = [];
for (const name of Object.keys(files).sort()) {
  const body = encode(files[name]);
  await writeFile(resolve(releaseRoot, name), body);
  manifestFiles.push({ name, sha256: sha256(body), bytes: Buffer.byteLength(body) });
}
const manifest = {
  manifestVersion: "1.0.0",
  releaseVersion,
  status: "release_candidate",
  supportedLocales: locales,
  files: manifestFiles,
  contentRootSha256: sha256(encode(manifestFiles)),
  activationRequirements: ["stage4_content_gate", "bilingual_product_eval", "five_profile_bilingual_eval", "behavior_control_plane_approval"],
};
await writeFile(resolve(releaseRoot, "manifest.json"), encode(manifest));
console.log(`Built Stage 4 content release ${releaseVersion}: ${roles.length} roles, ${capabilities.length} capabilities, ${transitions.length} transitions and ${tasks.length} tasks.`);
