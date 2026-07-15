#!/usr/bin/env node
import { createHash } from "node:crypto";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import path from "node:path";

const args = new Map();
for (let index = 2; index < process.argv.length; index += 2) args.set(process.argv[index], process.argv[index + 1]);
const input = args.get("--input");
const sourceCommit = args.get("--source-commit");
if (!input || !/^[0-9a-f]{40}$/.test(sourceCommit ?? "")) throw new Error("usage: write-stage3-provider-egress-report.mjs --input <go-test.jsonl> --source-commit <40-hex>");

const raw = await readFile(input);
const records = raw.toString("utf8").split("\n").filter(Boolean).map((line) => JSON.parse(line));
const packageName = "github.com/langshift/lites/internal/llmgateway/egress";
const gateTest = "TestStage3ProviderEgressGate";
const requiredTests = [
  gateTest, "TestEndpointPolicyRejectsEverySpecialAddressAndMixedDNSAnswer",
  "TestBoundDialerPinsValidatedIPAndBlocksDNSRebinding", "TestBrokerRedirectPolicyRejectsOriginSchemePortAndLoops",
  "TestBrokerInjectsSecretOnlyAfterExactOriginBinding",
];
const passes = requiredTests.map((name) => records.find((record) => record.Package === packageName && record.Test === name && record.Action === "pass"));
const metricRecord = records.find((record) => record.Package === packageName && record.Test === gateTest && record.Action === "output" && record.Output?.includes("provider_egress_gate="));
const failed = records.filter((record) => record.Action === "fail");
if (passes.some((record) => !record) || !metricRecord || failed.length) throw new Error("Provider egress tests did not pass in a completely passing suite");
const marker = metricRecord.Output.indexOf("provider_egress_gate=") + "provider_egress_gate=".length;
const metrics = JSON.parse(metricRecord.Output.slice(marker).trim());
const requiredCategories = ["loopback", "private_network", "link_local", "metadata", "dns_rebinding", "redirect", "byok_non_bound_host"];
const valid = metrics.scenario === "provider_egress_security" && metrics.attacks === requiredCategories.length
  && metrics.blocked === metrics.attacks && metrics.block_rate_percent === 100 && metrics.byok_non_bound_host_sends === 0
  && requiredCategories.every((category) => metrics.categories.includes(category));
if (!valid) throw new Error("Provider egress metrics do not satisfy the Stage 3 zero-tolerance gate");

const reportBase = {
  reportVersion: "1.0.0", stage: 3, kind: "provider-egress-security",
  generatedAt: passes.map((record) => record.Time).sort().at(-1), sourceCommit, status: "passed",
  evidence: {
    rawGoTestJsonSha256: createHash("sha256").update(raw).digest("hex"),
    package: packageName,
    tests: requiredTests.map((name) => ({ name, source: name === gateTest ? "internal/llmgateway/egress/gate_test.go" : "internal/llmgateway/egress/client_test.go or policy_test.go" })),
  },
  results: metrics,
  verifiedInvariants: {
    addressValidation: "all DNS answers must be globally routable and outside platform networks on initial validation and every connect",
    redirectValidation: "redirects cannot change scheme, host, port or credential origin",
    hostBoundSecrets: "BYOK is resolved and injected only after exact canonical host binding",
    dnsPinning: "the transport dials only the revalidated public IP, never a rebinding answer",
  },
  zeroToleranceFailures: [],
};
const report = { ...reportBase, reportHash: createHash("sha256").update(JSON.stringify(reportBase)).digest("hex") };
const output = path.resolve("gate-reports/stage-3/provider-egress-security-report.json");
await mkdir(path.dirname(output), { recursive: true });
await writeFile(output, `${JSON.stringify(report, null, 2)}\n`);
console.log(`stage-3 Provider egress gate: attacks=${metrics.attacks} blocked=${metrics.blocked} status=passed report=${report.reportHash}`);
