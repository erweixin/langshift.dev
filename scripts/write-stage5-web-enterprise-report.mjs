import { createHash } from "node:crypto";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const sourcePath = resolve(root, ".tmp/stage4-web-e2e.json");
const source = JSON.parse(await readFile(sourcePath, "utf8"));
const hash = (value) => createHash("sha256").update(typeof value === "string" ? value : JSON.stringify(value)).digest("hex");
const specs = [];
function collect(suites) {
  for (const suite of suites ?? []) {
    for (const spec of suite.specs ?? []) {
      for (const test of spec.tests ?? []) specs.push({ title: spec.title, project: test.projectName, statuses: (test.results ?? []).map((result) => result.status) });
    }
    collect(suite.suites);
  }
}
collect(source.suites);

const titles = [
  "Organization console preserves privileged-action and sensitive-read boundaries",
  "enterprise primary lifecycle covers invitation, governed CSV, cohorts, sharing, revocation, and offboarding",
];
const projects = ["desktop-chromium", "mobile-chromium"];
const expected = titles.flatMap((title) => projects.map((project) => ({ title, project })));
const results = expected.map(({ title, project }) => {
  const found = specs.find((spec) => spec.title === title && spec.project === project);
  const passed = Boolean(found) && found.statuses.length === 1 && found.statuses[0] === "passed";
  return { id: `${project}:${title}`, status: passed ? "passed" : "failed", details: passed ? "browser journey passed" : "missing or non-passing browser result" };
});
const failures = results.filter((result) => result.status === "failed");
const base = {
  reportVersion: "1.0.0",
  stage: 5,
  kind: "enterprise-browser-journey",
  generatedAt: new Date().toISOString(),
  status: failures.length === 0 && source.errors?.length === 0 && source.stats?.unexpected === 0 ? "passed" : "failed",
  summary: { checks: results.length, passed: results.length - failures.length, failed: failures.length },
  source: { path: ".tmp/stage4-web-e2e.json", sha256: hash(await readFile(sourcePath)), expected: source.stats?.expected, unexpected: source.stats?.unexpected, durationMs: source.stats?.duration },
  results,
};
const report = { ...base, reportHash: hash(base) };
const target = resolve(root, "gate-reports/stage-5/enterprise-browser-journey.json");
await mkdir(dirname(target), { recursive: true });
await writeFile(target, `${JSON.stringify(report, null, 2)}\n`);
console.log(`${report.status}: ${report.summary.passed}/${report.summary.checks} browser checks; report ${report.reportHash}`);
if (report.status !== "passed") process.exitCode = 1;
