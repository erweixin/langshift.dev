import { afterEach, describe, expect, it, vi } from "vitest";
import { apiDownload, apiRequest, csrfTokenFromCookie } from "./client";

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
    await apiRequest("/v1/preferences", { method: "PATCH", body: {}, idempotencyKey: "idem-12345678", csrfToken: "csrf", ifMatch: '"7"', contentType: "application/vnd.lites.preferences-update.v2+json", auditReason: "Quarterly review" });
    const init = request.mock.calls[0]?.[1];
    const headers = new Headers(init?.headers);
    expect(headers.get("Idempotency-Key")).toBe("idem-12345678");
    expect(headers.get("X-CSRF-Token")).toBe("csrf");
    expect(headers.get("If-Match")).toBe('"7"');
    expect(headers.get("Content-Type")).toBe("application/vnd.lites.preferences-update.v2+json");
    expect(headers.get("X-Audit-Reason")).toBe("Quarterly review");
    expect(init?.credentials).toBe("include");
  });

  it("reads only the dedicated CSRF cookie and decodes it safely", () => {
    expect(csrfTokenFromCookie("theme=dark; __Host-lites_csrf=token%2Evalue; session=secret")).toBe("token.value");
    expect(csrfTokenFromCookie("__Host-lites_csrf=%E0%A4%A")).toBeUndefined();
    expect(csrfTokenFromCookie("session=secret")).toBeUndefined();
  });

  it("downloads a no-store audit artifact with a bound reason", async () => {
    const request = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response("{\"id\":1}\n", { status: 200, headers: { "Content-Type": "application/x-ndjson", "Content-Disposition": 'attachment; filename="lites-audit.jsonl"', Digest: "SHA-256=YWJj" } }));
    const result = await apiDownload("/v1/admin/audit-exports/78000000-0000-4000-8000-000000000060", { accept: "application/x-ndjson", auditReason: "External auditor delivery" });
    const init = request.mock.calls[0]?.[1];
    const headers = new Headers(init?.headers);
    expect(headers.get("X-Audit-Reason")).toBe("External auditor delivery");
    expect(init?.credentials).toBe("include");
    expect(init?.cache).toBe("no-store");
    expect(result.filename).toBe("lites-audit.jsonl");
    expect(result.digest).toBe("SHA-256=YWJj");
    expect(await result.blob.text()).toBe("{\"id\":1}\n");
  });
});
