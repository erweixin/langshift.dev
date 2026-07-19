import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { apiDownload, apiRequest } from "@/lib/api/client";
import { SettingsPanel } from "./settings-panel";

vi.mock("@/lib/api/client", () => ({
  apiDownload: vi.fn(),
  apiRequest: vi.fn(),
  newIdempotencyKey: vi.fn(() => "settings-request-00000001"),
}));

const request = vi.mocked(apiRequest);
const download = vi.mocked(apiDownload);

afterEach(() => {
  cleanup();
  request.mockReset();
  download.mockReset();
  vi.restoreAllMocks();
});

describe("SettingsPanel account export", () => {
  it("reauthenticates for request and download, clears passwords, and exposes only supported BYOK providers", async () => {
    request
      .mockResolvedValueOnce({ version: 1, normalized_email: "owner@example.test", display_name: "Owner" })
      .mockResolvedValueOnce({ items: [{ name: "Personal", active: true }] })
      .mockResolvedValueOnce({ version: 1, timezone: "UTC", coach_preferences: { tone: "socratic" } })
      .mockResolvedValueOnce({ items: [] })
      .mockResolvedValueOnce({ version: 1, enabled: true, retention_days: 365 })
      .mockResolvedValueOnce({ valid_until: "2026-07-18T12:05:00Z" })
      .mockResolvedValueOnce({ id: "10000000-0000-4000-8000-000000000777" })
      .mockResolvedValueOnce({ id: "10000000-0000-4000-8000-000000000777", version: 2, status: "ready", expires_at: "2026-07-19T12:00:00Z", failure_code: null })
      .mockResolvedValueOnce({ valid_until: "2026-07-18T12:06:00Z" })
      .mockResolvedValueOnce({ valid_until: "2026-07-18T12:07:00Z" })
      .mockResolvedValueOnce({ id: "10000000-0000-4000-8000-000000000778", version: 1, status: "requested" });
    download.mockResolvedValueOnce({ blob: new Blob(["archive"]), filename: "lites-account-export.zip", digest: null });
    Object.defineProperty(URL, "createObjectURL", { configurable: true, value: vi.fn(() => "blob:account-export") });
    Object.defineProperty(URL, "revokeObjectURL", { configurable: true, value: vi.fn() });
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => undefined);

    const { container } = render(<SettingsPanel locale="en" />);
    await screen.findByText("owner@example.test · Personal");
    expect(screen.getByLabelText("Provider").querySelector('option[value="google"]')).toBeNull();

    const password = container.querySelector<HTMLInputElement>('[name="export-current-password"]')!;
    const requestButton = screen.getByRole("button", { name: "Request export" }) as HTMLButtonElement;
    expect(requestButton.disabled).toBe(true);
    fireEvent.change(password, { target: { value: "correct horse battery staple" } });
    fireEvent.click(requestButton);

    await screen.findByRole("button", { name: "Download archive" });
    expect(password.value).toBe("");
    expect(request).toHaveBeenNthCalledWith(6, "/v1/auth/reauthentication", expect.objectContaining({
      method: "POST",
      body: { request_id: "settings-request-00000001", password: "correct horse battery staple" },
    }));
    expect(request).toHaveBeenNthCalledWith(7, "/v1/account/export-requests", expect.objectContaining({
      method: "POST",
      body: expect.objectContaining({ format: "zip", scope: ["account", "missions", "evidence", "projects", "conversations", "memory", "audit"] }),
    }));

    const downloadButton = screen.getByRole("button", { name: "Download archive" }) as HTMLButtonElement;
    expect(downloadButton.disabled).toBe(true);
    fireEvent.change(password, { target: { value: "second reauthentication" } });
    fireEvent.click(downloadButton);
    await waitFor(() => expect(download).toHaveBeenCalledOnce());
    expect(request).toHaveBeenNthCalledWith(9, "/v1/auth/reauthentication", expect.objectContaining({
      body: { request_id: "settings-request-00000001", password: "second reauthentication" },
    }));
    expect(download).toHaveBeenCalledWith("/v1/account/export-requests/10000000-0000-4000-8000-000000000777/download", { accept: "application/zip" });
    expect(password.value).toBe("");

    const deletionPassword = container.querySelector<HTMLInputElement>('[name="delete-current-password"]')!;
    fireEvent.change(deletionPassword, { target: { value: "delete reauthentication" } });
    fireEvent.change(screen.getByLabelText("Confirmation phrase"), { target: { value: "DELETE MY ACCOUNT" } });
    fireEvent.click(screen.getByRole("button", { name: "Request erasure" }));
    await screen.findByRole("button", { name: "Request created" });
    expect(request).toHaveBeenNthCalledWith(10, "/v1/auth/reauthentication", expect.objectContaining({
      body: { request_id: "settings-request-00000001", password: "delete reauthentication" },
    }));
    expect(request).toHaveBeenNthCalledWith(11, "/v1/account/erasure-requests", expect.objectContaining({
      method: "POST",
      body: { request_id: "settings-request-00000001", confirmation: "DELETE MY ACCOUNT", reason: null },
    }));
    expect(deletionPassword.value).toBe("");
  });
});
