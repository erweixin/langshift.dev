import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { RouteReview } from "./route-review";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

it("uses a fresh idempotency key when retrying a failed route generation", async () => {
  const keys: string[] = [];
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    const path = String(input).replace(/^https?:\/\/[^/]+/, "");
    if (path === "/api/v1/onboarding-sessions/8c000000-0000-4000-8000-000000000001" && (init?.method ?? "GET") === "GET") {
      return jsonResponse({ id: "8c000000-0000-4000-8000-000000000001", version: 3, status: "route_failed", mission_id: null, route_revision_id: null, route: null, claim_version: null, updated_at: "2026-07-18T08:00:00Z" });
    }
    if (path.endsWith("/route-preview") && init?.method === "POST") {
      keys.push(new Headers(init.headers).get("Idempotency-Key") ?? "");
      return jsonResponse({ run_id: "8c000000-0000-4000-8000-000000000010" }, 202);
    }
    throw new Error(`unexpected request ${path}`);
  });

  render(<RouteReview locale="en" from="Support" to="Product" onboardingSessionID="8c000000-0000-4000-8000-000000000001" onboardingVersion={3} targetRoleProfileID="8c000000-0000-4000-8000-000000000002" />);

  const retry = await screen.findByRole("button", { name: "Retry generation" });
  retry.click();
  await waitFor(() => expect(keys).toHaveLength(2));
  expect(keys[0]).not.toBe("");
  expect(keys[1]).not.toBe(keys[0]);
});

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}
