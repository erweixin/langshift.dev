import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { CreateStudio } from "./create-studio";

const navigation = vi.hoisted(() => ({
  projectID: "8a000000-0000-4000-8000-000000000001",
}));

vi.mock("next/navigation", () => ({
  useSearchParams: () =>
    new URLSearchParams(`project=${encodeURIComponent(navigation.projectID)}`),
}));

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

it("restores a durable Create workspace instead of starting a second project", async () => {
  const requests: string[] = [];
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    const path = String(input).replace(/^https?:\/\/[^/]+/, "");
    requests.push(`${init?.method ?? "GET"} ${path}`);
    if (path === `/api/v1/projects/${navigation.projectID}`)
      return jsonResponse(projectDetail());
    throw new Error(`unexpected request ${path}`);
  });

  render(<CreateStudio locale="en" />);

  expect(
    screen.getByText(
      "Restoring Workspace, milestones, evidence, and export state.",
    ),
  ).not.toBeNull();
  await screen.findByRole("heading", { name: "Recovery tests" });
  expect((screen.getByTestId("workspace-output") as HTMLTextAreaElement).value).toBe("");
  expect(requests).toEqual([
    `GET /api/v1/projects/${navigation.projectID}`,
  ]);
  expect(requests.some((request) => request.startsWith("POST "))).toBe(false);
});

it("offers an explicit retry when the recovery snapshot is unavailable", async () => {
  let attempts = 0;
  vi.spyOn(globalThis, "fetch").mockImplementation(async () => {
    attempts += 1;
    if (attempts === 1)
      return jsonResponse(
        {
          type: "about:blank",
          title: "Dependency unavailable",
          status: 503,
          code: "dependency_unavailable",
          detail: "try again",
          retryable: true,
        },
        503,
      );
    return jsonResponse(projectDetail());
  });

  render(<CreateStudio locale="en" />);
  await screen.findByText("The durable project could not be loaded.");
  screen.getByRole("button", { name: "Retry restore" }).click();
  await screen.findByRole("heading", { name: "Recovery tests" });
  await waitFor(() => expect(attempts).toBe(2));
});

function projectDetail() {
  const at = "2026-07-18T08:00:00Z";
  return {
    project: {
      id: navigation.projectID,
      mission_id: "8a000000-0000-4000-8000-000000000002",
      accepted_route_revision_id:
        "8a000000-0000-4000-8000-000000000003",
      version: 9,
      status: "active",
      project_kind: "code",
      title: "Recoverable service",
      created_at: at,
      updated_at: at,
      completed_at: null,
    },
    brief: "Build a service whose recovery path is independently verified.",
    workspace: {
      id: "8a000000-0000-4000-8000-000000000004",
      project_id: navigation.projectID,
      workspace_id: "8a000000-0000-4000-8000-000000000005",
      branch_name: "project/recovery",
      base_revision: "inline:base",
      head_revision: `inline:sha256:${"a".repeat(64)}`,
      binding_manifest_hash: "b".repeat(64),
      version: 2,
      updated_at: at,
    },
    milestones: [
      {
        id: "8a000000-0000-4000-8000-000000000006",
        version: 5,
        sequence: 1,
        required: true,
        status: "completed",
        title: "Lifecycle contract",
        result: "The lifecycle and failure boundary are implemented.",
        verification_test_run_id:
          "8a000000-0000-4000-8000-000000000007",
        evidence_ids: ["8a000000-0000-4000-8000-000000000008"],
        latest_evaluation: {
          generation_id: "8a000000-0000-4000-8000-000000000009",
          run_id: "8a000000-0000-4000-8000-00000000000a",
          status: "succeeded",
          failure_reason: null,
          evidence_id: "8a000000-0000-4000-8000-000000000008",
          updated_at: at,
        },
        completed_at: at,
        updated_at: at,
      },
      {
        id: "8a000000-0000-4000-8000-00000000000b",
        version: 1,
        sequence: 2,
        required: true,
        status: "planned",
        title: "Recovery tests",
        result: null,
        verification_test_run_id: null,
        evidence_ids: [],
        latest_evaluation: null,
        completed_at: null,
        updated_at: at,
      },
    ],
    artifacts: [],
    latest_export: null,
  };
}

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}
