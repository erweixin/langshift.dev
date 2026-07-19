import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import type { RouteRevision } from "@/lib/use-product-workspace";
import { CapabilityClaimReview } from "./capability-claim-review";

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.restoreAllMocks();
});

it("persists an inferred claim, confirmation revision, and replacement route", async () => {
  const requests: Array<{ path: string; method: string; body: Record<string, unknown> | null }> = [];
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    const path = String(input).replace(/^https?:\/\/[^/]+/, "");
    const method = init?.method ?? "GET";
    const body = init?.body ? JSON.parse(String(init.body)) as Record<string, unknown> : null;
    requests.push({ path, method, body });
    if (method === "GET") return jsonResponse({ items: [] });
    if (path === "/api/v1/capability-claims") return jsonResponse({ id: "6b000000-0000-4000-8000-000000000020", version: 1, status: "active", claim_set_hash: "a".repeat(64) });
    if (path.endsWith("/revisions")) return jsonResponse({ id: "6b000000-0000-4000-8000-000000000020", version: 2, status: "active", claim_set_hash: "b".repeat(64) });
    if (path === "/api/v1/route-revisions") return jsonResponse({ route_revision_id: "6b000000-0000-4000-8000-000000000021", revision_version: 1, status: "generating" }, 202);
    throw new Error(`unexpected request ${method} ${path}`);
  });
  const changed = vi.fn(async () => undefined);
  const routeRevision: RouteRevision = { ...acceptedRoute("6b000000-0000-4000-8000-000000000023"), id: "6b000000-0000-4000-8000-000000000022" };
  render(<CapabilityClaimReview locale="en" missionID={routeRevision.mission_id} routeRevision={routeRevision} assessments={[{ statement: "Incident response transfers to reliable agent operations", capability_ids: ["6b000000-0000-4000-8000-000000000024"], evidence_ids: [], confidence: "inferred" }]} onChanged={changed} />);
  await screen.findByText("Planner inference awaiting user claim");
  fireEvent.click(screen.getByRole("button", { name: "Confirm" }));
  await screen.findByText("The claim is durable and a new route revision is generating.");
  expect(changed).toHaveBeenCalledOnce();
  expect(requests.map((request) => `${request.method} ${request.path}`)).toEqual([
    "GET /api/v1/capability-claims",
    "POST /api/v1/capability-claims",
    "POST /api/v1/capability-claims/6b000000-0000-4000-8000-000000000020/revisions",
    "POST /api/v1/route-revisions",
  ]);
  expect(requests[2]?.body).toMatchObject({ action: "confirm", expected_claim_version: 1 });
  expect(requests[3]?.body).toMatchObject({ expected_route_version: 4, expected_claim_set_hash: "b".repeat(64) });
  await waitFor(() => expect(screen.queryByText("The change was not saved")).toBeNull());
});

it.each([
  {
    action: "correct",
    open: "Correct",
    field: "Correct assessment",
    submit: "Save correction",
    value: "I supported the incident migration and coordinated recovery, but did not lead the platform work.",
  },
  {
    action: "dispute",
    open: "Dispute",
    field: "Reason for dispute",
    submit: "Submit dispute",
    value: "This judgment attributes work that was performed by another team.",
  },
])("persists an existing claim $action revision and replacement route", async ({ action, open, field, submit, value }) => {
  const claimID = "6b000000-0000-4000-8000-000000000030";
  const missionID = "6b000000-0000-4000-8000-000000000031";
  const capabilityID = "6b000000-0000-4000-8000-000000000032";
  const requests: Array<{ path: string; method: string; body: Record<string, unknown> | null }> = [];
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    const path = String(input).replace(/^https?:\/\/[^/]+/, "");
    const method = init?.method ?? "GET";
    const body = init?.body ? JSON.parse(String(init.body)) as Record<string, unknown> : null;
    requests.push({ path, method, body });
    if (method === "GET") return jsonResponse({ items: [{ id: claimID, version: 3, mission_id: missionID, capability_id: capabilityID, status: "active", statement: "I led the incident migration." }] });
    if (path.endsWith("/revisions")) return jsonResponse({ id: claimID, version: 4, status: action === "dispute" ? "disputed" : "active", claim_set_hash: "c".repeat(64) });
    if (path === "/api/v1/route-revisions") return jsonResponse({ route_revision_id: "6b000000-0000-4000-8000-000000000033", revision_version: 1, status: "generating" }, 202);
    throw new Error(`unexpected request ${method} ${path}`);
  });
  render(<CapabilityClaimReview locale="en" missionID={missionID} routeRevision={acceptedRoute(missionID)} assessments={[{ statement: "I led the incident migration.", capability_ids: [capabilityID], evidence_ids: [], confidence: "inferred" }]} onChanged={async () => undefined} />);
  await screen.findByText("Durable claim available");
  fireEvent.click(screen.getByRole("button", { name: open }));
  fireEvent.change(screen.getByLabelText(field), { target: { value } });
  fireEvent.click(screen.getByRole("button", { name: submit }));
  await screen.findByText("The claim is durable and a new route revision is generating.");
  expect(requests.map((request) => `${request.method} ${request.path}`)).toEqual([
    "GET /api/v1/capability-claims",
    `POST /api/v1/capability-claims/${claimID}/revisions`,
    "POST /api/v1/route-revisions",
  ]);
  expect(requests[1]?.body).toMatchObject({ action, reason: action === "dispute" ? value : "User corrected the route assessment", expected_claim_version: 3 });
  if (action === "correct") expect(requests[1]?.body).toMatchObject({ capability_id: capabilityID, statement: value });
  else expect(requests[1]?.body).not.toHaveProperty("statement");
  expect(requests[2]?.body).toMatchObject({ expected_route_version: 4, expected_claim_set_hash: "c".repeat(64) });
});

function acceptedRoute(missionID: string): RouteRevision {
  return {
    id: "6b000000-0000-4000-8000-000000000034",
    version: 3,
    mission_id: missionID,
    route_version: 4,
    base_route_version: 3,
    status: "accepted",
    claim_set_hash: "0".repeat(64),
    input_manifest: {},
    route: null,
    agent_profile_snapshot_id: "route-planner:test",
    ontology_snapshot_id: "ontology:test",
    content_snapshot_id: "content:test",
    planner_command_id: null,
    planner_run_id: null,
    accepted_at: "2026-07-18T00:00:00Z",
    stale_reason: null,
    failure_reason: null,
    created_at: "2026-07-18T00:00:00Z",
    updated_at: "2026-07-18T00:00:00Z",
  };
}

it("polls durable route state while a replacement revision is generating", async () => {
  vi.useFakeTimers();
  vi.spyOn(globalThis, "fetch").mockResolvedValue(jsonResponse({ items: [] }));
  const changed = vi.fn(async () => undefined);
  const route = { ...acceptedRoute("6b000000-0000-4000-8000-000000000040"), status: "generating" as const };
  render(<CapabilityClaimReview locale="en" missionID={route.mission_id} routeRevision={route} assessments={[]} onChanged={changed} />);
  expect(screen.getByText("The claim revision is saved. You can accept the route here when Planner finishes.")).not.toBeNull();
  await vi.advanceTimersByTimeAsync(1_500);
  expect(changed).toHaveBeenCalledOnce();
});

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}
