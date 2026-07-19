"use client";

import { useEffect, useState } from "react";

type ConnectionState = "idle" | "connecting" | "live" | "retrying";

export function useRealtime(enabled = true) {
  const [state, setState] = useState<ConnectionState>("idle");
  useEffect(() => {
    if (!enabled || typeof EventSource === "undefined") return;
    let source: EventSource | undefined;
    let retry: number | undefined;
    let stopped = false;
    const connect = () => {
      if (stopped || !navigator.onLine) return;
      setState((current) => current === "idle" ? "connecting" : "retrying");
      const cursor = sessionStorage.getItem("lites:last-seen-seq");
      source = new EventSource(`/api/v1/realtime${cursor ? `?after_seq=${encodeURIComponent(cursor)}` : ""}`, { withCredentials: true });
      source.onopen = () => setState("live");
      source.addEventListener("event", (event) => {
        if (event.lastEventId) sessionStorage.setItem("lites:last-seen-seq", event.lastEventId);
        window.dispatchEvent(new CustomEvent("lites:event", { detail: event.data }));
      });
      source.onerror = () => {
        source?.close();
        setState("retrying");
        retry = window.setTimeout(connect, 3000);
      };
    };
    connect();
    window.addEventListener("online", connect);
    return () => {
      stopped = true;
      source?.close();
      if (retry) window.clearTimeout(retry);
      window.removeEventListener("online", connect);
    };
  }, [enabled]);
  return state;
}
