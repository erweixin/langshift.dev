import { createHash } from "node:crypto";
import { execFile } from "node:child_process";
import { mkdir, readFile, readdir, stat, writeFile } from "node:fs/promises";
import { dirname, join, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";

const execFileAsync = promisify(execFile);
const scriptPath = fileURLToPath(import.meta.url);
const defaultRoot = resolve(dirname(scriptPath), "..");
const httpMethods = new Set(["get", "post", "put", "patch", "delete"]);

const sha256 = (value) => createHash("sha256").update(value).digest("hex");

async function readJSON(path) {
  return JSON.parse(await readFile(path, "utf8"));
}

export function parseJSONSequence(content) {
  const values = [];
  let start = -1;
  let depth = 0;
  let inString = false;
  let escaped = false;
  for (let index = 0; index < content.length; index += 1) {
    const character = content[index];
    if (start === -1) {
      if (/\s/.test(character)) continue;
      if (character !== "{" && character !== "[") throw new Error(`unsupported JSON sequence token at offset ${index}`);
      start = index;
      depth = 1;
      continue;
    }
    if (inString) {
      if (escaped) escaped = false;
      else if (character === "\\") escaped = true;
      else if (character === '"') inString = false;
      continue;
    }
    if (character === '"') {
      inString = true;
      continue;
    }
    if (character === "{" || character === "[") depth += 1;
    else if (character === "}" || character === "]") depth -= 1;
    if (depth === 0) {
      values.push(JSON.parse(content.slice(start, index + 1)));
      start = -1;
    }
  }
  if (start !== -1 || inString || depth !== 0) throw new Error("unterminated JSON sequence");
  if (values.length === 0) throw new Error("empty JSON sequence");
  return values;
}

async function exists(path) {
  try {
    await stat(path);
    return true;
  } catch {
    return false;
  }
}

async function jsonFiles(directory) {
  if (!(await exists(directory))) return [];
  const output = [];
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    const path = join(directory, entry.name);
    if (entry.isDirectory()) output.push(...(await jsonFiles(path)));
    else if (entry.isFile() && entry.name.endsWith(".json")) output.push(path);
  }
  return output.sort();
}

function addOperation(map, operation) {
  const key = `${operation.method} ${operation.path} ${operation.operationId}`;
  const current = map.get(key);
  if (current) {
    current.sources.push(...operation.sources.filter((source) => !current.sources.includes(source)));
    return;
  }
  map.set(key, operation);
}

export async function collectOpenAPIOperations(root, baseline) {
  const operations = new Map();
  const basePath = resolve(root, baseline.openapiSources.base);
  const base = await readJSON(basePath);
  for (const [path, pathItem] of Object.entries(base.paths ?? {})) {
    for (const [method, operation] of Object.entries(pathItem ?? {})) {
      if (!httpMethods.has(method) || !operation?.operationId) continue;
      addOperation(operations, {
        method: method.toUpperCase(),
        path,
        operationId: operation.operationId,
        sources: [relative(root, basePath)]
      });
    }
  }

  const amendmentRoot = resolve(root, baseline.openapiSources.amendmentsDirectory);
  for (const amendmentPath of await jsonFiles(amendmentRoot)) {
    const amendment = await readJSON(amendmentPath);
    for (const [operationId, operation] of Object.entries(amendment.operations ?? {})) {
      if (!operation?.method || !operation?.path) continue;
      addOperation(operations, {
        method: operation.method.toUpperCase(),
        path: operation.path,
        operationId,
        sources: [relative(root, amendmentPath)]
      });
    }
  }
  return [...operations.values()].sort((left, right) =>
    `${left.path} ${left.method} ${left.operationId}`.localeCompare(`${right.path} ${right.method} ${right.operationId}`)
  );
}

export function resolveOperationOwner(path, rules) {
  return rules.filter((rule) => new RegExp(rule.pathPattern).test(path));
}

function firstValue(report, names) {
  for (const name of names) {
    if (report?.[name] !== undefined && report[name] !== null && report[name] !== "") return report[name];
  }
  for (const containerName of ["metadata", "acceptanceTarget", "releaseCandidate", "source"]) {
    const container = report?.[containerName];
    if (!container || typeof container !== "object") continue;
    for (const name of names) {
      if (container[name] !== undefined && container[name] !== null && container[name] !== "") return container[name];
    }
  }
  return null;
}

export function classifyEvidence(report, { currentCommit, currentWorktreeDirty, path = "" }) {
  const declaredResult = firstValue(report, ["status", "result", "gateStatus"]);
  const sourceCommit = firstValue(report, ["sourceCommit", "source_commit", "currentCommit", "commit"]);
  const reportDirty = Boolean(firstValue(report, ["worktreeDirty", "worktree_dirty", "dirty"]));
  const explicitFixture = report?.fixture === true || declaredResult === "fixture_validated" || /(?:^|\/)fixtures?(?:\/|$)/.test(path);

  if (explicitFixture) return { classification: "fixture", sourceCommit, declaredResult, acceptableAsPassed: false, reason: "fixture_only" };
  if (!sourceCommit) return { classification: "historical", sourceCommit: null, declaredResult, acceptableAsPassed: false, reason: "source_commit_missing" };
  if (sourceCommit !== currentCommit) return { classification: "historical", sourceCommit, declaredResult, acceptableAsPassed: false, reason: "source_commit_mismatch" };
  if (reportDirty || currentWorktreeDirty) return { classification: "current_dirty", sourceCommit, declaredResult, acceptableAsPassed: false, reason: "dirty_worktree" };
  const acceptableAsPassed = declaredResult === "passed" || declaredResult === "pass";
  return {
    classification: "current_commit",
    sourceCommit,
    declaredResult,
    acceptableAsPassed,
    reason: acceptableAsPassed ? "current_clean_pass" : "current_clean_non_pass"
  };
}

function validateStatus(errors, status, allowed, context) {
  if (!allowed.includes(status)) errors.push(`${context}: unsupported status ${JSON.stringify(status)}`);
}

export function validateDeferredMetadata(errors, override, allowedOwners, context) {
  if (override.status !== "deferred") return;
  if (typeof override.reason !== "string" || override.reason.trim().length === 0) errors.push(`${context}: deferred requires a reason`);
  if (!allowedOwners.includes(override.ownerCategory)) errors.push(`${context}: deferred requires an allowed ownerCategory`);
  if (typeof override.productionValidationEntry !== "string" || override.productionValidationEntry.trim().length === 0) errors.push(`${context}: deferred requires a productionValidationEntry`);
}

async function validateDeferredEntry(root, errors, override, context) {
  if (override.status !== "deferred" || typeof override.productionValidationEntry !== "string") return;
  const documentPath = override.productionValidationEntry.split("#", 1)[0];
  if (!documentPath || !(await exists(resolve(root, documentPath)))) errors.push(`${context}: deferred productionValidationEntry does not resolve to a repository file`);
}

async function validateDocuments(root, assertions, errors) {
  const results = [];
  for (const assertion of assertions) {
    const path = resolve(root, assertion.path);
    if (!(await exists(path))) {
      errors.push(`document assertion path does not exist: ${assertion.path}`);
      continue;
    }
    const content = await readFile(path, "utf8");
    const missing = (assertion.mustContain ?? []).filter((value) => !content.includes(value));
    const forbidden = (assertion.mustNotContain ?? []).filter((value) => content.includes(value));
    if (missing.length > 0) errors.push(`${assertion.path}: missing required text: ${missing.join(", ")}`);
    if (forbidden.length > 0) errors.push(`${assertion.path}: contains forbidden text: ${forbidden.join(", ")}`);
    results.push({ path: assertion.path, status: missing.length === 0 && forbidden.length === 0 ? "passed" : "failed", missing, forbidden });
  }
  return results;
}

async function classifyGateReports(root, currentCommit, currentWorktreeDirty) {
  const evidenceRoot = resolve(root, "gate-reports");
  const reports = [];
  for (const path of await jsonFiles(evidenceRoot)) {
    const reportPath = relative(root, path);
    try {
      const content = await readFile(path, "utf8");
      let report;
      let format = "json";
      try {
        report = JSON.parse(content);
      } catch {
        const sequence = parseJSONSequence(content);
        report = sequence.find((value) => firstValue(value, ["sourceCommit", "source_commit", "currentCommit", "commit"])) ?? {};
        format = "json_sequence";
      }
      reports.push({ path: reportPath, format, ...classifyEvidence(report, { currentCommit, currentWorktreeDirty, path: reportPath }) });
    } catch (error) {
      reports.push({ path: reportPath, classification: "invalid", acceptableAsPassed: false, reason: `invalid_json:${error.message}` });
    }
  }
  const counts = {};
  for (const report of reports) counts[report.classification] = (counts[report.classification] ?? 0) + 1;
  return { counts, reports };
}

export function parseGitStatus(statusOutput) {
  return statusOutput
    .split(/\r?\n/)
    .filter((line) => line.length > 0)
    .map((line) => line.slice(3));
}

async function gitState(root) {
  const [{ stdout: commitOutput }, { stdout: statusOutput }, { stdout: gitVersion }] = await Promise.all([
    execFileAsync("git", ["rev-parse", "HEAD"], { cwd: root }),
    execFileAsync("git", ["status", "--porcelain", "--untracked-files=all"], { cwd: root }),
    execFileAsync("git", ["--version"], { cwd: root })
  ]);
  return {
    sourceCommit: commitOutput.trim(),
    worktreeDirty: statusOutput.trim().length > 0,
    changedPaths: parseGitStatus(statusOutput),
    gitVersion: gitVersion.trim()
  };
}

export async function buildStage0Baseline({ root = defaultRoot, includeEvidence = true, requireEngineeringRC = false } = {}) {
  const errors = [];
  const baselinePath = resolve(root, "contracts/traceability/implementation-baseline.json");
  const baseline = await readJSON(baselinePath);
  const requirements = (await readJSON(resolve(root, "contracts/traceability/requirements.json"))).requirements;
  const capabilities = (await readJSON(resolve(root, "contracts/traceability/capability-inventory.json"))).capabilities;
  const operations = await collectOpenAPIOperations(root, baseline);
  const operationIds = new Set(operations.map((operation) => operation.operationId));
  const evidenceByOperation = new Map();

  for (const evidenceSet of baseline.operationEvidenceSets ?? []) {
    validateStatus(errors, evidenceSet.status, baseline.allowedImplementationStatuses, `operation evidence set ${evidenceSet.id}`);
    if (!evidenceSet.id || !Array.isArray(evidenceSet.operations) || evidenceSet.operations.length === 0 || !evidenceSet.reason) errors.push(`operation evidence set ${evidenceSet.id ?? "<missing>"}: id, operations and reason are required`);
    for (const path of [...(evidenceSet.handlerFiles ?? []), ...(evidenceSet.serviceFiles ?? []), ...(evidenceSet.testFiles ?? [])]) {
      if (!(await exists(resolve(root, path)))) errors.push(`operation evidence set ${evidenceSet.id}: source or test path does not exist: ${path}`);
    }
    for (const operationId of evidenceSet.operations ?? []) {
      if (!operationIds.has(operationId)) errors.push(`operation evidence set ${evidenceSet.id}: unknown OpenAPI operation ${operationId}`);
      if (evidenceByOperation.has(operationId)) errors.push(`operation ${operationId}: assigned to multiple evidence sets`);
      evidenceByOperation.set(operationId, evidenceSet);
    }
  }

  for (const rule of baseline.operationOwnerRules) {
    try {
      new RegExp(rule.pathPattern);
    } catch (error) {
      errors.push(`operation owner ${rule.id}: invalid pathPattern: ${error.message}`);
    }
    validateStatus(errors, rule.defaultStatus, baseline.allowedImplementationStatuses, `operation owner ${rule.id}`);
    for (const path of [...(rule.handlerFiles ?? []), ...(rule.gatewayFiles ?? [])]) {
      if (!(await exists(resolve(root, path)))) errors.push(`operation owner ${rule.id}: source path does not exist: ${path}`);
    }
  }

  const operationCoverage = [];
  for (const operation of operations) {
    const owners = resolveOperationOwner(operation.path, baseline.operationOwnerRules);
    if (owners.length !== 1) {
      errors.push(`${operation.method} ${operation.path} ${operation.operationId}: expected exactly one owner, found ${owners.map((owner) => owner.id).join(", ") || "none"}`);
      continue;
    }
    const owner = owners[0];
    const override = baseline.operationOverrides[operation.operationId] ?? {};
    const evidenceSet = evidenceByOperation.get(operation.operationId) ?? {};
    const effective = { ...evidenceSet, ...override };
    const status = effective.status ?? owner.defaultStatus;
    const plannedStage = effective.plannedStage ?? owner.plannedStage;
    validateStatus(errors, status, baseline.allowedImplementationStatuses, `operation ${operation.operationId}`);
    if (status === "missing" && !effective.reason) errors.push(`operation ${operation.operationId}: missing requires a reason`);
    validateDeferredMetadata(errors, { ...effective, status }, baseline.allowedDeferredOwnerCategories ?? [], `operation ${operation.operationId}`);
    await validateDeferredEntry(root, errors, { ...effective, status }, `operation ${operation.operationId}`);
    if (!Number.isInteger(plannedStage) || plannedStage < 0 || plannedStage > 7) errors.push(`operation ${operation.operationId}: invalid plannedStage ${plannedStage}`);
    operationCoverage.push({ ...operation, owner: owner.id, service: owner.service, handlerFiles: effective.handlerFiles ?? owner.handlerFiles, serviceFiles: effective.serviceFiles ?? [], testFiles: effective.testFiles ?? [], gatewayFiles: owner.gatewayFiles, evidenceSet: evidenceSet.id ?? null, status, plannedStage, reason: effective.reason ?? null, deferredOwnerCategory: effective.ownerCategory ?? null, productionValidationEntry: effective.productionValidationEntry ?? null });
  }
  for (const operationId of Object.keys(baseline.operationOverrides)) {
    if (!operationIds.has(operationId)) errors.push(`operation override does not match OpenAPI: ${operationId}`);
  }

  const requirementsById = new Map(requirements.map((requirement) => [requirement.requirementId, requirement]));
  const capabilityIds = new Set(capabilities.map((capability) => capability.capabilityId));
  const evidenceByCapability = new Map();
  const evidenceSetIds = new Set();
  for (const evidenceSet of baseline.capabilityEvidenceSets ?? []) {
    const context = `capability evidence set ${evidenceSet.id ?? "<missing>"}`;
    if (!evidenceSet.id || evidenceSetIds.has(evidenceSet.id)) errors.push(`${context}: id must be present and unique`);
    else evidenceSetIds.add(evidenceSet.id);
    validateStatus(errors, evidenceSet.status, baseline.allowedImplementationStatuses, context);
    if (!evidenceSet.reason || !Array.isArray(evidenceSet.requirements) && !Array.isArray(evidenceSet.capabilities)) errors.push(`${context}: reason plus requirements or capabilities are required`);
    if (evidenceSet.status === "implemented" && ((evidenceSet.sourceFiles?.length ?? 0) === 0 || (evidenceSet.testFiles?.length ?? 0) === 0)) errors.push(`${context}: implemented evidence requires sourceFiles and testFiles`);
    for (const path of [...(evidenceSet.sourceFiles ?? []), ...(evidenceSet.testFiles ?? [])]) {
      if (!(await exists(resolve(root, path)))) errors.push(`${context}: source or test path does not exist: ${path}`);
    }
    for (const requirementId of evidenceSet.requirements ?? []) {
      if (!requirementsById.has(requirementId)) errors.push(`${context}: unknown requirement ${requirementId}`);
    }
    const excluded = new Set(evidenceSet.excludeCapabilities ?? []);
    for (const capabilityId of excluded) {
      if (!capabilityIds.has(capabilityId)) errors.push(`${context}: unknown excluded capability ${capabilityId}`);
    }
    const selected = new Set(evidenceSet.capabilities ?? []);
    for (const capability of capabilities) {
      if ((evidenceSet.requirements ?? []).includes(capability.requirementId)) selected.add(capability.capabilityId);
    }
    for (const capabilityId of selected) {
      if (!capabilityIds.has(capabilityId)) {
        errors.push(`${context}: unknown capability ${capabilityId}`);
        continue;
      }
      if (excluded.has(capabilityId)) continue;
      if (evidenceByCapability.has(capabilityId)) errors.push(`capability ${capabilityId}: assigned to multiple evidence sets`);
      else evidenceByCapability.set(capabilityId, evidenceSet);
    }
  }
  const capabilityCoverage = [];
  for (const capability of capabilities) {
    const requirement = requirementsById.get(capability.requirementId);
    if (!requirement) {
      errors.push(`capability ${capability.capabilityId}: unknown requirement ${capability.requirementId}`);
      continue;
    }
    const override = baseline.capabilityOverrides[capability.capabilityId] ?? {};
    const evidenceSet = evidenceByCapability.get(capability.capabilityId) ?? {};
    const effective = { ...evidenceSet, ...override };
    const status = effective.status ?? baseline.capabilityDefaultStatus;
    const plannedStage = effective.plannedStage ?? baseline.capabilityStageByDomain[requirement.domain];
    validateStatus(errors, status, baseline.allowedImplementationStatuses, `capability ${capability.capabilityId}`);
    if (status === "missing" && !effective.reason) errors.push(`capability ${capability.capabilityId}: missing requires a reason`);
    validateDeferredMetadata(errors, { ...effective, status }, baseline.allowedDeferredOwnerCategories ?? [], `capability ${capability.capabilityId}`);
    await validateDeferredEntry(root, errors, { ...effective, status }, `capability ${capability.capabilityId}`);
    if (!Number.isInteger(plannedStage) || plannedStage < 0 || plannedStage > 7) errors.push(`capability ${capability.capabilityId}: invalid plannedStage ${plannedStage}`);
    for (const operationId of capability.api ?? []) {
      if (!operationIds.has(operationId)) errors.push(`capability ${capability.capabilityId}: unknown API operation ${operationId}`);
    }
    capabilityCoverage.push({ capabilityId: capability.capabilityId, requirementId: capability.requirementId, domain: requirement.domain, name: capability.name, status, plannedStage, reason: effective.reason ?? null, deferredOwnerCategory: effective.ownerCategory ?? null, productionValidationEntry: effective.productionValidationEntry ?? null, evidenceSet: evidenceSet.id ?? null, sourceFiles: effective.sourceFiles ?? [], testFiles: effective.testFiles ?? [], verificationCommands: effective.verificationCommands ?? [], ui: capability.ui, api: capability.api, contracts: capability.contracts, data: capability.data, authorization: capability.authorization, testIds: capability.testIds });
  }
  for (const capabilityId of Object.keys(baseline.capabilityOverrides)) {
    if (!capabilityIds.has(capabilityId)) errors.push(`capability override does not match inventory: ${capabilityId}`);
  }
  for (const domain of new Set(requirements.map((requirement) => requirement.domain))) {
    if (!Number.isInteger(baseline.capabilityStageByDomain[domain])) errors.push(`capability domain has no planned stage: ${domain}`);
  }

  const documents = await validateDocuments(root, baseline.documentAssertions, errors);
  const git = await gitState(root);
  const evidence = includeEvidence ? await classifyGateReports(root, git.sourceCommit, git.worktreeDirty) : { counts: {}, reports: [] };
  const operationCounts = Object.fromEntries(baseline.allowedImplementationStatuses.map((status) => [status, operationCoverage.filter((operation) => operation.status === status).length]));
  const capabilityCounts = Object.fromEntries(baseline.allowedImplementationStatuses.map((status) => [status, capabilityCoverage.filter((capability) => capability.status === status).length]));
  if (requireEngineeringRC) {
    const incompleteOperations = operationCoverage.filter((operation) => operation.status === "partial" || operation.status === "missing");
    const incompleteCapabilities = capabilityCoverage.filter((capability) => capability.status === "partial" || capability.status === "missing");
    if (incompleteOperations.length > 0) errors.push(`Engineering RC requires every OpenAPI operation to be implemented or deferred; found ${incompleteOperations.length} partial/missing operations`);
    if (incompleteCapabilities.length > 0) errors.push(`Engineering RC requires every capability to be implemented or deferred; found ${incompleteCapabilities.length} partial/missing capabilities`);
  }
  const sourceMaterial = JSON.stringify({ baseline, requirements, capabilities, operationCoverage, capabilityCoverage, documents });

  return {
    schemaVersion: "1.0.0",
    stage: 0,
    completionMode: requireEngineeringRC ? "engineering_rc" : "implementation_baseline",
    result: errors.length === 0 && !git.worktreeDirty ? "passed" : "failed",
    generatedAt: new Date().toISOString(),
    sourceCommit: git.sourceCommit,
    worktreeDirty: git.worktreeDirty,
    changedPaths: git.changedPaths,
    toolVersions: { node: process.version, git: git.gitVersion },
    sourceDigest: sha256(sourceMaterial),
    summary: {
      requirements: requirements.length,
      capabilities: capabilityCoverage.length,
      capabilityCounts,
      openapiOperations: operationCoverage.length,
      operationCounts,
      documentAssertions: documents.length,
      evidenceCounts: evidence.counts,
      errors: errors.length
    },
    errors,
    documents,
    operations: operationCoverage,
    capabilities: capabilityCoverage,
    evidence
  };
}

async function main() {
  const writeIndex = process.argv.indexOf("--write-report");
  const outputPath = writeIndex === -1 ? null : process.argv[writeIndex + 1] ?? "gate-reports/stage-0/implementation-baseline-report.json";
  const requireEngineeringRC = process.argv.includes("--require-engineering-rc");
  const report = await buildStage0Baseline({ requireEngineeringRC });
  if (outputPath) {
    const absoluteOutput = resolve(defaultRoot, outputPath);
    await mkdir(dirname(absoluteOutput), { recursive: true });
    await writeFile(absoluteOutput, `${JSON.stringify(report, null, 2)}\n`);
  }
  const summary = { result: report.result, sourceCommit: report.sourceCommit, worktreeDirty: report.worktreeDirty, summary: report.summary, errors: report.errors };
  process.stdout.write(`${JSON.stringify(summary, null, 2)}\n`);
  if (report.errors.length > 0 || (outputPath && report.result !== "passed")) process.exitCode = 1;
}

if (process.argv[1] && resolve(process.argv[1]) === scriptPath) {
  await main();
}
