import { defineConfig, devices } from "@playwright/test";

export default defineConfig({
  testDir: "./tests/e2e",
  timeout: 90_000,
  fullyParallel: true,
  forbidOnly: Boolean(process.env.CI),
  retries: process.env.CI ? 2 : 0,
  workers: process.env.CI ? 2 : undefined,
  reporter: [["list"], ["html", { open: "never" }], ["json", { outputFile: "../../.tmp/stage4-web-e2e.json" }]],
  use: {
    baseURL: "http://127.0.0.1:3117",
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
    video: "retain-on-failure",
    locale: "en-US",
  },
  projects: [
    { name: "desktop-chromium", use: { ...devices["Desktop Chrome"] } },
    { name: "mobile-chromium", use: { ...devices["Pixel 7"] } },
  ],
  webServer: {
    command: "NEXT_PUBLIC_LITES_DEMO_MODE=true ../../node_modules/.bin/next dev --webpack --hostname 127.0.0.1 --port 3117",
    url: "http://127.0.0.1:3117/en/onboarding",
    reuseExistingServer: false,
    timeout: 120_000,
  },
});
