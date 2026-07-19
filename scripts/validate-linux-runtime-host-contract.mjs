import { readFile } from "node:fs/promises";
import { pathToFileURL } from "node:url";
import { resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const contractPath = "contracts/runtime/linux-firecracker-host.contract.json";
const schemaPath = "contracts/runtime/linux-firecracker-host.schema.json";

const exactKeys = (value, keys, label) => {
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new Error(`${label} must be an object`);
  const actual = Object.keys(value).sort();
  const expected = [...keys].sort();
  if (JSON.stringify(actual) !== JSON.stringify(expected)) throw new Error(`${label} keys do not match the strict contract`);
};
const requireEqual = (actual, expected, label) => {
  if (actual !== expected) throw new Error(`${label} must equal ${JSON.stringify(expected)}`);
};
const requireIncludes = (text, tokens, label) => {
  for (const token of tokens) if (!text.includes(token)) throw new Error(`${label} is missing required control ${JSON.stringify(token)}`);
};

export async function loadRuntimeHostFixture(base = root) {
  const contract = JSON.parse(await readFile(resolve(base, contractPath), "utf8"));
  const schema = JSON.parse(await readFile(resolve(base, schemaPath), "utf8"));
  const artifacts = {};
  for (const path of Object.values(contract.artifacts)) artifacts[path] = await readFile(resolve(base, path), "utf8");
  return { contract, schema, artifacts };
}

export function validateRuntimeHostContract(contract, schema, artifacts) {
  exactKeys(contract, ["schemaVersion", "kind", "host", "firecracker", "service", "security", "artifacts", "macosVerification", "evidence"], "contract");
  requireEqual(contract.schemaVersion, "1.0.0", "schemaVersion");
  requireEqual(contract.kind, "linux-firecracker-runtime-host-contract", "kind");

  exactKeys(contract.host, ["operatingSystem", "dedicatedNode", "virtualizationDevice", "cgroupVersion", "initSystem", "pinnedNetworkNamespace"], "host");
  requireEqual(contract.host.operatingSystem, "linux", "host.operatingSystem");
  requireEqual(contract.host.dedicatedNode, true, "host.dedicatedNode");
  requireEqual(contract.host.virtualizationDevice, "/dev/kvm", "host.virtualizationDevice");
  requireEqual(contract.host.cgroupVersion, 2, "host.cgroupVersion");
  requireEqual(contract.host.initSystem, "systemd", "host.initSystem");
  requireEqual(contract.host.pinnedNetworkNamespace, true, "host.pinnedNetworkNamespace");

  exactKeys(contract.firecracker, ["version", "jailerRequired", "immutableRootOwnedAssets", "requiredDigestVariables"], "firecracker");
  requireEqual(contract.firecracker.version, "1.15.1", "firecracker.version");
  requireEqual(contract.firecracker.jailerRequired, true, "firecracker.jailerRequired");
  requireEqual(contract.firecracker.immutableRootOwnedAssets, true, "firecracker.immutableRootOwnedAssets");
  const requiredDigests = ["JAILER_DIGEST", "FIRECRACKER_BINARY_DIGEST", "FIRECRACKER_KERNEL_DIGEST", "FIRECRACKER_ROOTFS_DIGEST", "RUNTIME_SCRATCH_DIGEST"];
  if (JSON.stringify(contract.firecracker.requiredDigestVariables) !== JSON.stringify(requiredDigests)) throw new Error("firecracker.requiredDigestVariables is incomplete or reordered");

  exactKeys(contract.service, ["runAs", "delegateCgroups", "killMode", "preflightRequired", "restartRecoveryFailClosed"], "service");
  requireEqual(contract.service.runAs, "root", "service.runAs");
  requireEqual(contract.service.delegateCgroups, true, "service.delegateCgroups");
  requireEqual(contract.service.killMode, "process", "service.killMode");
  requireEqual(contract.service.preflightRequired, true, "service.preflightRequired");
  requireEqual(contract.service.restartRecoveryFailClosed, true, "service.restartRecoveryFailClosed");

  exactKeys(contract.security, ["mtlsRequired", "clientSpiffeBindingRequired", "fileBackedSecretsRequired", "assetDigestVerificationRequired", "defaultDenyDevicePolicy"], "security");
  for (const [key, value] of Object.entries(contract.security)) requireEqual(value, true, `security.${key}`);

  const expectedArtifacts = {
    readme: "deploy/runtime-host/README.md",
    environmentExample: "deploy/runtime-host/runtime-host-agent.env.example",
    systemdUnit: "deploy/runtime-host/lites-runtime-host-agent.service",
    preflight: "deploy/runtime-host/runtime-host-preflight.sh",
    entrypoint: "cmd/runtime-host-agent/main.go",
    configuration: "cmd/runtime-host-agent/config.go",
    adapter: "internal/runtime/firecracker/jailer.go",
  };
  exactKeys(contract.artifacts, Object.keys(expectedArtifacts), "artifacts");
  for (const [key, path] of Object.entries(expectedArtifacts)) {
    requireEqual(contract.artifacts[key], path, `artifacts.${key}`);
    if (typeof artifacts[path] !== "string" || artifacts[path].length === 0) throw new Error(`artifact ${path} is missing`);
  }

  exactKeys(contract.macosVerification, ["launchesFirecracker", "requiresKvm", "allowedResult"], "macosVerification");
  requireEqual(contract.macosVerification.launchesFirecracker, false, "macosVerification.launchesFirecracker");
  requireEqual(contract.macosVerification.requiresKvm, false, "macosVerification.requiresKvm");
  requireEqual(contract.macosVerification.allowedResult, "fixture_validated", "macosVerification.allowedResult");
  exactKeys(contract.evidence, ["classification", "productionClaims", "linuxExecutionObserved", "isolationStrengthMeasured"], "evidence");
  requireEqual(contract.evidence.classification, "fixture_validated", "evidence.classification");
  for (const key of ["productionClaims", "linuxExecutionObserved", "isolationStrengthMeasured"]) requireEqual(contract.evidence[key], false, `evidence.${key}`);

  exactKeys(schema, ["$schema", "$id", "title", "type", "additionalProperties", "required", "properties"], "schema");
  requireEqual(schema.$schema, "https://json-schema.org/draft/2020-12/schema", "schema.$schema");
  requireEqual(schema.additionalProperties, false, "schema.additionalProperties");
  if (JSON.stringify([...schema.required].sort()) !== JSON.stringify(Object.keys(contract).sort())) throw new Error("schema root required fields drifted from the contract");
  for (const section of ["host", "firecracker", "service", "security", "artifacts", "macosVerification", "evidence"]) {
    requireEqual(schema.properties[section]?.additionalProperties, false, `schema.properties.${section}.additionalProperties`);
    if (JSON.stringify([...schema.properties[section].required].sort()) !== JSON.stringify(Object.keys(contract[section]).sort())) throw new Error(`schema required fields drifted for ${section}`);
  }

  const preflight = artifacts[expectedArtifacts.preflight];
  requireIncludes(preflight, ["/dev/kvm", "cgroup2fs", "JAILER_DIGEST", "FIRECRACKER_BINARY_DIGEST", "FIRECRACKER_KERNEL_DIGEST", "FIRECRACKER_ROOTFS_DIGEST", "RUNTIME_SCRATCH_DIGEST", "FIRECRACKER_NETWORK_NAMESPACE", "root-owned", "8#077"], "runtime host preflight");
  const unit = artifacts[expectedArtifacts.systemdUnit];
  requireIncludes(unit, ["ConditionPathExists=/dev/kvm", "User=root", "ExecStartPre=/usr/local/libexec/lites/runtime-host-preflight", "KillMode=process", "Delegate=yes", "DevicePolicy=closed", "DeviceAllow=/dev/kvm rw"], "runtime host systemd unit");
  const environment = artifacts[expectedArtifacts.environmentExample];
  requireIncludes(environment, ["ALLOW_INSECURE_DEVELOPMENT=false", "FIRECRACKER_NETWORK_NAMESPACE=", "SERVER_ALLOWED_CLIENT_SPIFFE_ID=spiffe://", "LISTEN_ADDRESS=0.0.0.0:8443", "HEALTH_ADDRESS=127.0.0.1:8085", ...requiredDigests.map((name) => `${name}=`)], "runtime host environment");
  requireIncludes(artifacts[expectedArtifacts.readme], ["dedicated Linux/KVM nodes under systemd", "Firecracker and jailer version `1.15.1`", "KillMode=process", "Delegate=yes"], "runtime host README");
  requireIncludes(artifacts[expectedArtifacts.entrypoint], ["syscall.Geteuid() != 0", "AssetDigest", "RegisterHost", "Recover(ctx", "SetHostStatus"], "runtime host entrypoint");
  requireIncludes(artifacts[expectedArtifacts.configuration], ["production runtime host requires file-backed mTLS credentials", "runtime host digest is invalid", "SERVER_ALLOWED_CLIENT_SPIFFE_ID"], "runtime host configuration");
  requireIncludes(artifacts[expectedArtifacts.adapter], ["--cgroup-version", "--new-pid-ns", "memory.swap.max=0", "--http-api-max-payload-size"], "Firecracker jailer adapter");
  return true;
}

async function main() {
  const fixture = await loadRuntimeHostFixture();
  validateRuntimeHostContract(fixture.contract, fixture.schema, fixture.artifacts);
  console.log("Linux runtime host contract validated: static configuration only; no KVM/Firecracker execution or isolation claim");
}

if (process.argv[1] && pathToFileURL(resolve(process.argv[1])).href === import.meta.url) {
  await main();
}
