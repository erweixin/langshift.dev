import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { EvidenceSharing } from "./evidence-sharing";
import { apiRequest } from "@/lib/api/client";

vi.mock("@/lib/api/client", () => ({
  ApiError: class ApiError extends Error {
    constructor(message: string, readonly status: number, readonly requestID: string | null) { super(message); }
  },
  apiRequest: vi.fn(),
  newIdempotencyKey: vi.fn(() => "share-request-000000000001"),
}));

const request = vi.mocked(apiRequest);

afterEach(() => {
  cleanup();
  request.mockReset();
});

describe("EvidenceSharing", () => {
  it("creates an exact-revision grant and revokes it with CAS", async () => {
    const grantID = "71000000-0000-4000-8000-000000000001";
    request
      .mockResolvedValueOnce({ id: grantID, version: 1, status: "active", updated_at: "2026-07-17T12:00:00Z" })
      .mockResolvedValueOnce({ id: grantID, version: 2, status: "revoked", updated_at: "2026-07-17T12:01:00Z" });
    render(<EvidenceSharing locale="en" />);

    fireEvent.change(screen.getByLabelText("Grantee user ID"), { target: { value: "71000000-0000-4000-8000-000000000002" } });
    fireEvent.change(screen.getByLabelText("Resource ID"), { target: { value: "71000000-0000-4000-8000-000000000003" } });
    fireEvent.change(screen.getByLabelText("Exact resource revision"), { target: { value: "sha256:exact-revision" } });
    fireEvent.click(screen.getByLabelText("review"));
    fireEvent.click(screen.getByRole("button", { name: "Create explicit share" }));

    await waitFor(() => expect(request).toHaveBeenCalledTimes(1));
    expect(request).toHaveBeenNthCalledWith(1, "/v1/share-grants", expect.objectContaining({
      method: "POST",
      contentType: "application/vnd.lites.share-grant-create.v2+json",
      body: expect.objectContaining({ resource_kind: "evidence", resource_revision: "sha256:exact-revision", scope: ["read", "review"] }),
    }));
    expect((screen.getByLabelText("Share grant ID") as HTMLInputElement).value).toBe(grantID);
    expect((screen.getByLabelText("Current grant version") as HTMLInputElement).value).toBe("1");

    fireEvent.change(screen.getByLabelText("Revocation reason"), { target: { value: "Review engagement ended" } });
    fireEvent.click(screen.getByRole("button", { name: "Revoke and block new reads" }));
    await waitFor(() => expect(request).toHaveBeenCalledTimes(2));
    expect(request).toHaveBeenNthCalledWith(2, `/v1/share-grants/${grantID}`, expect.objectContaining({
      method: "DELETE",
      ifMatch: '"1"',
      contentType: "application/vnd.lites.share-grant-revoke.v2+json",
      body: expect.objectContaining({ reason: "Review engagement ended", expected_grant_version: 1 }),
    }));
    expect(await screen.findByText("Share revoked; the service immediately rejects subsequent new reads.")).toBeTruthy();
  });
});
