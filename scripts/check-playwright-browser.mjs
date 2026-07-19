import { chromium } from "@playwright/test";

let browser;
try {
  browser = await chromium.launch({ headless: true });
  console.log(`Playwright Chromium ready: ${browser.version()}`);
} catch (error) {
  console.error(error instanceof Error ? error.message : String(error));
  process.exitCode = 1;
} finally {
  await browser?.close();
}
