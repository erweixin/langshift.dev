import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { apiRequest } from "@/lib/api/client";
import { useMissions } from "./use-product-workspace";

vi.mock("@/lib/api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api/client")>();
  return { ...actual, apiRequest: vi.fn() };
});

const request = vi.mocked(apiRequest);
const mission = {
  id: "00000000-0000-4000-8000-000000000001",
  version: 1,
  status: "active" as const,
  source_role_profile_id: "00000000-0000-4000-8000-000000000002",
  target_role_profile_id: "00000000-0000-4000-8000-000000000003",
  current_route_revision_id: "00000000-0000-4000-8000-000000000004",
  focused: true,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

afterEach(() => {
  vi.useRealTimers();
  request.mockReset();
});

it("keeps a successful Mission projection when the display catalog is temporarily unavailable", async () => {
  request.mockImplementation(async (path) => {
    if (path === "/v1/missions") return { items: [mission], focus: { mission_id: mission.id, version: 1 }, next_cursor: null };
    throw new Error("catalog temporarily unavailable");
  });

  const { result } = renderHook(() => useMissions("en"));
  await waitFor(() => expect(result.current.focus?.id).toBe(mission.id));
  expect(result.current.error).toBe("Some workspace data is temporarily unavailable; it is safe to retry.");
});

it("continues polling after a failed background refresh until the Mission projection is ready", async () => {
  vi.useFakeTimers();
  let missionReads = 0;
  request.mockImplementation(async (path) => {
    if (path.includes("/v1/catalog/roles")) return { items: [], rubrics: [] };
    missionReads += 1;
    if (missionReads === 1) return { items: [], focus: { mission_id: null, version: 0 }, next_cursor: null };
    if (missionReads === 2) throw new Error("transient Mission read failure");
    return { items: [mission], focus: { mission_id: mission.id, version: 1 }, next_cursor: null };
  });

  const { result } = renderHook(() => useMissions("en", true, true));
  await act(async () => { await Promise.resolve(); });
  expect(result.current.focus).toBeNull();
  await act(async () => { await vi.advanceTimersByTimeAsync(2_100); });
  expect(result.current.focus).toBeNull();
  await act(async () => { await vi.advanceTimersByTimeAsync(2_100); });
  expect(result.current.focus?.id).toBe(mission.id);
  expect(missionReads).toBe(3);
});
