import { readdir, readFile } from "node:fs/promises";
import { join, resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const readJSON = async (path) => JSON.parse(await readFile(resolve(root, path), "utf8"));

async function filesBelow(directory, suffix) {
  const output = [];
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    const path = join(directory, entry.name);
    if (entry.isDirectory()) output.push(...await filesBelow(path, suffix));
    else if (entry.isFile() && entry.name.endsWith(suffix)) output.push(path);
  }
  return output.sort();
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

function pathMatches(template, concrete) {
  const expected = template.split("/");
  const actual = concrete.split("/");
  return expected.length === actual.length && expected.every((part, index) => /^\{[^}]+\}$/.test(part) || part === actual[index]);
}

const normalizePathShape = (path) => path.replace(/\{[^}]+\}/g, "{}");

function normalizeClientPath(value) {
  return value
    .replace(/\$\{[^}]+\}/g, "{value}")
    .split("?")[0]
    .replace(/\/$/, "");
}

const current = await readJSON("contracts/openapi/lites.current.openapi.json");
const failures = [];
const check = (condition, message) => { if (!condition) failures.push(message); };
const sourcePaths = new Set(current["x-generated-from"] ?? []);
const amendmentFiles = await filesBelow(resolve(root, "contracts/openapi/amendments"), ".json");
let declaredOperations = 0;
const latestSchemas = new Map();
for (const absolute of amendmentFiles) {
  const relative = absolute.slice(root.length + 1);
  const amendment = JSON.parse(await readFile(absolute, "utf8"));
  check(sourcePaths.has(relative), `current OpenAPI provenance is missing ${relative}`);
  for (const [name, schema] of Object.entries(amendment.schemas ?? {})) {
    latestSchemas.set(name, { relative, schema: normalizeRefs(schema) });
  }
  for (const [operationID, operation] of Object.entries(amendment.operations ?? {})) {
    declaredOperations += 1;
    const actual = current.paths?.[operation.path]?.[operation.method?.toLowerCase()];
    check(actual?.operationId === operationID, `${relative}: current operation missing ${operation.method} ${operation.path} ${operationID}`);
    if (operation.requestSchema) {
      const media = operation.requestContentType ?? "application/json";
      check(actual?.requestBody?.content?.[media]?.schema?.$ref === `#/components/schemas/${operation.requestSchema}`, `${operationID}: request schema/media drifted`);
    }
    const response = operation.response ?? {};
    const responseSchema = operation.responseSchema ?? response.schema;
    const responseMedia = operation.responseContentType ?? response.contentType;
    const responseStatus = String(operation.status ?? response.status ?? (operation.method === "GET" ? 200 : 200));
    if (responseSchema && responseMedia) {
      check(actual?.responses?.[responseStatus]?.content?.[responseMedia]?.schema?.$ref === `#/components/schemas/${responseSchema}`, `${operationID}: response schema/media drifted`);
    }
  }
}
for (const [name, { relative, schema }] of latestSchemas) {
  check(JSON.stringify(current.components?.schemas?.[name]) === JSON.stringify(schema), `${relative}: current schema drifted for ${name}`);
}
check(current["x-amendment-count"] === amendmentFiles.length, `amendment count drifted: ${current["x-amendment-count"]} != ${amendmentFiles.length}`);
check(current["x-current-operation-overlays"] === declaredOperations, `operation overlay count drifted: ${current["x-current-operation-overlays"]} != ${declaredOperations}`);

const refs = [];
const collectRefs = (value) => {
  if (Array.isArray(value)) value.forEach(collectRefs);
  else if (value && typeof value === "object") for (const [key, item] of Object.entries(value)) key === "$ref" ? refs.push(item) : collectRefs(item);
};
collectRefs(current);
for (const ref of refs) {
  if (!ref.startsWith("#/")) continue;
  let value = current;
  for (const token of ref.slice(2).split("/")) value = value?.[token.replaceAll("~1", "/").replaceAll("~0", "~")];
  check(value !== undefined, `unresolved current OpenAPI reference: ${ref}`);
}

const clientFiles = await filesBelow(resolve(root, "apps/web/src"), ".ts");
clientFiles.push(...await filesBelow(resolve(root, "apps/web/src"), ".tsx"));
const clientPaths = new Set();
for (const file of [...new Set(clientFiles)]) {
  if (/\.(?:test|spec)\.(?:ts|tsx)$/.test(file)) continue;
  const source = await readFile(file, "utf8");
  for (const match of source.matchAll(/["'`](\/v1\/[^"'`\s]*)/g)) {
    const path = normalizeClientPath(match[1]);
    if (path !== "/v1" && path && !path.includes("+") && !path.includes("${")) clientPaths.add(path);
  }
}
const contractPaths = Object.keys(current.paths ?? {});
const pathShapes = new Map();
for (const path of contractPaths) {
  const shape = normalizePathShape(path);
  const existing = pathShapes.get(shape);
  check(!existing, `equivalent OpenAPI templates must use one canonical path: ${existing} and ${path}`);
  pathShapes.set(shape, path);
}
for (const path of clientPaths) {
  const querySuffixBase = path.endsWith("{value}") ? path.slice(0, -"{value}".length) : null;
  check(contractPaths.some((template) => pathMatches(template, path)) || querySuffixBase && contractPaths.includes(querySuffixBase), `Web client path has no current OpenAPI contract: ${path}`);
}

if (failures.length) {
  for (const failure of failures) console.error(failure);
  process.exit(1);
}
console.log(`current OpenAPI: ${amendmentFiles.length} ordered amendments, ${declaredOperations} overlays, ${clientPaths.size} Web client paths and ${refs.length} references validated`);
