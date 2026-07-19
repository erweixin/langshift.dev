import AxeBuilder from "@axe-core/playwright";
import { expect, test, type APIRequestContext } from "@playwright/test";

const mailAPI = process.env.LITES_E2E_MAIL_API ?? "http://127.0.0.1:8025/api/v1";

test("real career migration trunk persists from anonymous intake through reviewed evidence", async ({ page, request }) => {
  test.setTimeout(8 * 60_000);
  const email = `lites-e2e-${Date.now()}@example.test`;
  const password = `Lites-e2e-${crypto.randomUUID()}-safe`;
  const story = "I led frontend platform migrations, coordinated incident recovery, and now want to build reliable AI applications.";

  await page.goto("/en/onboarding");
  await page.getByTestId("natural-onboarding").focus();
  await page.keyboard.press("Enter");
  await expect(page.getByLabel("Current role anchor")).toBeEnabled();
  await page.getByLabel("Your story").fill(story);
  await page.getByRole("button", { name: "Submit story and build route" }).click();
  await expect(page).toHaveURL(/\/en\/route\?/);
  await expect(page.getByText("Confirm or correct after registration")).toBeVisible({ timeout: 180_000 });
  await page.reload();
  await expect(page.getByText("Confirm or correct after registration")).toBeVisible({ timeout: 180_000 });
  await page.getByRole("link", { name: "Register and persist route" }).click();

  await page.getByLabel("Email").fill(email);
  await page.getByLabel("Password").fill(password);
  await page.getByLabel(/I agree/).check();
  await page.getByRole("button", { name: "Create and send verification" }).click();
  await expect(page.getByText("Verification sent")).toBeVisible({ timeout: 60_000 });

  const verificationURL = await waitForVerificationURL(request, email);
  await page.goto(verificationURL);
  await expect(page.getByText("Your email is verified")).toBeVisible({ timeout: 60_000 });
  await page.getByRole("link", { name: "Sign in securely" }).click();
  await page.getByLabel("Email").fill(email);
  await page.getByLabel("Password").fill(password);
  await page.getByRole("button", { name: "Sign in and continue" }).click();
  await expect(page).toHaveURL(/\/en\/today$/, { timeout: 60_000 });

  await expect(page.getByRole("link", { name: "Start task" })).toBeVisible({ timeout: 180_000 });
  await page.getByTestId("coach-open").focus();
  await page.keyboard.press("Enter");
  const coachInput = page.locator("#coach-input");
  await expect(coachInput).toBeEnabled({ timeout: 60_000 });
  await coachInput.fill("Help me identify the decision I should make before the next task.");
  await coachInput.press("Enter");
  const coachAnswer = "Keep this grounded in your focused Mission and current evidence. Name the decision you are making and one constraint that would change it.";
  await expect(page.getByText(coachAnswer)).toBeVisible({ timeout: 180_000 });
  await page.keyboard.press("Escape");
  await expect(coachInput).toHaveCount(0);
  await page.reload();
  await page.getByTestId("coach-open").click();
  await expect(page.getByText(coachAnswer)).toBeVisible({ timeout: 60_000 });
  await page.keyboard.press("Escape");

  const contractBoundary = await page.evaluate(async () => {
    const response = await fetch("/api/v1/admin/usage", {
      credentials: "include",
      cache: "no-store",
      headers: { Accept: "application/vnd.lites.usage-snapshot.v2+json", "X-Audit-Reason": "real E2E contract-service routing boundary" },
    });
    return { status: response.status, contentType: response.headers.get("Content-Type") ?? "", body: await response.json() as { code?: string } };
  });
  expect(contractBoundary.status).toBe(403);
  expect(contractBoundary.contentType).toContain("application/problem+json");
  expect(contractBoundary.body.code).toBe("permission_denied");
  await expectNoSeriousAccessibilityViolations(page);
  await page.goto("/en/map");
  await expect(page.getByText("Durable capability calibration")).toBeVisible({ timeout: 60_000 });
  await page.getByRole("button", { name: "Correct", exact: true }).first().focus();
  await page.keyboard.press("Enter");
  const correctedStatement = "I coordinated incident recovery and can transfer that practice to reliable AI application operations.";
  await page.getByLabel("Correct assessment").fill(correctedStatement);
  await page.getByRole("button", { name: "Save correction" }).focus();
  await page.keyboard.press("Enter");
  await expect(page.getByText("The claim is durable and a new route revision is generating.")).toBeVisible({ timeout: 60_000 });
  await expect(async () => {
    await page.reload();
    await expect(page.getByRole("button", { name: "Accept this route revision" })).toBeVisible({ timeout: 15_000 });
  }).toPass({ timeout: 180_000, intervals: [2_000, 5_000, 10_000] });
  await page.getByRole("button", { name: "Accept this route revision" }).click();
  await expect(page.getByText("The new route revision is accepted.")).toBeVisible({ timeout: 60_000 });
  await page.reload();
  await expect(page.getByText("Durable claim available").first()).toBeVisible({ timeout: 60_000 });
  await expectNoSeriousAccessibilityViolations(page);
  await page.goto("/en/settings");
  await page.getByRole("button", { name: "Sign out" }).click();
  await expect(page).toHaveURL(/\/en\/login$/);
  await page.getByLabel("Email").fill(email);
  await page.getByLabel("Password").fill(password);
  await page.getByRole("button", { name: "Sign in and continue" }).click();
  await expect(page).toHaveURL(/\/en\/today$/, { timeout: 60_000 });
  await page.goto("/en/map");
  await expect(page.getByText("Durable claim available").first()).toBeVisible({ timeout: 60_000 });
  await page.goto("/en/today");
  await expect(page.getByRole("link", { name: "Start task" })).toBeVisible({ timeout: 180_000 });
  await page.reload();
  await page.getByRole("link", { name: "Start task" }).focus();
  await page.keyboard.press("Enter");
  const begin = page.getByRole("button", { name: "Begin task" });
  await expect(begin).toBeVisible({ timeout: 60_000 });
  await begin.focus();
  await page.keyboard.press("Enter");
  await page.getByTestId("task-content").fill("Define durable states, legal transitions, terminal-state guards, and an idempotent recovery path.");
  await page.getByLabel("Your understanding").fill("The database event and CAS version are authoritative; retries may repeat delivery but cannot repeat the committed effect.");
  const submit = page.getByRole("button", { name: "Submit for review" });
  await expect(submit).toBeEnabled({ timeout: 60_000 });
  await submit.focus();
  await page.keyboard.press("Enter");
  await expect(page.getByText("Review complete")).toBeVisible({ timeout: 180_000 });
  await page.reload();
  await page.goto("/en/evidence");
  await expect(page.locator(".ledger-row").first()).toBeVisible({ timeout: 60_000 });
  await expect(page.getByText("No evidence yet.")).toHaveCount(0);
  await expectNoSeriousAccessibilityViolations(page);
});

async function expectNoSeriousAccessibilityViolations(page: import("@playwright/test").Page) {
  const results = await new AxeBuilder({ page }).analyze();
  expect(results.violations.filter((violation) => ["serious", "critical"].includes(violation.impact ?? ""))).toEqual([]);
}

async function waitForVerificationURL(request: APIRequestContext, email: string) {
  for (let attempt = 0; attempt < 90; attempt += 1) {
    const list = await request.get(`${mailAPI}/messages?query=${encodeURIComponent(`to:${email}`)}`);
    if (list.ok()) {
      const body = await list.json() as { messages?: Array<{ ID?: string; id?: string }> };
      const id = body.messages?.[0]?.ID ?? body.messages?.[0]?.id;
      if (id) {
        const message = await request.get(`${mailAPI}/message/${encodeURIComponent(id)}`);
        if (message.ok()) {
          const verificationURL = verificationURLFromMessage(await message.json() as MailMessageDetail);
          if (verificationURL) return verificationURL;
        }
      }
    }
    await new Promise((resolve) => setTimeout(resolve, 1000));
  }
  throw new Error(`verification email was not delivered for ${email}`);
}

type MailMessageDetail = { Decoded?: string; Raw?: string };

function verificationURLFromMessage(message: MailMessageDetail) {
  const candidates = [message.Decoded, message.Raw]
    .filter((value): value is string => typeof value === "string")
    .flatMap((value) => value.match(/https?:\/\/[^\s"'<>]+/g) ?? []);
  for (const candidate of candidates) {
    try {
      const parsed = new URL(candidate.replaceAll("&amp;", "&"));
      if (!parsed.pathname.endsWith("/verify-email") || !parsed.searchParams.get("token")) continue;
      if (!new Set(["web", "127.0.0.1", "localhost"]).has(parsed.hostname)) continue;
      if (parsed.hostname === "web") parsed.hostname = "127.0.0.1";
      parsed.port = "3118";
      return parsed.toString();
    } catch {
      // Ignore malformed text and continue to the next URL in the message.
    }
  }
  return undefined;
}
