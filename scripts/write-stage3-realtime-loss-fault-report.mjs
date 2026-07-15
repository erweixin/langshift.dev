#!/usr/bin/env node
import { createHash } from "node:crypto";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import path from "node:path";

const args = new Map();
for (let index = 2; index < process.argv.length; index += 2) args.set(process.argv[index], process.argv[index + 1]);
const input = args.get("--input");
const postgresInput = args.get("--postgres-input");
const sourceCommit = args.get("--source-commit");
if (!input || !postgresInput || !/^[0-9a-f]{40}$/.test(sourceCommit ?? "")) {
  throw new Error("usage: write-stage3-realtime-loss-fault-report.mjs --input <go-test.jsonl> --postgres-input <go-test.jsonl> --source-commit <40-hex>");
}

const raw = await readFile(input);
const postgresRaw = await readFile(postgresInput);
const records = raw.toString("utf8").split("\n").filter(Boolean).map((line) => JSON.parse(line));
const postgresRecords = postgresRaw.toString("utf8").split("\n").filter(Boolean).map((line) => JSON.parse(line));
const packageName = "github.com/langshift/lites/internal/realtime";
const testName = "TestRealtimeWakeLossFaultInjection100";
const postgresPackage = "github.com/langshift/lites/internal/realtime/postgres";
const postgresTest = "TestReaderEnforcesTenantAndUserScopedReadOnlyBackfill";
const testPass = records.find((record) => record.Package === packageName && record.Test === testName && record.Action === "pass");
const metricRecord = records.find((record) => record.Package === packageName && record.Test === testName && record.Action === "output" && record.Output?.includes("fault_injection="));
const postgresPass = postgresRecords.find((record) => record.Package === postgresPackage && record.Test === postgresTest && record.Action === "pass");
const failed = [...records, ...postgresRecords].filter((record) => record.Action === "fail");
if (!testPass || !metricRecord || !postgresPass || failed.length) throw new Error("Realtime loss tests did not pass in completely passing suites");
const marker = metricRecord.Output.indexOf("fault_injection=") + "fault_injection=".length;
const metrics = JSON.parse(metricRecord.Output.slice(marker).trim());
const valid = metrics.scenario === "realtime_wake_loss"
  && metrics.repetitions >= 100
  && metrics.wake_hints_lost === metrics.repetitions
  && metrics.events_backfilled === metrics.repetitions
  && metrics.contiguous_sequences === metrics.repetitions
  && metrics.duplicate_deliveries === 0
  && metrics.sequence_gaps === 0
  && metrics.lost_event_facts === 0;
if (!valid) throw new Error("Realtime loss metrics do not satisfy the Stage 3 zero-tolerance gate");

const reportBase = {
  reportVersion: "1.0.0",
  stage: 3,
  kind: "realtime-loss-fault-injection",
  generatedAt: [testPass.Time, postgresPass.Time].sort().at(-1),
  sourceCommit,
  status: "passed",
  evidence: {
    rawRealtimeGoTestJsonSha256: createHash("sha256").update(raw).digest("hex"),
    rawPostgresGoTestJsonSha256: createHash("sha256").update(postgresRaw).digest("hex"),
    tests: [
      { package: packageName, name: testName, source: "internal/realtime/stream_test.go" },
      { package: postgresPackage, name: postgresTest, source: "internal/realtime/postgres/reader_integration_test.go" },
    ],
    database: "PostgreSQL 16 temporary isolated database",
    executionRole: "NOBYPASSRLS lites_realtime_service",
  },
  results: { ...metrics, tenant_user_isolation_verified: true, realtime_role_read_only_verified: true },
  verifiedInvariants: {
    wakeIsHint: "NATS wake loss or subscription disconnect cannot lose an event because the live bus is not the fact source",
    cursorRecovery: "periodic catch-up reads the EventStore high-water mark and emits every missing sequence exactly once in order",
    scopedBackfill: "the realtime database role reads only the requested tenant-user sequence and cannot mutate events or cursors",
    durableFacts: "reconnect and polling behavior never manufacture, skip or reorder EventStore facts",
  },
  zeroToleranceFailures: [],
};
const report = { ...reportBase, reportHash: createHash("sha256").update(JSON.stringify(reportBase)).digest("hex") };
const output = path.resolve("gate-reports/stage-3/realtime-loss-fault-report.json");
await mkdir(path.dirname(output), { recursive: true });
await writeFile(output, `${JSON.stringify(report, null, 2)}\n`);
console.log(`stage-3 Realtime loss fault gate: repetitions=${metrics.repetitions} status=passed report=${report.reportHash}`);
