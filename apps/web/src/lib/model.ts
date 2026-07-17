export type EvidenceLevel = "inferred" | "user_confirmed" | "demonstrated" | "applied" | "reviewer_verified";

export const evidenceLevelOrder: EvidenceLevel[] = ["inferred", "user_confirmed", "demonstrated", "applied", "reviewer_verified"];

export const evidenceLevelLabels: Record<"en" | "zh-CN", Record<EvidenceLevel, string>> = {
  en: { inferred: "Inferred", user_confirmed: "You confirmed", demonstrated: "Demonstrated", applied: "Applied", reviewer_verified: "Reviewer verified" },
  "zh-CN": { inferred: "初步推断", user_confirmed: "你已确认", demonstrated: "已在任务中展示", applied: "已在项目中应用", reviewer_verified: "评审已验证" },
};

export type Mission = {
  id: string;
  name: string;
  path: string;
  stage: string;
  focus: boolean;
  bridge: { from: string; to: string; rationale: string };
};

export type Capability = {
  id: string;
  name: string;
  level: EvidenceLevel;
  basis: string;
  status: "active" | "testing" | "gap";
};

export const demoMission: Mission = {
  id: "demo-mission-agent",
  name: "Cloud Agent Engineer",
  path: "Frontend Engineer → Cloud Agent Engineer",
  stage: "Stage 1 · Build the runtime mental model",
  focus: true,
  bridge: {
    from: "UI state and async flows",
    to: "durable run lifecycle",
    rationale: "You already reason about transitions and async work. This task tests what changes when state must survive crashes and irreversible effects.",
  },
};

export const demoCapabilities: Capability[] = [
  { id: "state", name: "State modeling", level: "user_confirmed", basis: "Confirmed from your React and state-management work", status: "active" },
  { id: "async", name: "Asynchronous programming", level: "demonstrated", basis: "Demonstrated in a streamed interaction project", status: "active" },
  { id: "lifecycle", name: "Durable run lifecycle", level: "inferred", basis: "Current route hypothesis; today’s task will test it", status: "testing" },
  { id: "effects", name: "Effect reconciliation", level: "inferred", basis: "Not yet supported by evidence", status: "gap" },
];

export const demoTask = {
  id: "demo-task-run-state",
  title: "Model a run lifecycle that cannot move backward",
  minutes: 25,
  judgment: "Explain why completed → running must be rejected",
  why: demoMission.bridge.rationale,
  status: "scheduled" as const,
};

export const demoEvidence = [
  { id: "ev-1", title: "Run state-machine sketch", type: "Practice", state: "Reviewed", level: "demonstrated" as EvidenceLevel, date: "Yesterday" },
  { id: "ev-2", title: "Why state must survive process crashes", type: "Reflection", state: "Private draft", level: "user_confirmed" as EvidenceLevel, date: "2 days ago" },
  { id: "ev-3", title: "Streaming AI chat project", type: "Project", state: "Evidence recorded", level: "applied" as EvidenceLevel, date: "Last week" },
];

export const demoProjects = [
  { id: "project-code", kind: "Code", title: "Crash-safe agent run service", status: "active", milestones: [{ name: "Lifecycle contract", status: "verified" }, { name: "Recovery tests", status: "in_progress" }, { name: "Operational reflection", status: "planned" }] },
  { id: "project-writing", kind: "Writing", title: "From UI state to durable state", status: "draft", milestones: [{ name: "Argument outline", status: "planned" }, { name: "Technical review", status: "planned" }] },
  { id: "project-design", kind: "Design", title: "Agent recovery control room", status: "draft", milestones: [{ name: "Failure journey", status: "planned" }, { name: "Usability rubric", status: "planned" }] },
];
