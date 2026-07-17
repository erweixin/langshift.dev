import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";

test("quick onboarding produces a correctable route and opens Today", async ({ page }) => {
  await page.goto("/en/onboarding");
  await expect(page.getByRole("heading", { name: /You are not starting over/ })).toBeVisible();
  await page.getByTestId("quick-onboarding").click();
  await page.getByRole("button", { name: "Continue" }).click();
  await page.getByRole("button", { name: "Continue" }).click();
  await page.getByRole("button", { name: "Build route" }).click();
  await expect(page.getByRole("heading", { name: /Frontend engineering/ })).toBeVisible();
  await page.getByRole("link", { name: "Review my route" }).click();
  await expect(page).toHaveURL(/\/en\/route/);
  await expect(page.getByText("Confirm or correct")).toBeVisible();
  await page.getByRole("link", { name: "Save route and continue" }).click();
  await page.getByRole("link", { name: "Create and open Today" }).click();
  await expect(page).toHaveURL("/en/today");
  await expect(page.getByRole("heading", { name: "Good morning." })).toBeVisible();
});

test("natural-language onboarding stays editable until Coach structures it", async ({ page }) => {
  await page.goto("/en/onboarding");
  await page.getByTestId("natural-onboarding").click();
  const story = page.getByLabel("Your story");
  await story.fill("I build frontend systems and want to learn how durable agents recover from crashes.");
  await expect(page.getByRole("button", { name: /Let Coach structure it/ })).toBeVisible();
  await page.getByRole("button", { name: /Let Coach structure it/ }).click();
  await expect(page.getByText("Route draft ready")).toBeVisible();
});

test("task draft survives reload and offline state never claims submission", async ({ page, context }) => {
  await page.goto("/en/task");
  await page.getByTestId("task-content").fill("scheduled -> running -> completed; reject all backward transitions");
  await page.getByLabel("Your understanding").fill("Completed is terminal, so moving back would invalidate durable facts and replay effects.");
  await page.waitForTimeout(500);
  await page.reload();
  await expect(page.getByTestId("task-content")).toHaveValue(/scheduled -> running/);
  await expect(page.getByText("Restored an unsubmitted draft from this device.")).toBeVisible();
  await context.setOffline(true);
  await expect(page.getByTestId("network-banner")).toContainText("nothing is marked submitted");
  await page.getByRole("button", { name: "Submit for review" }).click();
  await expect(page.locator(".submit-error")).toContainText("not submitted");
  await expect(page.getByText("Review complete")).toHaveCount(0);
  await context.setOffline(false);
});

test("an online task moves from confirmed submission to review and evidence", async ({ page }) => {
  await page.goto("/en/task");
  await page.getByTestId("task-content").fill("scheduled -> running -> completed; terminal states reject backward transitions");
  await page.getByLabel("Your understanding").fill("The durable record is authoritative across retries, so completed cannot become running again.");
  await page.getByRole("button", { name: "Submit for review" }).click();
  await expect(page.getByText("Review complete")).toBeVisible();
  await expect(page.getByText("Level: Demonstrated.")).toBeVisible();
});

test("core workspace has no serious accessibility violations or capability percentages", async ({ page }) => {
  await page.goto("/en/today");
  const body = await page.locator("body").innerText();
  expect(body).not.toMatch(/\b\d{1,3}%/);
  const results = await new AxeBuilder({ page }).disableRules(["color-contrast"]).analyze();
  expect(results.violations.filter((violation) => ["serious", "critical"].includes(violation.impact ?? ""))).toEqual([]);
  await page.getByTestId("coach-open").click();
  await expect(page.getByRole("dialog", { name: "Coach" })).toBeVisible();
  await page.getByRole("button", { name: "Why does this step matter?" }).click();
  await expect(page.getByText(/grounded in your current Mission/)).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(page.getByRole("dialog", { name: "Coach" })).toHaveCount(0);
});

for (const kind of ["Code", "Writing", "Design"] as const) {
  test(`complete ${kind.toLowerCase()} Create lifecycle preserves evidence and revision provenance`, async ({ page }) => {
    await page.goto("/en/create");
    await page.getByRole("button", { name: new RegExp(`^${kind}`) }).click();
    await page.getByLabel("Project brief").fill(`Build a production ${kind.toLowerCase()} artifact for a real operating need.`);
    await page.getByRole("button", { name: "Generate project plan" }).click();
    await expect(page.getByText("Milestone 1 · Required")).toBeVisible();
    await expect(page.getByText("Milestone 2 · Required")).toBeVisible();
    await page.getByRole("button", { name: "Create project, Workspace & milestones" }).click();
    await page.getByTestId("workspace-output").fill("Exact revision includes the work, observable acceptance result, and failure boundary.");
    await page.getByRole("button", { name: "Submit exact revision for independent evaluation" }).click();
    await expect(page.getByRole("heading", { name: "Evidence is bound to the exact revision" })).toBeVisible();
    await page.getByLabel("What changed in response to the review?").fill("Revised the failure boundary and attached the independently observed result.");
    await page.getByRole("button", { name: "Create and scan Artifact revision" }).click();
    await page.getByLabel("Project reflection").fill("The invariant held under evaluation; next time I would test the irreversible effect boundary earlier.");
    await page.getByRole("button", { name: "Complete project" }).click();
    await expect(page.getByText("Project complete")).toBeVisible();
    await expect(page.getByText("Evidence · Applied")).toBeVisible();
    await page.getByRole("button", { name: "Build exact-revision portfolio export" }).click();
    await expect(page.getByRole("button", { name: "Download verified project package" })).toBeVisible();
  });
}

test("Settings covers locale, timezone, reminders, BYOK, Memory, export, and erasure", async ({ page }) => {
  await page.goto("/en/settings");
  await page.getByLabel("Daily reminder time").fill("10:30");
  await page.getByLabel("Timezone").fill("Asia/Shanghai");
  await page.getByLabel("Coach style").selectOption("direct");
  await page.getByRole("button", { name: "Save settings" }).click();
  await expect(page.getByRole("button", { name: "Saved" })).toBeVisible();

  await page.getByLabel("Provider").selectOption("openai");
  await page.getByLabel("API key").fill("sk-demo-secret-1234");
  await page.getByRole("button", { name: "Verify and save credential" }).click();
  await expect(page.getByText("openai · ••••1234")).toBeVisible();
  await expect(page.getByLabel("API key")).toHaveValue("");

  await page.getByRole("button", { name: "Save Memory policy" }).click();
  await expect(page.getByRole("button", { name: "Policy saved" })).toBeVisible();
  await page.getByRole("button", { name: "Request export" }).click();
  await expect(page.getByRole("button", { name: "Requested" })).toBeVisible();

  await page.getByLabel("Confirmation phrase").fill("DELETE MY ACCOUNT");
  await page.getByRole("button", { name: "Request erasure" }).click();
  await expect(page.getByRole("button", { name: "Request created" })).toBeVisible();
  await page.getByRole("link", { name: "简体中文" }).click();
  await expect(page).toHaveURL("/zh-CN/settings");
  await expect(page.getByRole("heading", { name: "你的工作区，由你掌控。" })).toBeVisible();
});

test("PWA manifest and offline fallback are installable assets", async ({ page, request }) => {
  await page.goto("/en/today");
  await expect(page.locator('link[rel="manifest"]')).toHaveAttribute("href", "/manifest.webmanifest");
  const manifest = await request.get("/manifest.webmanifest");
  expect(manifest.ok()).toBeTruthy();
  expect((await manifest.json()).display).toBe("standalone");
  const offline = await request.get("/offline.html");
  expect(offline.ok()).toBeTruthy();
});

test("Chinese workspace and mobile navigation are first-class", async ({ page }, testInfo) => {
  test.skip(!testInfo.project.name.includes("mobile"), "mobile-only assertion");
  await page.goto("/zh-CN/today");
  await expect(page.getByRole("heading", { name: "早上好。" })).toBeVisible();
  await expect(page.getByRole("navigation", { name: "Primary navigation" })).toBeVisible();
  await page.getByRole("link", { name: "成长记录" }).click();
  await expect(page.getByRole("heading", { name: /每个判断/ })).toBeVisible();
});
