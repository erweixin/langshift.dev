import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { StatusPage } from "./status-page";
import { TrustCenter } from "./trust-center";
import { LegalNotice } from "./legal-notice";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("public trust surfaces", () => {
  it("publishes the network-source offer and legal boundaries in both locales", () => {
    const { rerender } = render(<LegalNotice locale="en" />);
    const source = screen.getByRole("link", { name: /Get corresponding source/ });
    expect(source.getAttribute("href")).toBe("https://github.com/erweixin/langshift.dev");
    expect(screen.getByRole("heading", { name: "No warranty" })).toBeTruthy();
    rerender(<LegalNotice locale="zh-CN" />);
    expect(screen.getByRole("link", { name: /获取对应源代码/ })).toBeTruthy();
    expect(screen.getByRole("heading", { name: "免责声明" })).toBeTruthy();
  });

  it("labels unearned independent certifications instead of implying them", () => {
    render(<TrustCenter locale="en" />);
    expect(screen.getByText("SOC 2 Type II")).toBeTruthy();
    expect(screen.getAllByText("Not claimed")).toHaveLength(2);
    expect(screen.getByText("Required before GA")).toBeTruthy();
  });

  it("renders a current expiring status snapshot", async () => {
    const generated = new Date(Date.now() - 30_000).toISOString();
    const valid = new Date(Date.now() + 120_000).toISOString();
    vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({ schema_version: 1, overall: "operational", generated_at: generated, valid_until: valid, components: [{ id: "api", name: "API", state: "operational" }], incidents: [] }), { status: 200 }));
    render(<StatusPage locale="en" />);
    await waitFor(() => expect(screen.getAllByText("Operational").length).toBeGreaterThan(0));
    expect(screen.getByText("API")).toBeTruthy();
    expect(screen.getByText("No active incidents in the current snapshot.")).toBeTruthy();
  });

  it("fails closed to unknown when the status snapshot is expired", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({ schema_version: 1, overall: "operational", generated_at: "2020-01-01T00:00:00Z", valid_until: "2020-01-01T00:01:00Z", components: [], incidents: [] }), { status: 200 }));
    render(<StatusPage locale="en" />);
    await screen.findByRole("heading", { name: "Unknown" });
    expect(screen.getByText(/avoid false green/)).toBeTruthy();
    expect(screen.queryByText("No active incidents in the current snapshot.")).toBeNull();
  });
});
