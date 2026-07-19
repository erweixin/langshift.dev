import assert from "node:assert/strict";
import { loadProductScopeFixture, validateProductScope } from "./validate-product-scope.mjs";

const fixture = await loadProductScopeFixture();
assert.deepEqual(validateProductScope(fixture), []);

const cases = [
  ["online payment path", (copy) => { copy.openapi.paths["/v1/payments"] = { post: { operationId: "commerce.create" } }; }, "out-of-scope public API path"],
  ["hidden checkout operation", (copy) => { copy.openapi.paths["/v1/commerce"] = { post: { operationId: "checkout.create" } }; }, "out-of-scope operationId"],
  ["developer token path", (copy) => { copy.openapi.paths["/v1/developer-tokens"] = { post: { operationId: "credentials.create" } }; }, "out-of-scope public API path"],
  ["custom agent operation", (copy) => { copy.openapi.paths["/v1/automation"] = { post: { operationId: "custom_agents.create" } }; }, "out-of-scope operationId"],
  ["unsupported BYOK provider", (copy) => { copy.openapi.components.schemas.ByokCreateRequest.properties.provider_id.enum.push("arbitrary_http"); }, "BYOK provider surface drifted"],
  ["tenant tool source", (copy) => { copy.registry = copy.registry.replace('case "platform":', 'case "platform", "tenant_custom":'); }, "tool registry"],
  ["payment dependency", (copy) => { copy.webPackage += " stripe "; }, "online-payment dependency present"],
  ["browser Agent Run control", (copy) => { copy.webSource += '\napiRequest("/v1/runs/run-id")'; }, "internal Agent Run control surface"],
  ["scope statement removed", (copy) => { copy.plan = copy.plan.replace("不增加任何在线支付 API", ""); }, "implementation plan is missing scope statement"],
];

for (const [name, mutate, expected] of cases) {
  const copy = structuredClone(fixture);
  mutate(copy);
  assert(validateProductScope(copy).some((failure) => failure.includes(expected)), `${name} was not rejected`);
}

console.log(`Product scope self-test passed: current OpenAPI accepted and ${cases.length} out-of-scope mutations rejected`);
