import { readFile, readdir } from "node:fs/promises";
import { resolve } from "node:path";
import { pathToFileURL } from "node:url";

const root = resolve(import.meta.dirname, "..");
const read = (path, base = root) => readFile(resolve(base, path), "utf8");
const supportedByokProviders = ["openai", "anthropic", "openai_compatible"];
const forbiddenPublicPath = /\/(?:checkout|payments?|payment-methods?|refunds?|coupons?|tax|mfa|sso|scim|developers?|developer-tokens?|api-keys?|agent-profiles?|agents?|tools?|marketplace)(?:\/|$)/i;
const forbiddenOperation = /(?:checkout|payment|refund|coupon|tax|mfa|sso|scim|marketplace|developer[_-]?tokens?|api[_-]?keys?|agent[_-]?profiles?|custom[_-]?agents?|tool[_-]?(?:registry|management|marketplace))/i;

async function readProductionWebSource(directory, base) {
  const entries = await readdir(resolve(base, directory), { withFileTypes: true });
  const sources = await Promise.all(entries.map(async (entry) => {
    const relative = `${directory}/${entry.name}`;
    if (entry.isDirectory()) return readProductionWebSource(relative, base);
    if (!/\.(?:ts|tsx)$/.test(entry.name) || /(?:\.test\.|\.d\.ts$)/.test(entry.name)) return "";
    return read(relative, base);
  }));
  return sources.join("\n");
}

export async function loadProductScopeFixture(base = root) {
  const [openapiSource, registry, registryTest, gateway, webPackage, webSource, goModule, plan, toolSystem] = await Promise.all([
    read("contracts/openapi/lites.current.openapi.json", base),
    read("internal/toolregistry/registry.go", base),
    read("internal/toolregistry/registry_test.go", base),
    read("internal/gateway/route_policy.go", base),
    read("apps/web/package.json", base),
    readProductionWebSource("apps/web/src", base),
    read("go.mod", base),
    read("docs/product-implementation-plan.md", base),
    read("docs/tool-system.md", base),
  ]);
  return { openapi: JSON.parse(openapiSource), registry, registryTest, gateway, webPackage, webSource, goModule, plan, toolSystem };
}

export function validateProductScope({ openapi, registry, registryTest, gateway, webPackage, webSource, goModule, plan, toolSystem }) {
  const failures = [];
  const assert = (condition, message) => { if (!condition) failures.push(message); };

  for (const path of Object.keys(openapi.paths ?? {})) {
    assert(!forbiddenPublicPath.test(path), `out-of-scope public API path: ${path}`);
  }
  for (const [path, item] of Object.entries(openapi.paths ?? {})) {
    for (const operation of Object.values(item)) {
      if (!operation || typeof operation !== "object" || !("operationId" in operation)) continue;
      const operationID = String(operation.operationId ?? "");
      assert(!forbiddenOperation.test(operationID), `out-of-scope operationId: ${path} ${operationID}`);
    }
  }
  const advertisedByokProviders = openapi.components?.schemas?.ByokCreateRequest?.properties?.provider_id?.enum ?? [];
  assert(
    JSON.stringify(advertisedByokProviders) === JSON.stringify(supportedByokProviders),
    `BYOK provider surface drifted: expected ${supportedByokProviders.join(", ")}; received ${advertisedByokProviders.join(", ") || "none"}`,
  );

  assert(/switch value\.Source \{\s*case "platform":\s*return true/s.test(registry), "tool registry is not locked to platform source");
  assert(!/case "platform",/.test(registry), "tool registry accepts additional runtime tool sources");
  assert(registryTest.includes('value.Source = "tenant_custom"') && registryTest.includes('value.Source = "marketplace"'), "tool registry lacks fail-closed source tests");
  assert(!forbiddenPublicPath.test(gateway), "gateway advertises an out-of-scope public route");
  assert(!/["'`]\/v1\/runs(?:\/|["'`?])/.test(webSource), "Web product code calls the internal Agent Run control surface directly");

  const dependencyText = `${webPackage}\n${goModule}`;
  for (const sdk of ["stripe", "braintree", "adyen", "paypal", "paddle"]) assert(!new RegExp(sdk, "i").test(dependencyText), `online-payment dependency present: ${sdk}`);

  for (const statement of ["职业迁移垂直 SaaS", "不建设开发者平台", "不发布开发者 token", "不增加任何在线支付 API"]) {
    assert(plan.includes(statement), `implementation plan is missing scope statement: ${statement}`);
  }
  assert(toolSystem.includes("`source` 只允许 `platform`"), "tool documentation does not state the internal-only source boundary");
  assert(toolSystem.includes("不提供 Tool Management 公共 API"), "tool documentation does not reject a public management plane");
  return failures;
}

async function main() {
  const fixture = await loadProductScopeFixture();
  const failures = validateProductScope(fixture);
  if (failures.length) {
    for (const failure of failures) console.error(failure);
    process.exitCode = 1;
    return;
  }
  console.log(`product scope: ${Object.keys(fixture.openapi.paths ?? {}).length} current public paths checked; internal platform tools only; no payment/PaaS/SSO/SCIM/MFA surface`);
}

if (process.argv[1] && pathToFileURL(resolve(process.argv[1])).href === import.meta.url) await main();
