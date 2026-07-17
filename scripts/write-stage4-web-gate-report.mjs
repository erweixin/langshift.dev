import { readFile, writeFile, mkdir } from "node:fs/promises";
import { resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const inputPath = resolve(root, ".tmp/stage4-web-e2e.json");
const outputPath = resolve(root, "gate-reports/stage-4/web-journey-report.json");
const payload = JSON.parse(await readFile(inputPath, "utf8"));

const specs = [];
function collect(suites = []) {
  for (const suite of suites) {
    for (const spec of suite.specs ?? []) specs.push(spec);
    collect(suite.suites ?? []);
  }
}
collect(payload.suites);

const executions = specs.flatMap((spec) => (spec.tests ?? []).map((test) => ({
  title: spec.title,
  project: test.projectName,
  statuses: (test.results ?? []).map((result) => result.status),
})));
const passing = (execution) => execution.statuses.length > 0 && execution.statuses.at(-1) === "passed";
const failed = executions.filter((execution) => !passing(execution));
const titles = new Set(executions.filter(passing).map((execution) => execution.title));

const requiredJourneys = {
  anonymous_quick_selection: "quick onboarding produces a correctable route and opens Today",
  anonymous_natural_language: "natural-language onboarding stays editable until Coach structures it",
  capability_confirmation: "quick onboarding produces a correctable route and opens Today",
  route_preview_and_correction: "quick onboarding produces a correctable route and opens Today",
  registration_claim_and_first_task: "quick onboarding produces a correctable route and opens Today",
  code_practice: "complete code Create lifecycle preserves evidence and revision provenance",
  writing_practice: "complete writing Create lifecycle preserves evidence and revision provenance",
  design_practice: "complete design Create lifecycle preserves evidence and revision provenance",
  submit_review_and_evidence: "an online task moves from confirmed submission to review and evidence",
  lower_difficulty: "an online task moves from confirmed submission to review and evidence",
  coach_followup: "core workspace has no serious accessibility violations or capability percentages",
  multi_mission_switch: "multiple Missions can switch Focus without mixing Coach context",
  full_create_project: "complete code Create lifecycle preserves evidence and revision provenance",
  settings_language_timezone_reminder_byok_memory_export_delete: "Settings covers locale, timezone, reminders, BYOK, Memory, export, and erasure",
  pwa_assets: "PWA manifest and offline fallback are installable assets",
  bilingual_mobile_navigation: "Chinese workspace and mobile navigation are first-class",
};

const journeys = Object.fromEntries(Object.entries(requiredJourneys).map(([id, title]) => [id, { status: titles.has(title) ? "passed" : "missing", evidence: title }]));
const missing = Object.entries(journeys).filter(([, value]) => value.status !== "passed").map(([id]) => id);
const status = failed.length === 0 && missing.length === 0 ? "passed" : "failed";
const report = {
  schema_version: 1,
  generated_at: new Date().toISOString(),
  status,
  scope: "automated_web_release_gate",
  browsers: [...new Set(executions.map((execution) => execution.project))].sort(),
  executions: { total: executions.length, passed: executions.filter(passing).length, failed: failed.length },
  journeys,
  missing_journeys: missing,
  failed_executions: failed,
  claims_not_made: [
    "manual assistive-technology review",
    "two-reviewer model-output scoring",
    "28-day pilot with 60 consented participants",
    "Stage 4 release approval",
  ],
};
await mkdir(resolve(outputPath, ".."), { recursive: true });
await writeFile(outputPath, `${JSON.stringify(report, null, 2)}\n`, "utf8");
if (status !== "passed") {
  console.error(JSON.stringify({ status, missing, failed: failed.length }));
  process.exit(1);
}
console.log(JSON.stringify({ status, executions: report.executions, journeys: Object.keys(journeys).length }));
