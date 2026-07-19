import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { TaskWorkspace } from "./task-workspace";

const fixture = vi.hoisted(() => ({
  taskID: "8b000000-0000-4000-8000-000000000001",
  submissionID: "8b000000-0000-4000-8000-000000000002",
  generationID: "8b000000-0000-4000-8000-000000000003",
  runID: "8b000000-0000-4000-8000-000000000004",
  rubricID: "8b000000-0000-4000-8000-000000000005",
  reviewID: "8b000000-0000-4000-8000-000000000006",
  evidenceID: "8b000000-0000-4000-8000-000000000007",
  task: null as unknown,
}));

vi.mock("next/navigation", () => ({
  useSearchParams: () => new URLSearchParams(`task=${fixture.taskID}`),
}));
vi.mock("@/lib/demo", () => ({ demoMode: false }));
vi.mock("@/lib/use-product-workspace", () => ({
  useProductWorkspace: () => ({
    tasks: [fixture.task],
    currentRouteTasks: [fixture.task],
    currentTask: fixture.task,
    loading: false,
    error: "",
    retry: vi.fn(async () => undefined),
    missions: { rubrics: [{ id: fixture.rubricID, practice_kind: "code" }] },
  }),
}));

afterEach(() => {
  cleanup();
  localStorage.clear();
  vi.restoreAllMocks();
});

it("restores a completed review from the durable task snapshot", async () => {
  fixture.task = taskFixture({
    status: "completed",
    version: 5,
    current_review_id: fixture.reviewID,
    generation_status: "succeeded",
    review_id: fixture.reviewID,
    evidence_id: fixture.evidenceID,
  });
  const requests: string[] = [];
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    const path = String(input).replace(/^https?:\/\/[^/]+/, "");
    requests.push(`${init?.method ?? "GET"} ${path}`);
    if (path === `/api/v1/reviews/${fixture.reviewID}`) return jsonResponse(reviewFixture());
    throw new Error(`unexpected request ${path}`);
  });

  render(<TaskWorkspace locale="en" />);

  await screen.findByRole("heading", { name: "A durable review" });
  expect(requests).toEqual([`GET /api/v1/reviews/${fixture.reviewID}`]);
  expect(requests.some((request) => request.startsWith("POST "))).toBe(false);
});

it("retries a failed generation without creating a second submission", async () => {
  fixture.task = taskFixture({ status: "submitted", version: 3, generation_status: "failed", failure_reason: "run_failed" });
  const requests: string[] = [];
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    const path = String(input).replace(/^https?:\/\/[^/]+/, "");
    requests.push(`${init?.method ?? "GET"} ${path}`);
    if (path === "/api/v1/daily-tasks") {
      return jsonResponse({ items: [taskFixture({ status: "submitted", version: 3, generation_status: "failed", failure_reason: "run_failed" })] });
    }
    if (path === "/api/v1/reviews" && init?.method === "POST") {
      return jsonResponse({ generation_id: fixture.generationID, run_id: fixture.runID, status: "queued", accepted_at: new Date().toISOString(), replayed: false }, 202);
    }
    if (path === `/api/v1/reviews/${fixture.reviewID}`) return jsonResponse(reviewFixture());
    throw new Error(`unexpected request ${path}`);
  });

  // The polling read after admission sees the reconciled terminal snapshot.
  let listReads = 0;
  const fetchMock = vi.mocked(globalThis.fetch);
  fetchMock.mockImplementation(async (input, init) => {
    const path = String(input).replace(/^https?:\/\/[^/]+/, "");
    requests.push(`${init?.method ?? "GET"} ${path}`);
    if (path === "/api/v1/daily-tasks") {
      listReads += 1;
      return jsonResponse({ items: [listReads === 1 ? taskFixture({ status: "submitted", version: 3, generation_status: "failed", failure_reason: "run_failed" }) : taskFixture({ status: "completed", version: 5, current_review_id: fixture.reviewID, generation_status: "succeeded", review_id: fixture.reviewID, evidence_id: fixture.evidenceID })] });
    }
    if (path === "/api/v1/reviews" && init?.method === "POST") return jsonResponse({ generation_id: fixture.generationID, run_id: fixture.runID, status: "queued", accepted_at: new Date().toISOString(), replayed: false }, 202);
    if (path === `/api/v1/reviews/${fixture.reviewID}`) return jsonResponse(reviewFixture());
    throw new Error(`unexpected request ${path}`);
  });

  render(<TaskWorkspace locale="en" />);
  await screen.findByText("review_run_failed");
  screen.getByRole("button", { name: "Retry review" }).click();
  await screen.findByRole("heading", { name: "A durable review" });
  await waitFor(() => expect(requests.filter((request) => request === "POST /api/v1/reviews")).toHaveLength(1));
  expect(requests.some((request) => request === "POST /api/v1/submissions")).toBe(false);
});

it("polls durable product state without reading the internal Agent run", async () => {
  fixture.task = taskFixture({ status: "reviewing", version: 4, generation_status: "generating" });
  const requests: string[] = [];
  let listReads = 0;
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const path = String(input).replace(/^https?:\/\/[^/]+/, "");
    requests.push(`GET ${path}`);
    if (path === "/api/v1/daily-tasks") {
      listReads += 1;
      return jsonResponse({
        items: [
          listReads === 1
            ? taskFixture({ status: "reviewing", version: 4, generation_status: "generating" })
            : taskFixture({ status: "completed", version: 5, current_review_id: fixture.reviewID, generation_status: "succeeded", review_id: fixture.reviewID, evidence_id: fixture.evidenceID }),
        ],
      });
    }
    if (path === `/api/v1/reviews/${fixture.reviewID}`) return jsonResponse(reviewFixture());
    throw new Error(`unexpected request ${path}`);
  });

  render(<TaskWorkspace locale="en" />);

  await screen.findByRole("heading", { name: "A durable review" }, { timeout: 3_000 });
  expect(requests.some((request) => request.includes("/api/v1/runs/"))).toBe(false);
  expect(listReads).toBe(2);
});

function taskFixture(overrides: Record<string, unknown>) {
  const at = "2026-07-18T08:00:00Z";
  const {
    generation_status,
    review_id,
    evidence_id,
    failure_reason,
    ...resourceOverrides
  } = overrides;
  return {
    id: fixture.taskID,
    version: 3,
    mission_id: "8b000000-0000-4000-8000-000000000010",
    route_revision_id: "8b000000-0000-4000-8000-000000000011",
    status: "submitted",
    practice_kind: "code",
    task: { schema_version: 1, title: "Recovery task", objective: "Recover", why_this_task: "Durability matters", key_judgment: "Resume", estimated_minutes: 30, difficulty: "standard", practice_kind: "code", explanation: [{ title: "Recovery", content: "Use server state." }], example: "", practice: { instructions: "Implement recovery", starter_content: "", deterministic_checks: [], success_criteria: ["Recovers"] }, capability_ids: ["capability-1"], evidence_targets: ["review"], next_task_hint: "" },
    estimated_minutes: 30,
    difficulty: "standard",
    focus_version: 1,
    scheduled_for: "2026-07-18",
    rescheduled_to: null,
    current_submission_id: fixture.submissionID,
    current_review_id: null,
    completed_at: null,
    created_at: at,
    updated_at: at,
    ...resourceOverrides,
    review_recovery: {
      submission_id: fixture.submissionID,
      submission_revision: 1,
      generation_id: fixture.generationID,
      run_id: fixture.runID,
      rubric_version_id: fixture.rubricID,
      generation_status: generation_status ?? "generating",
      review_id: review_id ?? null,
      evidence_id: evidence_id ?? null,
      failure_reason: failure_reason ?? null,
      generation_created_at: at,
      generation_updated_at: at,
      generation_completed_at: null,
    },
  };
}

function reviewFixture() {
  return {
    id: fixture.reviewID,
    status: "completed",
    evidence_id: fixture.evidenceID,
    reviewed_at: "2026-07-18T08:01:00Z",
    review: {
      schema_version: 1,
      verdict: "pass",
      summary: "A durable review",
      deterministic_results: { checks: [] },
      dimensions: [],
      strengths: ["Recovered from server state"],
      improvements: [],
      capability_evidence: [{ capability_id: "capability-1", level: "demonstrated", statement: "Recovery is durable." }],
      next_action: "Continue",
      uncertainty: "None",
    },
  };
}

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}
