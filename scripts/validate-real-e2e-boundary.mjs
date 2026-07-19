import { readFile } from "node:fs/promises";
import { resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const read = (path) => readFile(resolve(root, path), "utf8");
const [config, journey, webPackageSource, rootPackageSource] = await Promise.all([
  read("apps/web/playwright.real.config.ts"),
  read("apps/web/tests/real-e2e/career-migration.spec.ts"),
  read("apps/web/package.json"),
  read("package.json"),
]);
const webPackage = JSON.parse(webPackageSource);
const rootPackage = JSON.parse(rootPackageSource);
const failures = [];
const assert = (condition, message) => { if (!condition) failures.push(message); };

assert(config.includes('testDir: "./tests/real-e2e"'), "real E2E does not use the isolated real-e2e directory");
assert(config.includes("NEXT_PUBLIC_LITES_DEMO_MODE=false"), "real E2E does not force Demo mode off");
assert(!config.includes("NEXT_PUBLIC_LITES_DEMO_MODE=true"), "real E2E can enable Demo mode");
assert(config.includes("reuseExistingServer: false"), "real E2E may reuse an untrusted existing web server");
assert(config.includes("desktop-chromium-real") && config.includes("mobile-chromium-real"), "real E2E does not cover desktop and mobile viewports");
assert(config.includes("must be an HTTP loopback origin"), "real E2E API origin is not constrained to the macOS local stack");

for (const forbidden of ["page.route(", "context.route(", "route.fulfill(", "NEXT_PUBLIC_LITES_DEMO_MODE", "demoMode"]) {
  assert(!journey.includes(forbidden), `real E2E contains forbidden mock/demo construct: ${forbidden}`);
}
for (const required of [
  "/en/onboarding",
  "Submit story and build route",
  "Register and persist route",
  "waitForVerificationURL",
  "Save correction",
  "Accept this route revision",
  "Durable claim available",
  "Sign out",
  "Start task",
  "coach-open",
  "current evidence",
  "/api/v1/admin/usage",
  "permission_denied",
  "Submit for review",
  "/en/evidence",
  "page.reload()",
  'page.keyboard.press("Enter")',
  "AxeBuilder",
]) assert(journey.includes(required), `real E2E is missing journey assertion: ${required}`);

assert(webPackage.scripts?.["test:e2e"] === "playwright test --config playwright.real.config.ts", "web test:e2e is not bound to the real config");
assert(webPackage.scripts?.["test:e2e:fixture"] === "playwright test --config playwright.config.ts", "fixture E2E is not explicitly separated");
assert(rootPackage.scripts?.["web:test:e2e"]?.includes("test:e2e --workspace"), "root real E2E command is not wired");

if (failures.length) {
  for (const failure of failures) console.error(failure);
  process.exit(1);
}
console.log("real E2E boundary: no Demo mode or request mocks; desktop/mobile durable journey and accessibility assertions present");
