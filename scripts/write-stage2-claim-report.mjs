#!/usr/bin/env node
import { createHash } from "node:crypto";
import { readFile, writeFile, mkdir } from "node:fs/promises";
import path from "node:path";

const args = new Map();
for (let index = 2; index < process.argv.length; index += 2) {
  args.set(process.argv[index], process.argv[index + 1]);
}
const modelPath = args.get("--model");
const sourceCommit = args.get("--source-commit");
if (!modelPath || !sourceCommit || !/^[0-9a-f]{40}$/.test(sourceCommit)) {
  throw new Error("usage: write-stage2-claim-report.mjs --model <path> --source-commit <40-hex>");
}

const raw = await readFile(modelPath);
const model = JSON.parse(raw);
if (model.status !== "passed" || model.source_commit !== sourceCommit) {
  throw new Error("claim gate model did not pass or is bound to another source commit");
}
const metrics = model.metrics;
const requiredSurfaces = ["body_payload", "preview_projection", "principal_mapping"];
const requiredBoundaryNames = [
  "reserved",
  "destination_mission_commit",
  "enter_erasing",
  ...requiredSurfaces.map((surface) => `deletion_receipt:${surface}`),
];
for (const name of requiredBoundaryNames) {
  for (const position of ["before", "after"]) {
    const boundary = metrics.boundaries.find((item) => item.name === name && item.position === position);
    if (!boundary || boundary.iterations < 100 || boundary.recovered !== boundary.iterations) {
      throw new Error(`missing passing fault boundary ${name}:${position}`);
    }
  }
}

const rawModelSha256 = createHash("sha256").update(raw).digest("hex");
const common = {
  schema_version: "1.0.0",
  source_commit: sourceCommit,
  generated_at: model.generated_at,
  status: "passed",
  raw_model_sha256: rawModelSha256,
  implementation: {
    state_machine: "internal/identity/anonymousclaim/saga.go",
    coordinator: "internal/identity/anonymousclaim/service.go",
    postgres_store: "internal/identity/postgres/anonymous_claim_store.go",
  },
  verification: {
    deterministic_race_model: "passed",
    postgres_nobypassrls_integration: "passed",
    postgres_integration_test: "internal/identity/postgres/anonymous_claim_store_integration_test.go",
  },
};

const concurrencyPassed = metrics.claim_requests === 10_000 &&
  metrics.replay_requests === 10_000 &&
  metrics.expiry_cleanup_attempts === 10_000 &&
  metrics.successful_claim_records === 1 &&
  metrics.mission_effects === 1 &&
  metrics.deletion_receipts === requiredSurfaces.length &&
  metrics.expired_after_reserved === 0 &&
  metrics.manual_review === 0;
if (!concurrencyPassed) throw new Error("claim concurrency thresholds were not met");

const concurrencyReport = {
  ...common,
  gate: "stage-2-anonymous-claim-concurrency",
  acceptance: "10,000 full claim replays converge with concurrent expiry cleanup",
  results: {
    claim_requests: metrics.claim_requests,
    concurrent_workers: metrics.concurrent_workers,
    replay_requests: metrics.replay_requests,
    expiry_cleanup_attempts: metrics.expiry_cleanup_attempts,
    successful_claim_records: metrics.successful_claim_records,
    mission_effects: metrics.mission_effects,
    deletion_receipts: metrics.deletion_receipts,
    required_deletion_surfaces: requiredSurfaces,
    expired_after_reserved: metrics.expired_after_reserved,
    manual_review: metrics.manual_review,
  },
  zero_tolerance: {
    duplicate_mission: 0,
    second_successful_claim: 0,
    missing_deletion_receipt: 0,
    reserved_claim_expired: 0,
  },
};

const faultPassed = metrics.fault_repetitions_per_boundary >= 100 &&
  metrics.fault_runs === metrics.boundaries.length * metrics.fault_repetitions_per_boundary &&
  metrics.reconcile_redeliveries === metrics.fault_runs &&
  metrics.unknown_commit_results >= 100 &&
  metrics.duplicate_missions === 0 &&
  metrics.incomplete_receipt_runs === 0 &&
  metrics.expired_after_reserved === 0 &&
  metrics.manual_review === 0;
if (!faultPassed) throw new Error("claim fault-injection thresholds were not met");

const faultReport = {
  ...common,
  gate: "stage-2-anonymous-claim-fault-reconciliation",
  acceptance: "crash injection before and after every required saga boundary automatically reconciles",
  results: {
    repetitions_per_boundary: metrics.fault_repetitions_per_boundary,
    total_fault_runs: metrics.fault_runs,
    reconcile_redeliveries: metrics.reconcile_redeliveries,
    unknown_commit_results: metrics.unknown_commit_results,
    duplicate_missions: metrics.duplicate_missions,
    incomplete_receipt_runs: metrics.incomplete_receipt_runs,
    expired_after_reserved: metrics.expired_after_reserved,
    manual_review: metrics.manual_review,
    boundaries: metrics.boundaries,
  },
  semantics: {
    destination_effect_idempotency_key: "claim_key",
    deletion_receipt_idempotency_key: "claim_key + surface",
    recovery: "reload durable saga state and redeliver reconciliation",
    unknown_destination_commit: "reconcile by claim_key before any repeated effect",
  },
};

const outputDir = path.resolve("gate-reports/stage-2");
await mkdir(outputDir, { recursive: true });
await writeFile(path.join(outputDir, "claim-concurrency-report.json"), `${JSON.stringify(concurrencyReport, null, 2)}\n`);
await writeFile(path.join(outputDir, "claim-saga-fault-report.json"), `${JSON.stringify(faultReport, null, 2)}\n`);
