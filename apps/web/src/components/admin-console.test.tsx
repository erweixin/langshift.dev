import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AdminConsole } from "./admin-console";
import { apiRequest } from "@/lib/api/client";

vi.mock("@/lib/api/client", () => ({
  ApiError: class ApiError extends Error {
    constructor(message: string, readonly status: number, readonly requestID: string | null) { super(message); }
  },
  apiRequest: vi.fn(),
  newIdempotencyKey: vi.fn(() => "request-00000000-0000-4000-8000-000000000001"),
}));

const request = vi.mocked(apiRequest);

afterEach(() => {
  cleanup();
  request.mockReset();
});

describe("AdminConsole", () => {
  it("reauthenticates the current session without retaining the password", async () => {
    request.mockResolvedValueOnce({ valid_until: "2026-07-17T12:05:00Z" });
    render(<AdminConsole locale="en" />);

    const password = screen.getByLabelText("Current password") as HTMLInputElement;
    fireEvent.change(password, { target: { value: "correct horse battery staple" } });
    fireEvent.click(screen.getByRole("button", { name: "Reauthenticate" }));

    await waitFor(() => expect(request).toHaveBeenCalledTimes(1));
    expect(request).toHaveBeenCalledWith("/v1/auth/reauthentication", expect.objectContaining({
      method: "POST",
      idempotencyKey: "request-00000000-0000-4000-8000-000000000001",
      body: { request_id: "request-00000000-0000-4000-8000-000000000001", password: "correct horse battery staple" },
    }));
    expect(password.value).toBe("");
  });

  it("requires and forwards an explicit audit reason for usage reads", async () => {
    request.mockResolvedValueOnce({ tenant_id: "tenant-1", granted_units: 100, available_units: 80, reserved_units: 5, settled_units: 15, active_seats: 3, seat_limit: 10, as_of: "2026-07-17T12:00:00Z" });
    render(<AdminConsole locale="en" />);

    const load = screen.getByRole("button", { name: "Load usage" }) as HTMLButtonElement;
    expect(load.disabled).toBe(true);
    fireEvent.change(screen.getByLabelText("Reason for this access"), { target: { value: "Quarterly renewal review" } });
    expect(load.disabled).toBe(false);
    fireEvent.click(load);

    await waitFor(() => expect(request).toHaveBeenCalledWith("/v1/admin/usage", {
      accept: "application/vnd.lites.usage-snapshot.v2+json",
      auditReason: "Quarterly renewal review",
    }));
    expect(screen.getByText("80")).toBeTruthy();
  });

  it("submits contract proposals with the governed media type and complete terms", async () => {
    request.mockResolvedValueOnce({ id: "proposal-1", version: 1, status: "pending", proposal_hash: "a".repeat(64), target_id: "", target_version: 0, approval_count: 0, updated_at: "2026-07-17T12:00:00Z" });
    render(<AdminConsole locale="en" />);

    fireEvent.change(screen.getByLabelText("Contract number"), { target: { value: "ENT-2026-001" } });
    fireEvent.change(screen.getAllByLabelText("Starts")[1]!, { target: { value: "2026-08-01T09:00" } });
    fireEvent.change(screen.getAllByLabelText("Ends")[1]!, { target: { value: "2027-08-01T09:00" } });
    fireEvent.change(screen.getByLabelText("Seat limit"), { target: { value: "250" } });
    fireEvent.change(screen.getByLabelText("Region"), { target: { value: "ap-southeast-1" } });
    fireEvent.change(screen.getAllByLabelText("Change reason")[0]!, { target: { value: "Annual enterprise agreement" } });
    fireEvent.click(screen.getByRole("button", { name: "Seal proposal" }));

    await waitFor(() => expect(request).toHaveBeenCalledTimes(1));
    const options = request.mock.calls[0]?.[1];
    expect(request.mock.calls[0]?.[0]).toBe("/v1/admin/contracts");
    expect(options).toMatchObject({ method: "POST", accept: "application/vnd.lites.admin-control-resource.v2+json", contentType: "application/vnd.lites.contract-proposal.v2+json" });
    expect(options?.body).toMatchObject({ action: "create", target_contract_id: "", target_version: 0, contract_number: "ENT-2026-001", seat_limit: 250, region: "ap-southeast-1", license_kind: "enterprise_cloud", reason: "Annual enterprise agreement" });
  });

  it("invites a member and enrolls the exact user set with cohort CAS", async () => {
    request
      .mockResolvedValueOnce({ id: "invitation-1", version: 1, status: "pending", updated_at: "2026-07-17T12:00:00Z" })
      .mockResolvedValueOnce({ id: "cohort-1", version: 4, status: "active", updated_at: "2026-07-17T12:01:00Z" });
    render(<AdminConsole locale="en" />);

    const invitation = screen.getByRole("heading", { name: "Invite one member" }).closest("form")!;
    fireEvent.change(invitation.querySelector('[name="email"]')!, { target: { value: "learner@example.com" } });
    fireEvent.change(invitation.querySelector('[name="role"]')!, { target: { value: "reviewer" } });
    fireEvent.click(screen.getByRole("button", { name: "Send invitation" }));
    await waitFor(() => expect(request).toHaveBeenCalledTimes(1));
    expect(request).toHaveBeenNthCalledWith(1, "/v1/invitations", expect.objectContaining({ body: expect.objectContaining({ email: "learner@example.com", role: "reviewer", expires_in_days: 7 }) }));

    const enrollment = screen.getByRole("heading", { name: "Enroll members in a cohort" }).closest("form")!;
    fireEvent.change(enrollment.querySelector('[name="cohort_id"]')!, { target: { value: "71000000-0000-4000-8000-000000000001" } });
    fireEvent.change(enrollment.querySelector('[name="cohort_version"]')!, { target: { value: "3" } });
    fireEvent.change(enrollment.querySelector('[name="user_ids"]')!, { target: { value: "71000000-0000-4000-8000-000000000002\n71000000-0000-4000-8000-000000000003" } });
    fireEvent.click(screen.getByRole("button", { name: "Enroll members" }));
    await waitFor(() => expect(request).toHaveBeenCalledTimes(2));
    expect(request).toHaveBeenNthCalledWith(2, "/v1/admin/cohorts/71000000-0000-4000-8000-000000000001/enrollments", expect.objectContaining({
      ifMatch: '"3"',
      contentType: "application/vnd.lites.cohort-enroll.v2+json",
      body: expect.objectContaining({ user_ids: ["71000000-0000-4000-8000-000000000002", "71000000-0000-4000-8000-000000000003"], expected_cohort_version: 3 }),
    }));
  });
});
