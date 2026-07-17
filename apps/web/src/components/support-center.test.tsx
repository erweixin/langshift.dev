import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { apiRequest } from "@/lib/api/client";
import { SupportCenter } from "./support-center";

vi.mock("@/lib/api/client", () => ({
  ApiError: class ApiError extends Error {
    constructor(message: string, readonly status: number, readonly requestID: string | null) { super(message); }
  },
  apiRequest: vi.fn(),
  newIdempotencyKey: vi.fn(() => "support-request-00000001"),
}));

const request = vi.mocked(apiRequest);
const supportCase = {
  id: "50000000-0000-4000-8000-000000000001", reference: "LTS-20260717-50000000", requester_user_id: "50000000-0000-4000-8000-000000000010", category: "availability", priority: "urgent", status: "open", subject: "Agent runs unavailable", support_tier: "enterprise", version: 1, response_due_at: "2026-07-17T13:30:00Z", resolution_due_at: "2026-07-17T17:00:00Z", first_responded_at: null, resolved_at: null, created_at: "2026-07-17T13:00:00Z", updated_at: "2026-07-17T13:00:00Z", messages: [{ id: "50000000-0000-4000-8000-000000000002", author_user_id: "50000000-0000-4000-8000-000000000010", author_kind: "customer", body: "Runs stop before scheduling.", created_at: "2026-07-17T13:00:00Z" }],
} as const;

afterEach(() => {
  cleanup();
  request.mockReset();
});

describe("SupportCenter", () => {
  it("submits a governed encrypted-case request and clears plaintext on success", async () => {
    request.mockResolvedValueOnce(supportCase);
    render(<SupportCenter locale="en" />);
    expect(request).not.toHaveBeenCalled();

    fireEvent.change(screen.getByLabelText("Category"), { target: { value: "availability" } });
    fireEvent.change(screen.getByLabelText("Urgency"), { target: { value: "urgent" } });
    fireEvent.change(screen.getByLabelText("Subject"), { target: { value: "Agent runs unavailable" } });
    const details = screen.getByLabelText("Details") as HTMLTextAreaElement;
    fireEvent.change(details, { target: { value: "Runs stop before scheduling." } });
    fireEvent.click(screen.getByRole("button", { name: "Submit securely" }));

    await waitFor(() => expect(request).toHaveBeenCalledTimes(1));
    expect(request).toHaveBeenCalledWith("/v1/support/cases", expect.objectContaining({ method: "POST", idempotencyKey: "support-request-00000001", accept: "application/vnd.lites.support-case.v2+json", contentType: "application/vnd.lites.support-case-create.v2+json", body: { request_id: "support-request-00000001", category: "availability", priority: "urgent", subject: "Agent runs unavailable", body: "Runs stop before scheduling." } }));
    expect(details.value).toBe("");
    expect(screen.getByText(/was submitted securely/)).toBeTruthy();
  });

  it("reads cases explicitly and binds replies to the selected version", async () => {
    const replied = { ...supportCase, version: 2, status: "waiting_on_support", messages: [...supportCase.messages, { id: "50000000-0000-4000-8000-000000000003", author_user_id: supportCase.requester_user_id, author_kind: "customer", body: "Fresh diagnostic context", created_at: "2026-07-17T13:05:00Z" }] };
    request.mockResolvedValueOnce({ items: [supportCase] }).mockResolvedValueOnce(supportCase).mockResolvedValueOnce(replied);
    render(<SupportCenter locale="en" />);

    fireEvent.click(screen.getByRole("button", { name: "Load my cases" }));
    await screen.findByText("Agent runs unavailable");
    fireEvent.click(screen.getByRole("button", { name: /Agent runs unavailable/ }));
    await screen.findByText("Runs stop before scheduling.");
    fireEvent.change(screen.getByLabelText("Add a reply"), { target: { value: "Fresh diagnostic context" } });
    fireEvent.click(screen.getByRole("button", { name: "Append to case" }));

    await waitFor(() => expect(request).toHaveBeenCalledTimes(3));
    expect(request.mock.calls[2]).toEqual([`/v1/support/cases/${supportCase.id}/messages`, expect.objectContaining({ method: "POST", ifMatch: '"1"', contentType: "application/vnd.lites.support-reply.v2+json", body: { request_id: "support-request-00000001", body: "Fresh diagnostic context", expected_case_version: 1 } })]);
  });
});
