import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const output = resolve(root, process.env.LITES_MACOS_PRODUCT_REPORT ?? ".tmp/verification/macos-product-stack.json");
const executionResult = process.argv[2];
if (executionResult !== "passed" && executionResult !== "failed") throw new Error("result must be passed or failed");
const failureReason = executionResult === "failed" ? (process.env.LITES_MACOS_PRODUCT_FAILURE_REASON?.trim() || "unspecified_failure") : null;
const failureDetail = executionResult === "failed" ? (process.env.LITES_MACOS_PRODUCT_FAILURE_DETAIL?.trim() || null) : null;
const sourceCommit = execFileSync("git", ["rev-parse", "HEAD"], { cwd: root, encoding: "utf8" }).trim();
const dirty = Boolean(execFileSync("git", ["status", "--porcelain=v1", "--untracked-files=all"], { cwd: root, encoding: "utf8" }).trim());
const result = executionResult === "passed" && !dirty ? "passed" : "failed";
const files = ["deploy/compose/foundation.compose.yaml", "deploy/compose/local-product.compose.yaml", "deploy/compose/postgres-init-local.sh"];
const contracts = {};
for (const name of files) contracts[name] = createHash("sha256").update(await readFile(resolve(root, name))).digest("hex");
const base = {
  reportVersion: "1.0.0",
  kind: "macos-product-stack-smoke",
  sourceCommit,
  worktreeDirty: dirty,
  generatedAt: new Date().toISOString(),
  result,
  executionResult,
  failureReason,
  failureDetail,
  evidenceClassification: executionResult === "passed" && dirty ? "current_dirty" : result,
  fixtureValidated: false,
  usesTestProviderAndRuntimeAdapters: true,
  commercialGA: false,
  claims: {
    dockerDesktopSingleFailureDomain: true,
    realHTTPAndDatabaseJourney: executionResult === "passed",
    backupRestoreSmoke: executionResult === "passed",
    observabilityConfigurationContract: true,
    otelLokiDataPlaneSmoke: false,
    productionProviderQuality: false,
    productionIsolationHAOrDR: false,
  },
  contracts,
  toolVersions: { node: process.version, testHarness: "macos-product-stack@1.0.0" },
};
const report = { ...base, reportHash: createHash("sha256").update(JSON.stringify(base)).digest("hex") };
await mkdir(dirname(output), { recursive: true });
await writeFile(output, `${JSON.stringify(report, null, 2)}\n`);
console.log(`macOS product stack execution=${executionResult} evidence=${result} report=${output}`);
