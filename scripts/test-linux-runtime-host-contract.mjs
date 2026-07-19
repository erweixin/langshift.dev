import assert from "node:assert/strict";
import { loadRuntimeHostFixture, validateRuntimeHostContract } from "./validate-linux-runtime-host-contract.mjs";

const fixture = await loadRuntimeHostFixture();
assert.equal(validateRuntimeHostContract(fixture.contract, fixture.schema, fixture.artifacts), true);

const cases = [
  ["missing KVM requirement", (copy) => { copy.contract.host.virtualizationDevice = "none"; }],
  ["missing immutable digest", (copy) => { copy.contract.firecracker.requiredDigestVariables.pop(); }],
  ["restart recovery not fail closed", (copy) => { copy.contract.service.restartRecoveryFailClosed = false; }],
  ["production claim forged", (copy) => { copy.contract.evidence.productionClaims = true; }],
  ["fixture promoted to passed", (copy) => { copy.contract.evidence.classification = "passed"; }],
  ["macOS requires KVM", (copy) => { copy.contract.macosVerification.requiresKvm = true; }],
  ["preflight omits cgroup v2", (copy) => { const path = copy.contract.artifacts.preflight; copy.artifacts[path] = copy.artifacts[path].replace("cgroup2fs", "legacy-cgroup"); }],
  ["systemd opens all devices", (copy) => { const path = copy.contract.artifacts.systemdUnit; copy.artifacts[path] = copy.artifacts[path].replace("DevicePolicy=closed", "DevicePolicy=auto"); }],
];
for (const [name, mutate] of cases) {
  const copy = structuredClone(fixture);
  mutate(copy);
  assert.throws(() => validateRuntimeHostContract(copy.contract, copy.schema, copy.artifacts), undefined, name);
}

console.log(`Linux runtime host contract self-test passed: positive fixture accepted and ${cases.length} unsafe/forged fixtures rejected`);
