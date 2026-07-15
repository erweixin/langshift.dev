#!/usr/bin/env node
import { createHash } from "node:crypto";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import path from "node:path";

const args = new Map();
for (let index = 2; index < process.argv.length; index += 2) args.set(process.argv[index], process.argv[index + 1]);
const input = args.get("--input");
const sourceCommit = args.get("--source-commit");
if (!input || !/^[0-9a-f]{40}$/.test(sourceCommit ?? "")) throw new Error("usage: write-stage3-llm-manifest-report.mjs --input <go-test.jsonl> --source-commit <40-hex>");

const raw = await readFile(input);
const records = raw.toString("utf8").split("\n").filter(Boolean).map((line) => JSON.parse(line));
const packageName = "github.com/langshift/lites/internal/llmgateway/postgres";
const testName = "TestProviderDispatchIsAtMostOnceAndFallbackIsFullyAccounted";
const testPass = records.find((record) => record.Package === packageName && record.Test === testName && record.Action === "pass");
const metricRecord = records.find((record) => record.Package === packageName && record.Test === testName && record.Action === "output" && record.Output?.includes("llm_manifest_gate="));
const failed = records.filter((record) => record.Action === "fail");
if (!testPass || !metricRecord || failed.length) throw new Error("LLM manifest tests did not pass in a completely passing PostgreSQL suite");
const marker = metricRecord.Output.indexOf("llm_manifest_gate=") + "llm_manifest_gate=".length;
const metrics = JSON.parse(metricRecord.Output.slice(marker).trim());
const total = metrics.real_provider_requests;
const valid = metrics.scenario === "llm_request_manifest_completeness" && total > 0
  && metrics.provider_attempt_id_complete === total && metrics.model_version_complete === total
  && metrics.usage_complete === total && metrics.cost_complete === total
  && metrics.context_manifest_complete === total && metrics.coverage_percent === 100 && metrics.missing_records === 0;
if (!valid) throw new Error("LLM request manifest metrics do not satisfy the Stage 3 completeness gate");

const reportBase = {
  reportVersion: "1.0.0", stage: 3, kind: "llm-context-manifest-completeness",
  generatedAt: testPass.Time, sourceCommit, status: "passed",
  evidence: {
    rawPostgresGoTestJsonSha256: createHash("sha256").update(raw).digest("hex"),
    database: "PostgreSQL 16 temporary isolated database",
    test: { package: packageName, name: testName, source: "internal/llmgateway/postgres/store_integration_test.go" },
    requestBoundary: "provider attempts with a durable ProviderAttemptDispatchAuthorized event",
  },
  results: metrics,
  requiredFields: ["provider_attempt_id", "provider_request_id", "model_version", "input_tokens", "output_tokens", "cost_microunits", "context_manifest"],
  verifiedInvariants: {
    immutableManifest: "the physical provider attempt hash matches the canonical logical-attempt context_manifest hash",
    exactAccounting: "usage and cost values match the immutable contracts.provider_costs row for the same provider attempt",
    fallbackCoverage: "primary failure, successful fallback and reconciled outcome_unknown provider requests are included",
  },
  zeroToleranceFailures: [],
};
const report = { ...reportBase, reportHash: createHash("sha256").update(JSON.stringify(reportBase)).digest("hex") };
const output = path.resolve("gate-reports/stage-3/context-manifest-completeness-report.json");
await mkdir(path.dirname(output), { recursive: true });
await writeFile(output, `${JSON.stringify(report, null, 2)}\n`);
console.log(`stage-3 LLM manifest gate: requests=${total} coverage=100% status=passed report=${report.reportHash}`);
