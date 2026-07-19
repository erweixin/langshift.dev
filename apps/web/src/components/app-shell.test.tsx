import { cleanup, render } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { dictionary } from "@/i18n/dictionaries";
import { useMissions } from "@/lib/use-product-workspace";
import { AppShell } from "./app-shell";

vi.mock("next/navigation", () => ({ usePathname: () => "/en/today" }));
vi.mock("next/link", () => ({ default: ({ href, children, ...props }: React.AnchorHTMLAttributes<HTMLAnchorElement> & { href: string }) => <a href={href} {...props}>{children}</a> }));
vi.mock("@/lib/demo", () => ({ demoMode: false }));
vi.mock("@/lib/api/client", () => ({ apiRequest: vi.fn(), newIdempotencyKey: () => "app-shell-request-0001", ApiError: class ApiError extends Error {} }));
vi.mock("@/lib/use-product-workspace", () => ({ useMissions: vi.fn() }));

const missions = vi.mocked(useMissions);

afterEach(() => {
  cleanup();
  missions.mockReset();
});

it("keeps polling Mission projection so Coach admission recovers after sign-in", () => {
  missions.mockReturnValue({
    items: [], focus: null, focusState: { mission_id: null, version: 0 }, next_cursor: null,
    roles: [], rubrics: [], roleNames: new Map(), loading: false, error: "", refresh: vi.fn(),
  });
  render(<AppShell locale="en" dictionary={dictionary("en")}><div>Today</div></AppShell>);
  expect(missions).toHaveBeenCalledWith("en", true, true);
});
