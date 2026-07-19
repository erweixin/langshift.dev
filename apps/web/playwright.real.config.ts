import { defineConfig, devices } from "@playwright/test";

const apiURL = new URL(process.env.LITES_E2E_API_ORIGIN ?? "http://127.0.0.1:8080");
if (!["127.0.0.1", "localhost"].includes(apiURL.hostname) || apiURL.protocol !== "http:") {
  throw new Error("LITES_E2E_API_ORIGIN must be an HTTP loopback origin for the macOS engineering gate");
}
const apiOrigin = apiURL.origin;
const reuseProductStack = process.env.LITES_E2E_REUSE_PRODUCT_STACK === "true";

export default defineConfig({
  testDir: "./tests/real-e2e",
  fullyParallel: false,
  forbidOnly: true,
  retries: process.env.CI ? 1 : 0,
  workers: 1,
  reporter: [["list"], ["json", { outputFile: "../../.tmp/verification/macos-real-e2e.json" }]],
  use: {
    baseURL: "http://127.0.0.1:3118",
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
    video: "retain-on-failure",
    locale: "en-US",
  },
  projects: [
    { name: "desktop-chromium-real", use: { ...devices["Desktop Chrome"] } },
    { name: "mobile-chromium-real", use: { ...devices["Pixel 7"] } },
  ],
  webServer: reuseProductStack ? undefined : {
      command: `NEXT_PUBLIC_LITES_DEMO_MODE=false LITES_API_ORIGIN=${apiOrigin} ../../node_modules/.bin/next dev --webpack --hostname 127.0.0.1 --port 3118`,
      url: "http://127.0.0.1:3118/en/onboarding",
      reuseExistingServer: false,
      timeout: 120_000,
    },
});
