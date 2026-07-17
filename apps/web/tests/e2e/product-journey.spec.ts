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
  await page.getByRole("button", { name: "Edit bridge" }).click();
  await page.getByLabel("Correct the transfer relationship").fill("From async UI orchestration to durable, reconciled agent effects");
  await page.getByRole("button", { name: "Save correction" }).click();
  await expect(page.getByText("Capability bridge · Corrected by you")).toBeVisible();
  for (const capability of ["State modeling", "Asynchronous programming", "Durable run lifecycle", "Effect reconciliation"]) {
    await page.getByRole("button", { name: `Confirm ${capability}` }).click();
  }
  await expect(page.getByText("You confirmed the route. The first test is ready.")).toBeVisible();
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
  await page.getByRole("button", { name: "Make this easier" }).click();
  await expect(page.getByRole("button", { name: "Adjusted to easier" })).toBeVisible();
  await expect(page.getByText("15 min")).toBeVisible();
  await page.getByTestId("task-content").fill("scheduled -> running -> completed; terminal states reject backward transitions");
  await page.getByLabel("Your understanding").fill("The durable record is authoritative across retries, so completed cannot become running again.");
  await page.getByRole("button", { name: "Submit for review" }).click();
  await expect(page.getByText("Review complete")).toBeVisible();
  await expect(page.getByText("Level: Demonstrated.")).toBeVisible();
});

test("multiple Missions can switch Focus without mixing Coach context", async ({ page }, testInfo) => {
  await page.goto("/en/today");
  if (testInfo.project.name.includes("mobile")) {
    await page.getByLabel("Switch Mission").selectOption("ai-product");
    await expect(page.getByLabel("Switch Mission")).toHaveValue("ai-product");
  } else {
    await page.getByRole("button", { name: /AI Product Strategist/ }).click();
    await expect(page.getByRole("button", { name: /AI Product Strategist/ })).toHaveAttribute("aria-pressed", "true");
  }
  await page.getByTestId("coach-open").click();
  await expect(page.getByRole("dialog", { name: "Coach" }).getByText(/AI Product Strategist/)).toBeVisible();
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

test("Organization console preserves privileged-action and sensitive-read boundaries", async ({ page }) => {
  await page.route("**/api/v1/auth/reauthentication", async (route) => {
    const request = route.request();
    expect(request.method()).toBe("POST");
    expect(request.headers()["idempotency-key"]).toBeTruthy();
    expect((await request.postDataJSON()).password).toBe("current-password-value");
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ valid_until: "2026-07-17T12:05:00Z" }) });
  });
  await page.route("**/api/v1/admin/task-packs", async (route) => {
    const request = route.request();
    expect(request.method()).toBe("POST");
    expect(request.headers()["content-type"]).toBe("application/vnd.lites.task-pack-publish.v2+json");
    expect(await request.postDataJSON()).toMatchObject({ revision: 1, name: "Crash recovery lab", task_template_ids: ["e1000000-0000-4000-8000-000000000021"], assignment: { required: true } });
    await route.fulfill({ status: 200, contentType: "application/vnd.lites.enterprise-resource.v2+json", body: JSON.stringify({ id: "e1000000-0000-4000-8000-000000000047", version: 1, status: "published", updated_at: "2026-07-17T12:00:00Z" }) });
  });
  const auditExportID = "78000000-0000-4000-8000-000000000060";
  await page.route("**/api/v1/admin/audit-exports", async (route) => {
    const request = route.request();
    expect(request.method()).toBe("POST");
    expect(request.headers()["content-type"]).toBe("application/vnd.lites.admin-audit-export-request.v2+json");
    expect(await request.postDataJSON()).toMatchObject({ kinds: ["contract_change", "accounting_adjustment", "admin_read"], format: "jsonl", reason: "Annual compliance evidence" });
    await route.fulfill({ status: 200, contentType: "application/vnd.lites.admin-audit-export.v2+json", body: JSON.stringify({ id: auditExportID, version: 1, status: "ready", format: "jsonl", record_count: 42, content_hash: "a".repeat(64), byte_size: 128, period_start: "2026-01-01T00:00:00Z", period_end: "2026-07-01T00:00:00Z", created_at: "2026-07-17T12:00:00Z", expires_at: "2026-08-16T12:00:00Z" }) });
  });
  await page.route(`**/api/v1/admin/audit-exports/${auditExportID}`, async (route) => {
    expect(route.request().headers()["x-audit-reason"]).toBe("Quarterly renewal review");
    await route.fulfill({ status: 200, contentType: "application/x-ndjson", headers: { "Content-Disposition": `attachment; filename="lites-audit-${auditExportID}.jsonl"`, Digest: "SHA-256=YWFh" }, body: '{"id":"record-1"}\n' });
  });
  await page.goto("/en/admin");
  await expect(page.getByRole("heading", { name: "Govern people, access, and commercial terms." })).toBeVisible();
  await expect(page.getByRole("link", { name: "Organization", exact: true })).toHaveAttribute("aria-current", "page");

  const usage = page.getByRole("button", { name: "Load usage" });
  await expect(usage).toBeDisabled();
  await page.getByLabel("Reason for this access").fill("Quarterly renewal review");
  await expect(usage).toBeEnabled();

  const password = page.getByLabel("Current password");
  await password.fill("current-password-value");
  await page.getByRole("button", { name: "Reauthenticate" }).click();
  await expect(password).toHaveValue("");
  await expect(page.getByText("This session is reauthenticated for five minutes.")).toBeVisible();

  const taskPack = page.getByRole("heading", { name: "Publish a task pack" }).locator("xpath=ancestor::form");
  await taskPack.getByLabel("Program ID").fill("e1000000-0000-4000-8000-000000000040");
  await taskPack.getByLabel("Revision").fill("1");
  await taskPack.getByLabel("Name").fill("Crash recovery lab");
  await taskPack.getByLabel("Task template IDs").fill("e1000000-0000-4000-8000-000000000021");
  await taskPack.getByRole("button", { name: "Publish task pack" }).click();
  await expect(page.getByText("Task pack published as an immutable revision.")).toBeVisible();

  const auditExport = page.getByRole("heading", { name: "Create a compliance audit export" }).locator("xpath=ancestor::form");
  await auditExport.getByLabel("Period start").fill("2026-01-01T00:00");
  await auditExport.getByLabel("Period end").fill("2026-07-01T00:00");
  await auditExport.getByLabel("Export reason").fill("Annual compliance evidence");
  await auditExport.getByRole("button", { name: "Create export" }).click();
  await expect(page.getByText("Audit export created with 42 records.")).toBeVisible();
  await auditExport.getByRole("button", { name: "Download export" }).click();
  await expect(page.getByText("Downloaded; the sensitive access was recorded by the service.")).toBeVisible();

  const results = await new AxeBuilder({ page }).disableRules(["color-contrast"]).analyze();
  expect(results.violations.filter((violation) => ["serious", "critical"].includes(violation.impact ?? ""))).toEqual([]);
});

test("enterprise primary lifecycle covers invitation, governed CSV, cohorts, sharing, revocation, and offboarding", async ({ page }) => {
  const programID = "81000000-0000-4000-8000-000000000001";
  const cohortID = "81000000-0000-4000-8000-000000000002";
  const userID = "81000000-0000-4000-8000-000000000003";
  const membershipID = "81000000-0000-4000-8000-000000000004";
  const roleProfileID = "81000000-0000-4000-8000-000000000005";
  const taskTemplateID = "81000000-0000-4000-8000-000000000006";
  const grantID = "81000000-0000-4000-8000-000000000007";
  const resourceID = "81000000-0000-4000-8000-000000000008";
  const mutation = (id: string, version: number, status: string) => JSON.stringify({ id, version, status, updated_at: "2026-07-17T12:00:00Z" });

  await page.route("**/api/v1/invitations", async (route) => {
    const request = route.request();
    expect(request.headers()["content-type"]).toBe("application/json");
    expect(await request.postDataJSON()).toMatchObject({ email: "learner@example.com", role: "member", expires_in_days: 7 });
    await route.fulfill({ status: 200, contentType: "application/json", body: mutation("invitation-1", 1, "pending") });
  });
  await page.route("**/api/v1/invitation-imports", async (route) => {
    expect(await route.request().postDataJSON()).toMatchObject({ object_ref: "s3://imports/acme/invitations.csv", content_hash: `sha256:${"a".repeat(64)}`, import_key: "invitation-import-2026-01", default_role: "member" });
    await route.fulfill({ status: 200, contentType: "application/json", body: mutation("invitation-import-1", 1, "queued") });
  });
  await page.route("**/api/v1/membership-imports", async (route) => {
    expect(await route.request().postDataJSON()).toMatchObject({ object_ref: "s3://imports/acme/members.csv", content_hash: `sha256:${"b".repeat(64)}`, import_key: "membership-import-2026-01", mode: "deactivate_missing" });
    await route.fulfill({ status: 200, contentType: "application/json", body: mutation("membership-import-1", 1, "queued") });
  });
  await page.route("**/api/v1/memberships", async (route) => {
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ items: [{ id: membershipID, user_id: userID, email: "learner@example.com", role: "member", status: "active", version: 2, joined_at: "2026-07-01T00:00:00Z", updated_at: "2026-07-01T00:00:00Z", deactivated_at: null }], next_cursor: null }) });
  });
  await page.route(`**/api/v1/memberships/${membershipID}`, async (route) => {
    const request = route.request();
    expect(request.method()).toBe("DELETE");
    expect(request.headers()["if-match"]).toBe('"2"');
    expect(await request.postDataJSON()).toMatchObject({ action: "deactivate", reason: "Employment ended", expected_membership_version: 2 });
    await route.fulfill({ status: 200, contentType: "application/json", body: mutation(membershipID, 3, "deactivated") });
  });
  await page.route("**/api/v1/admin/programs", async (route) => {
    expect(await route.request().postDataJSON()).toMatchObject({ name: "Agent operations", settings: {} });
    await route.fulfill({ status: 200, contentType: "application/vnd.lites.enterprise-resource.v2+json", body: mutation(programID, 1, "active") });
  });
  await page.route("**/api/v1/admin/cohorts", async (route) => {
    expect(await route.request().postDataJSON()).toMatchObject({ program_id: programID, name: "Q3 platform cohort" });
    await route.fulfill({ status: 200, contentType: "application/vnd.lites.enterprise-resource.v2+json", body: mutation(cohortID, 1, "active") });
  });
  await page.route(`**/api/v1/admin/cohorts/${cohortID}/enrollments`, async (route) => {
    expect(route.request().headers()["if-match"]).toBe('"1"');
    expect(await route.request().postDataJSON()).toMatchObject({ user_ids: [userID], expected_cohort_version: 1 });
    await route.fulfill({ status: 200, contentType: "application/vnd.lites.enterprise-resource.v2+json", body: mutation(cohortID, 2, "active") });
  });
  await page.route(`**/api/v1/admin/cohorts/${cohortID}/enrollments/${userID}`, async (route) => {
    expect(route.request().headers()["if-match"]).toBe('"2"');
    expect(await route.request().postDataJSON()).toMatchObject({ reason: "Moved to another cohort", expected_cohort_version: 2 });
    await route.fulfill({ status: 200, contentType: "application/vnd.lites.enterprise-resource.v2+json", body: mutation(cohortID, 3, "active") });
  });
  await page.route("**/api/v1/admin/role-packs", async (route) => {
    expect(await route.request().postDataJSON()).toMatchObject({ program_id: programID, revision: 1, role_profile_ids: [roleProfileID], task_template_ids: [taskTemplateID] });
    await route.fulfill({ status: 200, contentType: "application/vnd.lites.enterprise-resource.v2+json", body: mutation("81000000-0000-4000-8000-000000000009", 1, "published") });
  });

  await page.goto("/en/admin");
  const invitation = page.getByRole("heading", { name: "Invite one member" }).locator("xpath=ancestor::form");
  await invitation.getByLabel("Work email").fill("learner@example.com");
  await invitation.getByRole("button", { name: "Send invitation" }).click();
  await expect(page.getByText(/Invitation created/)).toBeVisible();

  const invitationImport = page.getByRole("heading", { name: "Import bulk invitations" }).locator("xpath=ancestor::form");
  await invitationImport.getByLabel("Staged CSV object").fill("s3://imports/acme/invitations.csv");
  await invitationImport.getByLabel("SHA-256").fill(`sha256:${"a".repeat(64)}`);
  await invitationImport.getByLabel("Import key").fill("invitation-import-2026-01");
  await invitationImport.getByRole("button", { name: "Start invitation import" }).click();
  await expect(page.getByText(/Bulk invitations entered/)).toBeVisible();

  const memberImport = page.getByRole("heading", { name: "Bulk synchronize or deactivate members" }).locator("xpath=ancestor::form");
  await memberImport.getByLabel("Staged CSV object").fill("s3://imports/acme/members.csv");
  await memberImport.getByLabel("SHA-256").fill(`sha256:${"b".repeat(64)}`);
  await memberImport.getByLabel("Import key").fill("membership-import-2026-01");
  await memberImport.getByLabel("Import mode").selectOption("deactivate_missing");
  await memberImport.getByRole("button", { name: "Start membership import" }).click();
  await expect(page.getByText(/Membership changes entered/)).toBeVisible();

  const program = page.getByRole("heading", { name: "Create an organization program" }).locator("xpath=ancestor::form");
  await program.getByLabel("Program name").fill("Agent operations");
  await program.getByRole("button", { name: "Create program" }).click();
  const cohort = page.getByRole("heading", { name: "Create a learning cohort" }).locator("xpath=ancestor::form");
  await cohort.getByLabel("Program ID").fill(programID);
  await cohort.getByLabel("Cohort name").fill("Q3 platform cohort");
  await cohort.getByRole("button", { name: "Create cohort" }).click();

  const enrollment = page.getByRole("heading", { name: "Enroll members in a cohort" }).locator("xpath=ancestor::form");
  await enrollment.getByLabel("Cohort ID").fill(cohortID);
  await enrollment.getByLabel("Current cohort version").fill("1");
  await enrollment.getByLabel("User IDs").fill(userID);
  await enrollment.getByRole("button", { name: "Enroll members" }).click();
  await expect(page.getByText("Members enrolled against the exact cohort version.")).toBeVisible();

  const removal = page.getByRole("heading", { name: "Remove a member from a cohort" }).locator("xpath=ancestor::form");
  await removal.getByLabel("Cohort ID").fill(cohortID);
  await removal.getByLabel("User ID").fill(userID);
  await removal.getByLabel("Current cohort version").fill("2");
  await removal.getByLabel("Removal reason").fill("Moved to another cohort");
  await removal.getByRole("button", { name: "Remove member" }).click();
  await expect(page.getByText(/Member removed from the cohort/)).toBeVisible();

  const rolePack = page.getByRole("heading", { name: "Publish a role-path pack" }).locator("xpath=ancestor::form");
  await rolePack.getByLabel("Program ID").fill(programID);
  await rolePack.getByLabel("Revision").fill("1");
  await rolePack.getByLabel("Role profile IDs").fill(roleProfileID);
  await rolePack.getByLabel("Task template IDs").fill(taskTemplateID);
  await rolePack.getByRole("button", { name: "Publish role pack" }).click();
  await expect(page.getByText(/Role pack published/)).toBeVisible();

  await page.getByRole("button", { name: "Load members" }).click();
  await expect(page.getByText("learner@example.com")).toBeVisible();
  const lifecycle = page.getByRole("heading", { name: "Deactivate a member or leave" }).locator("xpath=ancestor::form");
  await lifecycle.getByLabel("Membership ID").fill(membershipID);
  await lifecycle.getByLabel("Current membership version").fill("2");
  await lifecycle.getByLabel("Lifecycle reason").fill("Employment ended");
  await lifecycle.getByRole("button", { name: "Submit membership change" }).click();
  await expect(page.getByText(/Membership deactivated/)).toBeVisible();

  await page.route("**/api/v1/share-grants", async (route) => {
    const request = route.request();
    expect(request.headers()["content-type"]).toBe("application/vnd.lites.share-grant-create.v2+json");
    expect(await request.postDataJSON()).toMatchObject({ grantee_user_id: userID, resource_kind: "evidence", resource_id: resourceID, resource_revision: "sha256:evidence-revision-7", scope: ["read", "review"] });
    await route.fulfill({ status: 200, contentType: "application/vnd.lites.share-grant.v2+json", body: mutation(grantID, 1, "active") });
  });
  await page.route(`**/api/v1/share-grants/${grantID}`, async (route) => {
    expect(route.request().headers()["if-match"]).toBe('"1"');
    expect(await route.request().postDataJSON()).toMatchObject({ reason: "Reviewer engagement ended", expected_grant_version: 1 });
    await route.fulfill({ status: 200, contentType: "application/vnd.lites.share-grant.v2+json", body: mutation(grantID, 2, "revoked") });
  });
  await page.goto("/en/evidence");
  const share = page.getByRole("heading", { name: "Create a bounded share" }).locator("xpath=ancestor::form");
  await share.getByLabel("Grantee user ID").fill(userID);
  await share.getByLabel("Resource ID").fill(resourceID);
  await share.getByLabel("Exact resource revision").fill("sha256:evidence-revision-7");
  await share.getByLabel("review").check();
  await share.getByRole("button", { name: "Create explicit share" }).click();
  await expect(page.getByText(/Explicit share created/)).toBeVisible();
  const revocation = page.getByRole("heading", { name: "Revoke a share" }).locator("xpath=ancestor::form");
  await revocation.getByLabel("Revocation reason").fill("Reviewer engagement ended");
  await revocation.getByRole("button", { name: "Revoke and block new reads" }).click();
  await expect(page.getByText(/Share revoked/)).toBeVisible();
});

test("Support Center creates an encrypted case boundary and exposes frozen SLA clocks", async ({ page }) => {
  await page.route("**/api/v1/support/cases", async (route) => {
    const request = route.request();
    expect(request.method()).toBe("POST");
    expect(request.headers()["content-type"]).toBe("application/vnd.lites.support-case-create.v2+json");
    expect(request.headers()["idempotency-key"]).toBeTruthy();
    const body = await request.postDataJSON();
    expect(body).toMatchObject({ category: "availability", priority: "urgent", subject: "Agent runs unavailable", body: "Runs stop before scheduling." });
    await route.fulfill({ status: 201, contentType: "application/vnd.lites.support-case.v2+json", body: JSON.stringify({ id: "50000000-0000-4000-8000-000000000001", reference: "LTS-20260717-50000000", requester_user_id: "50000000-0000-4000-8000-000000000010", category: "availability", priority: "urgent", status: "open", subject: "Agent runs unavailable", support_tier: "enterprise", version: 1, response_due_at: "2026-07-17T13:30:00Z", resolution_due_at: "2026-07-17T17:00:00Z", first_responded_at: null, resolved_at: null, created_at: "2026-07-17T13:00:00Z", updated_at: "2026-07-17T13:00:00Z", messages: [{ id: "50000000-0000-4000-8000-000000000002", author_user_id: "50000000-0000-4000-8000-000000000010", author_kind: "customer", body: "Runs stop before scheduling.", created_at: "2026-07-17T13:00:00Z" }] }) });
  });
  await page.goto("/en/support");
  await expect(page.getByRole("heading", { name: "Every issue has a record. Every promise has a clock." })).toBeVisible();
  await page.getByLabel("Category").selectOption("availability");
  await page.getByLabel("Urgency").selectOption("urgent");
  await page.getByLabel("Subject").fill("Agent runs unavailable");
  const details = page.getByLabel("Details");
  await details.fill("Runs stop before scheduling.");
  await page.getByRole("button", { name: "Submit securely" }).click();
  await expect(details).toHaveValue("");
  await expect(page.getByText("Case LTS-20260717-50000000 was submitted securely.")).toBeVisible();
  await expect(page.getByText("First response")).toBeVisible();
  await expect(page.getByText("Resolution target")).toBeVisible();

  const results = await new AxeBuilder({ page }).disableRules(["color-contrast"]).analyze();
  expect(results.violations.filter((violation) => ["serious", "critical"].includes(violation.impact ?? ""))).toEqual([]);
});

test("public Trust Center makes no unearned certification claim", async ({ page }) => {
  await page.goto("/en/trust");
  await expect(page.getByRole("heading", { name: "Trust comes from verifiable boundaries." })).toBeVisible();
  await expect(page.getByRole("row", { name: /SOC 2 Type II Not claimed/ })).toBeVisible();
  await expect(page.getByRole("row", { name: /ISO 27001 Not claimed/ })).toBeVisible();
  await expect(page.getByRole("row", { name: /Penetration test Required before GA/ })).toBeVisible();
  const results = await new AxeBuilder({ page }).disableRules(["color-contrast"]).analyze();
  expect(results.violations.filter((violation) => ["serious", "critical"].includes(violation.impact ?? ""))).toEqual([]);
});

test("public status uses fresh evidence and fails stale evidence to unknown", async ({ page }) => {
  const generatedAt = new Date(Date.now() - 5_000).toISOString();
  const validUntil = new Date(Date.now() + 120_000).toISOString();
  await page.route("**/api/v1/public/status", async (route) => {
    expect(route.request().headers().accept).toBe("application/vnd.lites.public-status.v1+json");
    await route.fulfill({ status: 200, contentType: "application/vnd.lites.public-status.v1+json", body: JSON.stringify({ schema_version: 1, overall: "operational", generated_at: generatedAt, valid_until: validUntil, components: [{ id: "api", name: "Cloud API", state: "operational" }], incidents: [] }) });
  });
  await page.goto("/en/status");
  await expect(page.getByRole("heading", { name: "Operational" })).toBeVisible();
  await expect(page.getByText("Cloud API")).toBeVisible();
  let results = await new AxeBuilder({ page }).disableRules(["color-contrast"]).analyze();
  expect(results.violations.filter((violation) => ["serious", "critical"].includes(violation.impact ?? ""))).toEqual([]);

  await page.unroute("**/api/v1/public/status");
  await page.route("**/api/v1/public/status", async (route) => {
    await route.fulfill({ status: 200, contentType: "application/vnd.lites.public-status.v1+json", body: JSON.stringify({ schema_version: 1, overall: "operational", generated_at: new Date(Date.now() - 180_000).toISOString(), valid_until: new Date(Date.now() - 60_000).toISOString(), components: [{ id: "api", name: "Cloud API", state: "operational" }], incidents: [] }) });
  });
  await page.getByRole("button", { name: "Refresh" }).click();
  await expect(page.getByRole("heading", { name: "Unknown" })).toBeVisible();
  await expect(page.getByText("Status data unavailable")).toBeVisible();
  results = await new AxeBuilder({ page }).disableRules(["color-contrast"]).analyze();
  expect(results.violations.filter((violation) => ["serious", "critical"].includes(violation.impact ?? ""))).toEqual([]);
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
  await page.goto("/zh-CN/today");
  await expect(page.getByRole("heading", { name: "早上好。" })).toBeVisible();
  if (testInfo.project.name.includes("mobile")) await expect(page.getByRole("navigation", { name: "Primary navigation" })).toBeVisible();
  await page.getByRole("link", { name: "成长记录" }).click();
  await expect(page.getByRole("heading", { name: /每个判断/ })).toBeVisible();
});
