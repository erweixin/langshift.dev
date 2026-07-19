import { createHash } from "node:crypto";
import { readFile } from "node:fs/promises";
import { resolve } from "node:path";
import { pathToFileURL } from "node:url";

const sha256 = (value) => createHash("sha256").update(value).digest("hex");

export function verifyAccountErasureReport(report, expected) {
  const failures = [];
  const check = (condition, message) => { if (!condition) failures.push(message); };
  check(report.executionResult === "passed", "account erasure execution did not pass");
  check(report.sourceCommit === expected.sourceCommit, "source commit does not match");
  check(report.rcHash === expected.rcHash, "release candidate hash does not match");
  check(report.worktreeDirty === expected.worktreeDirty, "worktree provenance does not match");
  check(report.accounts === 100, "100-account deletion evidence is required");
  check(report.totalSurfaceReceiptRowsAfterRestore === 1200, "restore re-deletion receipts are incomplete");
  check(report.readableSurfacesAfter === 0, "deleted data remains readable");
  check(Array.isArray(report.dependencyGate?.tests) && report.dependencyGate.tests.length === 3, "dependency deletion evidence is incomplete");
  if (expected.worktreeDirty) {
    check(report.status === "failed", "dirty evidence must not pass");
    check(report.evidenceClassification === "current_dirty", "dirty evidence classification is invalid");
  } else {
    check(report.status === "passed", "clean evidence did not pass");
    check(report.evidenceClassification === "current_clean", "clean evidence classification is invalid");
  }
  const { reportHash, ...base } = report;
  check(typeof reportHash === "string" && reportHash === sha256(JSON.stringify(base)), "report hash is invalid");
  if (failures.length) throw new Error(failures.join("; "));
  return true;
}

async function main() {
  const option = (name) => {
    const index = process.argv.indexOf(name);
    return index >= 0 ? process.argv[index + 1] : undefined;
  };
  const reportPath = option("--report");
  const sourceCommit = option("--source-commit");
  const rcHash = option("--rc-hash");
  const dirty = option("--dirty");
  if (!reportPath || !/^[0-9a-f]{40}$/.test(sourceCommit ?? "") || !/^[0-9a-f]{64}$/.test(rcHash ?? "") || !["true", "false"].includes(dirty)) {
    throw new Error("report, source commit, release candidate hash, and dirty provenance are required");
  }
  const report = JSON.parse(await readFile(resolve(reportPath), "utf8"));
  verifyAccountErasureReport(report, { sourceCommit, rcHash, worktreeDirty: dirty === "true" });
  console.log(`account erasure report verified: execution=passed evidence=${report.status}`);
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  await main();
}
