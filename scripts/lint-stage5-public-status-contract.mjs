import { createHash } from "node:crypto";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const readJSON = async (path) => JSON.parse(await readFile(resolve(root, path), "utf8"));
const readText = async (path) => readFile(resolve(root, path), "utf8");
const hash = (value) => createHash("sha256").update(JSON.stringify(value)).digest("hex");
const api = await readJSON("contracts/openapi/amendments/v1.27.0/public-status.json");
const reader = await readText("internal/statuspage/reader.go");
const handler = await readText("internal/product/api/public_status_handler.go");
const publisher = await readText("cmd/lites-status-sign/main.go");
const gatewayPolicy = await readText("internal/gateway/route_policy.go");
const gatewayRouting = await readText("cmd/api-gateway/main.go");
const helm = await readText("deploy/helm/lites/values.yaml");
const frontend = await readText("apps/web/src/components/status-page.tsx");
const tests = await readText("internal/statuspage/reader_test.go");
const results = [];
const check = (id, passed, details) => results.push({ id, status: passed ? "passed" : "failed", details });
const operation = api.operations["public.status.get.v1"];

check("OPENAPI-CHAIN", api.amendmentVersion === "1.27.0" && api.baseContractVersion === "1.26.0" && api.compatibility === "versioned-client-additive", "public status additively extends support cases");
check("EXACT-PUBLIC-ROUTE", operation.method === "GET" && operation.path === "/v1/public/status" && operation.authentication === "gateway_signed_public_request" && gatewayPolicy.includes('request.Method == http.MethodGet && request.URL.Path == "/v1/public/status"') && gatewayRouting.includes('path == "/v1/public/status"'), "only the exact GET route receives a signed public principal");
check("CLOSED-REPRESENTATIONS", ["PublicStatusComponentV1", "PublicStatusIncidentV1", "PublicStatusV1"].every((name) => api.schemas[name].additionalProperties === false), "status representations reject ambiguous fields");
check("PURPOSE-SIGNED", reader.includes("ed25519.Verify") && reader.includes("LoadPublicKeyring") && publisher.includes("statuspage.Sign"), "publisher private key and service public keyring are purpose separated");
check("KEY-WINDOW", reader.includes("GeneratedAt.Before(window.NotBefore)") && reader.includes("GeneratedAt.Before(window.NotAfter)") && reader.includes("ValidUntil.After(window.NotAfter)"), "the complete snapshot validity must fall inside the signing key issuance window");
check("SHORT-LIVED", api.integrity.includes("30-second to 5-minute") && reader.includes("5*time.Minute") && reader.includes("30*time.Second"), "publisher and reader bound snapshot validity");
check("WORST-STATE", reader.includes("stateRank(component.State) > stateRank(worst)") && reader.includes("worst != document.Overall"), "overall cannot be greener than any component");
check("ACTIVE-INCIDENT", reader.includes('incident.State != "resolved" && document.Overall == Operational'), "an active incident cannot coexist with a false-green overall");
check("FAIL-CLOSED-API", operation.failureMode === "503_status_snapshot_unavailable" && handler.includes("http.StatusServiceUnavailable"), "unavailable or rejected evidence returns 503");
check("FAIL-CLOSED-CLIENT", frontend.includes('setState("unknown")') && frontend.includes("Date.parse(result.valid_until) <= Date.now()"), "client renders missing or stale evidence as unknown");
check("BOUNDED-CACHE", operation.cacheControl === "public, max-age=15, must-revalidate" && handler.includes('public, max-age=15, must-revalidate'), "cache lifetime is materially shorter than snapshot validity");
check("ATOMIC-PUBLISH", publisher.includes("os.CreateTemp") && publisher.includes("temporary.Sync()") && publisher.includes("os.Rename"), "publisher never exposes a partially written snapshot");
check("SECRET-REFRESH", helm.includes("PUBLIC_STATUS_DOCUMENT_FILE") && helm.includes("PUBLIC_STATUS_KEYRING_FILE"), "Helm requires both externally refreshed status artifacts");
check("ADVERSARIAL-TESTS", tests.includes("TamperingExpiryAndInconsistentOverall") && tests.includes("tampered snapshot accepted") && tests.includes("expired snapshot accepted"), "tests cover tamper, expiry, and false-green aggregation");

const failures = results.filter((result) => result.status === "failed");
const base = { reportVersion: "1.0.0", stage: 5, kind: "public-status-control-plane-contract", generatedAt: new Date().toISOString(), status: failures.length ? "failed" : "passed", summary: { checks: results.length, passed: results.length - failures.length, failed: failures.length }, openAPIHash: hash(api), results };
const report = { ...base, reportHash: hash(base) };
const target = resolve(root, process.env.LITES_GATE_REPORT_ROOT ?? "gate-reports", "stage-5/public-status-control-plane-contract.json");
await mkdir(dirname(target), { recursive: true });
await writeFile(target, `${JSON.stringify(report, null, 2)}\n`);
console.log(`${report.status}: ${report.summary.passed}/${report.summary.checks} checks; report ${report.reportHash}`);
if (failures.length) {
  for (const failure of failures) console.error(`${failure.id}: ${failure.details}`);
  process.exitCode = 1;
}
