import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { dictionary } from "@/i18n/dictionaries";
import { apiRequest } from "@/lib/api/client";
import { CoachDrawer } from "./coach-drawer";

vi.mock("@/lib/demo", () => ({ demoMode: false }));
vi.mock("@/lib/api/client", () => ({
  ApiError: class ApiError extends Error {},
  apiRequest: vi.fn(),
  newIdempotencyKey: () => "coach-request-00000001",
}));

const request = vi.mocked(apiRequest);

afterEach(() => {
  cleanup();
  request.mockReset();
  window.localStorage.clear();
});

it("recovers the fixed Coach conversation without exposing Agent Run polling", async () => {
  request
    .mockResolvedValueOnce({ id: "conversation-1", version: 1 })
    .mockResolvedValueOnce({
      run_id: "run-1",
      status: "accepted",
      accepted_at: "2026-07-19T00:00:00Z",
    })
    .mockResolvedValueOnce({
      id: "conversation-1",
      mission_id: "mission-1",
      version: 2,
      mode: "coach",
      status: "active",
      messages: [
        {
          id: "message-user",
          run_id: "run-1",
          role: "user",
          content: "How should I recover this workflow?",
          created_at: "2026-07-19T00:00:00Z",
        },
        {
          id: "message-coach",
          run_id: "run-1",
          role: "assistant",
          content: "Start from the durable event and verify the CAS boundary.",
          created_at: "2026-07-19T00:00:01Z",
        },
      ],
    });

  render(
    <CoachDrawer
      dictionary={dictionary("en")}
      context="task"
      missionID="mission-1"
      locale="en"
    />,
  );

  fireEvent.click(screen.getByRole("button", { name: "Ask Coach" }));
  fireEvent.change(screen.getByPlaceholderText("Ask about this step…"), {
    target: { value: "How should I recover this workflow?" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Send" }));

  expect(
    await screen.findByText(
      "Start from the durable event and verify the CAS boundary.",
    ),
  ).not.toBeNull();
  expect(request.mock.calls.map(([path]) => path)).toEqual([
    "/v1/conversations",
    "/v1/messages",
    "/v1/conversations/conversation-1?limit=50",
  ]);
  expect(
    request.mock.calls.some(([path]) => String(path).startsWith("/v1/runs/")),
  ).toBe(false);
});

it("restores the durable Coach projection when the drawer opens after refresh", async () => {
  window.localStorage.setItem("lites.coach.conversation.mission-1", "conversation-1");
  request.mockResolvedValueOnce({
    id: "conversation-1",
    mission_id: "mission-1",
    version: 2,
    mode: "coach",
    status: "active",
    messages: [{
      id: "message-coach",
      run_id: "run-1",
      role: "assistant",
      content: "Recovered from the durable projection.",
      created_at: "2026-07-19T00:00:01Z",
    }],
  });

  render(<CoachDrawer dictionary={dictionary("en")} context="task" missionID="mission-1" locale="en" />);
  fireEvent.click(screen.getByRole("button", { name: "Ask Coach" }));

  expect(await screen.findByText("Recovered from the durable projection.")).not.toBeNull();
  expect(request).toHaveBeenCalledWith("/v1/conversations/conversation-1?limit=50", expect.objectContaining({ signal: expect.any(AbortSignal) }));
});
