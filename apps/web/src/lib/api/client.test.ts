import { afterEach, describe, expect, it, vi } from "vitest";
import { apiRequest } from "./client";

afterEach(() => vi.restoreAllMocks());

describe("apiRequest", () => {
  it("rejects unversioned paths before issuing a request", async () => {
    const request = vi.spyOn(globalThis, "fetch");
    await expect(apiRequest("/missions")).rejects.toThrow("API path must be versioned");
    expect(request).not.toHaveBeenCalled();
  });

  it("blocks offline mutations and never creates false success", async () => {
    vi.spyOn(window.navigator, "onLine", "get").mockReturnValue(false);
    const request = vi.spyOn(globalThis, "fetch");
    await expect(apiRequest("/v1/submissions", { method: "POST", body: { content: "draft" } })).rejects.toMatchObject({ message: "offline_write_blocked", status: 0, requestID: null, name: "ApiError" });
    expect(request).not.toHaveBeenCalled();
  });

  it("sends idempotency, CSRF, and optimistic-concurrency headers", async () => {
    vi.spyOn(window.navigator, "onLine", "get").mockReturnValue(true);
    const request = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({ ok: true }), { status: 200, headers: { "Content-Type": "application/json" } }));
    await apiRequest("/v1/preferences", { method: "PATCH", body: {}, idempotencyKey: "idem-12345678", csrfToken: "csrf", ifMatch: '"7"', contentType: "application/vnd.lites.preferences-update.v2+json" });
    const init = request.mock.calls[0]?.[1];
    const headers = new Headers(init?.headers);
    expect(headers.get("Idempotency-Key")).toBe("idem-12345678");
    expect(headers.get("X-CSRF-Token")).toBe("csrf");
    expect(headers.get("If-Match")).toBe('"7"');
    expect(headers.get("Content-Type")).toBe("application/vnd.lites.preferences-update.v2+json");
    expect(init?.credentials).toBe("include");
  });
});
