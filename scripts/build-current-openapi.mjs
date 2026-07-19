import { readdir, readFile, writeFile } from "node:fs/promises";
import { resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const basePath = resolve(root, "contracts/openapi/lites.openapi.json");
const amendmentRoot = resolve(root, "contracts/openapi/amendments");
const outputPath = resolve(root, "contracts/openapi/lites.current.openapi.json");

const readJSON = async (path) => JSON.parse(await readFile(path, "utf8"));
const clone = (value) => structuredClone(value);
const semverParts = (value) => value.replace(/^v/, "").split(".").map(Number);
const compareSemver = (left, right) => {
  const a = semverParts(left);
  const b = semverParts(right);
  for (let index = 0; index < Math.max(a.length, b.length); index += 1) {
    const delta = (a[index] ?? 0) - (b[index] ?? 0);
    if (delta !== 0) return delta;
  }
  return 0;
};

const normalizePathShape = (path) => path.replace(/\{[^}]+\}/g, "{}");

function pathParameterMap(fromPath, toPath) {
  const mapping = new Map();
  const fromSegments = fromPath.split("/");
  const toSegments = toPath.split("/");
  for (let index = 0; index < fromSegments.length; index += 1) {
    const from = fromSegments[index]?.match(/^\{([^}]+)\}$/)?.[1];
    const to = toSegments[index]?.match(/^\{([^}]+)\}$/)?.[1];
    if (from && to && from !== to) mapping.set(from, to);
  }
  return mapping;
}

function remapPathParameters(pathItem, fromPath, toPath) {
  const mapping = pathParameterMap(fromPath, toPath);
  if (!mapping.size) return clone(pathItem);
  const remapped = clone(pathItem);
  const rename = (parameters) => {
    for (const parameter of parameters ?? []) {
      if (parameter?.in === "path" && mapping.has(parameter.name)) parameter.name = mapping.get(parameter.name);
    }
  };
  rename(remapped.parameters);
  for (const operation of Object.values(remapped)) {
    if (operation && typeof operation === "object" && !Array.isArray(operation)) rename(operation.parameters);
  }
  return remapped;
}

function useCanonicalPath(paths, sourcePath) {
  const equivalentPaths = Object.keys(paths).filter((path) => path !== sourcePath && normalizePathShape(path) === normalizePathShape(sourcePath));
  let canonical = clone(paths[sourcePath] ?? {});
  for (const path of equivalentPaths) {
    canonical = { ...remapPathParameters(paths[path], path, sourcePath), ...canonical };
    delete paths[path];
  }
  paths[sourcePath] = canonical;
}

function normalizeRefs(value) {
  if (Array.isArray(value)) return value.map(normalizeRefs);
  if (!value || typeof value !== "object") return value;
  return Object.fromEntries(Object.entries(value).map(([key, item]) => [
    key,
    key === "$ref" && typeof item === "string"
      ? item.startsWith("#/schemas/")
        ? item.replace("#/schemas/", "#/components/schemas/")
        : /^[A-Za-z][A-Za-z0-9_]*$/.test(item)
          ? `#/components/schemas/${item}`
          : item
      : normalizeRefs(item),
  ]));
}

function problemResponse(description) {
  return {
    description,
    content: { "application/problem+json": { schema: { $ref: "#/components/schemas/Problem" } } },
  };
}

function queryParameter(name, required) {
  const known = {
    mission_id: { type: "string", format: "uuid" },
    cursor: { type: "string", minLength: 1 },
  };
  return { name, in: "query", required, schema: known[name] ?? { type: "string", minLength: 1 } };
}

function headerParameter(name) {
  if (name === "If-Match") return { $ref: "#/components/parameters/IfMatch" };
  return { name, in: "header", required: true, schema: { type: "string", minLength: 1 } };
}

function currentOperation(operationID, source) {
  const method = source.method.toLowerCase();
  const write = method !== "get";
  const parameters = [];
  for (const match of source.path.matchAll(/\{([^}]+)\}/g)) {
    parameters.push({ name: match[1], in: "path", required: true, schema: { type: "string", format: "uuid" } });
  }
  for (const parameter of source.parameters ?? []) parameters.push(normalizeRefs(parameter));
  for (const name of source.requiredQuery ?? []) parameters.push(queryParameter(name, true));
  for (const name of source.optionalQuery ?? []) parameters.push(queryParameter(name, false));
  for (const name of source.requiredHeaders ?? []) parameters.push(headerParameter(name));

  const requestContentType = source.requestContentType ?? (source.requestSchema ? "application/json" : null);
  const response = source.response ?? null;
  const responseStatus = String(source.status ?? response?.status ?? (write && source.runSemantics ? 202 : write ? 200 : 200));
  const responseContentType = source.responseContentType ?? response?.contentType ?? "application/json";
  const responseSchema = source.responseSchema ?? response?.schema ?? null;
  let successContent;
  if (responseContentType === "text/event-stream") {
    successContent = { "text/event-stream": { schema: { type: "string", description: "Server-sent event stream; EventStore remains the durable fact source." } } };
  } else if (responseSchema) {
    successContent = { [responseContentType]: { schema: { $ref: `#/components/schemas/${responseSchema}` } } };
  } else {
    successContent = { [responseContentType]: { schema: { type: "object", additionalProperties: true } } };
  }

  const operation = {
    operationId: operationID,
    summary: operationID.replaceAll(".", " "),
    "x-contract-version": "current-engineering",
    "x-authentication": source.authentication ?? "gateway_signed_session",
    "x-authorization": source.authorization ?? "resource_policy",
    "x-idempotency": source.idempotency ?? (write ? "required" : "not_applicable"),
    "x-concurrency": source.concurrency ?? "read_snapshot",
    "x-audit": source.audit ?? (write ? "mutation" : "access"),
    parameters,
    responses: {
      [responseStatus]: { description: "Successful operation", content: successContent },
      "400": problemResponse("Invalid request"),
      "401": problemResponse("Authentication or recent reauthentication required"),
      "403": problemResponse("Permission denied"),
      "404": problemResponse("Resource not found in the authorized scope"),
      "409": problemResponse("Idempotency, state, or version conflict"),
      "429": problemResponse("Rate or quota limit"),
    },
    security: source.authentication === "public" ? [] : [{ sessionCookie: [] }],
  };
  if (requestContentType && source.requestSchema) {
    operation.requestBody = {
      required: true,
      content: { [requestContentType]: { schema: { $ref: `#/components/schemas/${source.requestSchema}` } } },
    };
  }
  return operation;
}

const base = await readJSON(basePath);
const current = clone(base);
current.info = {
  ...current.info,
  title: "Lites Current Engineering API",
  version: "1.29.0-engineering",
  description: "Generated current Engineering RC contract: base contract with every ordered additive amendment applied. This is the authoritative source for application client generation; it is not a public developer-platform promise.",
};
current["x-generated-from"] = ["contracts/openapi/lites.openapi.json"];

const directories = (await readdir(amendmentRoot, { withFileTypes: true }))
  .filter((entry) => entry.isDirectory())
  .map((entry) => entry.name)
  .sort(compareSemver);
let expectedBase = null;
let amendmentCount = 0;
let operationCount = 0;
for (const directory of directories) {
  const files = (await readdir(resolve(amendmentRoot, directory), { withFileTypes: true }))
    .filter((entry) => entry.isFile() && entry.name.endsWith(".json"))
    .map((entry) => entry.name)
    .sort();
  for (const file of files) {
    const relativePath = `contracts/openapi/amendments/${directory}/${file}`;
    const amendment = await readJSON(resolve(amendmentRoot, directory, file));
    const minimumBase = expectedBase ?? base.info.version;
    if (!amendment.amendmentVersion || !amendment.baseContractVersion || compareSemver(amendment.baseContractVersion, minimumBase) < 0 || compareSemver(amendment.amendmentVersion, amendment.baseContractVersion) <= 0) {
      throw new Error(`${relativePath}: expected a monotonic base at or after ${minimumBase}, received ${amendment.baseContractVersion ?? "missing"}`);
    }
    for (const [name, schema] of Object.entries(amendment.schemas ?? {})) {
      current.components.schemas[name] = normalizeRefs(schema);
    }
    for (const [operationID, source] of Object.entries(amendment.operations ?? {})) {
      if (!source.method || !source.path) throw new Error(`${relativePath}: ${operationID} is missing method/path`);
      useCanonicalPath(current.paths, source.path);
      current.paths[source.path][source.method.toLowerCase()] = currentOperation(operationID, source);
      operationCount += 1;
    }
    current["x-generated-from"].push(relativePath);
    expectedBase = amendment.amendmentVersion;
    amendmentCount += 1;
  }
}
current.info.version = `${expectedBase}-engineering`;
current["x-amendment-count"] = amendmentCount;
current["x-current-operation-overlays"] = operationCount;

await writeFile(outputPath, `${JSON.stringify(current, null, 2)}\n`);
console.log(`current OpenAPI: ${Object.keys(current.paths).length} paths, ${operationCount} ordered operation overlays, version ${current.info.version}`);
