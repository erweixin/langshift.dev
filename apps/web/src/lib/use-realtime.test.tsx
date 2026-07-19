import { act, cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { useRealtime } from "./use-realtime";

class TestEventSource {
  static instances: TestEventSource[] = [];
  readonly url: string;
  readonly withCredentials: boolean;
  onopen: ((event: Event) => void) | null = null;
  onerror: ((event: Event) => void) | null = null;
  closed = false;
  private listeners = new Map<string, EventListenerOrEventListenerObject[]>();

  constructor(url: string | URL, init?: EventSourceInit) {
    this.url = String(url);
    this.withCredentials = init?.withCredentials ?? false;
    TestEventSource.instances.push(this);
  }

  addEventListener(type: string, listener: EventListenerOrEventListenerObject) {
    this.listeners.set(type, [...(this.listeners.get(type) ?? []), listener]);
  }

  removeEventListener() {}

  dispatch(type: string, event: Event) {
    for (const listener of this.listeners.get(type) ?? []) {
      if (typeof listener === "function") listener(event);
      else listener.handleEvent(event);
    }
  }

  close() {
    this.closed = true;
  }
}

function RealtimeState() {
  return <div>{useRealtime()}</div>;
}

afterEach(() => {
  cleanup();
  sessionStorage.clear();
  TestEventSource.instances = [];
  vi.unstubAllGlobals();
});

it("reconnects from the public after_seq contract and consumes named event frames", async () => {
  vi.stubGlobal("EventSource", TestEventSource);
  sessionStorage.setItem("lites:last-seen-seq", "42");
  const received: string[] = [];
  window.addEventListener("lites:event", (event: Event) => {
    received.push((event as CustomEvent<string>).detail);
  }, { once: true });

  render(<RealtimeState />);

  expect(TestEventSource.instances).toHaveLength(1);
  const source = TestEventSource.instances[0];
  if (!source) throw new Error("EventSource was not created");
  expect(source.url).toBe("/api/v1/realtime?after_seq=42");
  expect(source.withCredentials).toBe(true);

  act(() => source.onopen?.(new Event("open")));
  expect(await screen.findByText("live")).not.toBeNull();

  act(() => source.dispatch("event", new MessageEvent("event", { data: "{\"event_type\":\"ReviewCompleted\"}", lastEventId: "43" })));
  expect(sessionStorage.getItem("lites:last-seen-seq")).toBe("43");
  expect(received).toEqual(["{\"event_type\":\"ReviewCompleted\"}"]);

  cleanup();
  expect(source.closed).toBe(true);
});
