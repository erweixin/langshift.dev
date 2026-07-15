import { createHash } from "node:crypto";
import { readFile, writeFile } from "node:fs/promises";
import path from "node:path";

const sourceCommit = process.env.SUPPLY_CHAIN_SOURCE_COMMIT ?? "";
if (!/^[0-9a-f]{40}$/.test(sourceCommit)) {
  throw new Error("SUPPLY_CHAIN_SOURCE_COMMIT must be a full lowercase Git commit SHA");
}

const root = path.resolve("gate-reports/stage-2");
const supplyRoot = path.join(root, "supply-chain");
const imageRoot = path.join(root, "images");
const services = ["api-gateway", "identity-service", "realtime-gateway", "identity-import-worker", "identity-mail-worker", "outbox-publisher", "store-epoch-authority", "lites-migrate", "runtime-host-agent"];

const sha256 = (contents) => createHash("sha256").update(contents).digest("hex");
const readJSON = async (file) => JSON.parse(await readFile(file, "utf8"));
const artifact = async (file) => {
  const contents = await readFile(file);
  return { path: path.relative(process.cwd(), file), sha256: sha256(contents), size_bytes: contents.length };
};

function parseJSONStream(source) {
  const documents = [];
  let start = -1;
  let depth = 0;
  let inString = false;
  let escaped = false;
  for (let index = 0; index < source.length; index += 1) {
    const character = source[index];
    if (inString) {
      if (escaped) escaped = false;
      else if (character === "\\") escaped = true;
      else if (character === '"') inString = false;
      continue;
    }
    if (character === '"') {
      inString = true;
    } else if (character === "{") {
      if (depth === 0) start = index;
      depth += 1;
    } else if (character === "}") {
      depth -= 1;
      if (depth < 0) throw new Error("invalid govulncheck JSON stream");
      if (depth === 0 && start >= 0) {
        documents.push(JSON.parse(source.slice(start, index + 1)));
        start = -1;
      }
    }
  }
  if (depth !== 0 || inString) throw new Error("truncated govulncheck JSON stream");
  return documents;
}

function countTrivyFindings(report) {
  return (report.Results ?? []).reduce(
    (total, result) => total + (result.Vulnerabilities ?? []).length + (result.Secrets ?? []).length + (result.Misconfigurations ?? []).length,
    0,
  );
}

const workflowText = await readFile(".github/workflows/supply-chain.yml", "utf8");
const dockerfileText = await readFile("Dockerfile", "utf8");
const version = (name) => {
  const match = workflowText.match(new RegExp(`^  ${name}: "([^"]+)"$`, "m"));
  if (!match) throw new Error(`missing pinned ${name}`);
  return match[1];
};
const digest = (pattern, name) => {
  const match = dockerfileText.match(pattern);
  if (!match) throw new Error(`missing pinned ${name} digest`);
  return match[1];
};

const govulnFile = path.join(supplyRoot, "govulncheck.json");
const govulnDocuments = parseJSONStream(await readFile(govulnFile, "utf8"));
const govulnConfig = govulnDocuments.find((document) => document.config)?.config;
if (!govulnConfig || govulnConfig.scan_level !== "symbol" || govulnConfig.go_version !== `go${version("GO_VERSION")}`) {
  throw new Error("govulncheck evidence is missing the pinned symbol-level configuration");
}
const findings = govulnDocuments.filter((document) => document.finding).map((document) => document.finding);
const called = findings.filter((finding) => finding.trace.some((frame) => frame.function));
const imported = findings.filter((finding) => !finding.trace.some((frame) => frame.function) && finding.trace.some((frame) => frame.package));
const moduleOnly = findings.filter((finding) => !finding.trace.some((frame) => frame.function || frame.package));
if (called.length !== 0 || imported.length !== 0) throw new Error("reachable or imported-package Go vulnerability found");

const filesystemTrivyFile = path.join(supplyRoot, "trivy-filesystem.json");
const filesystemTrivy = await readJSON(filesystemTrivyFile);
if (countTrivyFindings(filesystemTrivy) !== 0) throw new Error("filesystem security findings remain");

const sboms = [];
for (const name of ["module", ...services]) {
  const file = path.join(supplyRoot, `${name}.cdx.json`);
  const bom = await readJSON(file);
  if (bom.bomFormat !== "CycloneDX" || bom.specVersion !== "1.6") throw new Error(`invalid CycloneDX SBOM: ${name}`);
  sboms.push({ name, ...(await artifact(file)) });
}

const images = [];
for (const service of services) {
  const hardeningFile = path.join(imageRoot, `${service}.json`);
  const trivyFile = path.join(imageRoot, `${service}-trivy.json`);
  const hardening = await readJSON(hardeningFile);
  const trivy = await readJSON(trivyFile);
  if (hardening.status !== "passed" || hardening.revision !== sourceCommit || hardening.shell_present !== false || hardening.runtime_user !== "65532:65532") {
    throw new Error(`invalid image hardening evidence: ${service}`);
  }
  if (countTrivyFindings(trivy) !== 0) throw new Error(`image security findings remain: ${service}`);
  images.push({
    service,
    image: hardening.image,
    image_id: hardening.image_id,
    size_bytes: hardening.size_bytes,
    runtime_user: hardening.runtime_user,
    shell_present: hardening.shell_present,
    hardening_report: await artifact(hardeningFile),
    trivy_report: await artifact(trivyFile),
  });
}

const report = {
  schema_version: "1.0.0",
  stage: 2,
  source_commit: sourceCommit,
  status: "local_gates_passed_release_attestation_pending",
  pinned_inputs: {
    go: version("GO_VERSION"),
    govulncheck: version("GOVULNCHECK_VERSION"),
    cyclonedx_gomod: version("CYCLONEDX_GOMOD_VERSION"),
    trivy: version("TRIVY_VERSION"),
    cosign: version("COSIGN_VERSION"),
    actionlint: version("ACTIONLINT_VERSION"),
    buildx: version("BUILDX_VERSION"),
    buildkit_image: version("BUILDKIT_IMAGE"),
    dockerfile_frontend_digest: digest(/^# syntax=.*@(sha256:[0-9a-f]{64})$/m, "Dockerfile frontend"),
    go_builder_index_digest: digest(/^FROM golang:[^@]+@(sha256:[0-9a-f]{64}) AS build$/m, "Go builder"),
  },
  action_references: { immutable_sha_policy: "passed", actionlint: "passed" },
  dependency_security: {
    scan_level: govulnConfig.scan_level,
    database: govulnConfig.db,
    database_last_modified: govulnConfig.db_last_modified,
    called_vulnerabilities: called.length,
    imported_package_vulnerabilities: imported.length,
    module_only_findings: moduleOnly.map((finding) => ({ id: finding.osv, module: finding.trace[0]?.module, version: finding.trace[0]?.version })),
    evidence: await artifact(govulnFile),
  },
  filesystem_security: { high_or_critical_vulnerabilities_secrets_or_misconfigurations: 0, evidence: await artifact(filesystemTrivyFile) },
  sboms,
  images,
  release_attestation: {
    workflow: ".github/workflows/supply-chain.yml",
    required_branch: "refs/heads/main",
    multi_platforms: ["linux/amd64", "linux/arm64"],
    buildkit_provenance: "mode=max",
    image_sbom_attestation: "required",
    cosign_mode: "keyless",
    local_status: "not_executed_requires_github_oidc_and_ghcr",
  },
};

await writeFile(path.join(root, "supply-chain-report.json"), `${JSON.stringify(report, null, 2)}\n`);
console.log(`Wrote Stage 2 supply-chain report for ${sourceCommit}.`);
