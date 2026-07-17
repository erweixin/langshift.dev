"use client";

import { useEffect, useRef, useState } from "react";
import { WifiOff } from "lucide-react";
import type { Dictionary } from "@/i18n/dictionaries";

export function OfflineBanner({ dictionary }: { dictionary: Dictionary }) {
  const [online, setOnline] = useState(true);
  const [restored, setRestored] = useState(false);
  const wasOffline = useRef(false);
  useEffect(() => {
    const sync = () => {
      const next = navigator.onLine;
      if (next && wasOffline.current) {
        setRestored(true);
        window.setTimeout(() => setRestored(false), 3500);
      }
      wasOffline.current = !next;
      setOnline(next);
    };
    sync();
    window.addEventListener("online", sync);
    window.addEventListener("offline", sync);
    return () => {
      window.removeEventListener("online", sync);
      window.removeEventListener("offline", sync);
    };
  }, []);
  if (online && !restored) return null;
  return (
    <div className={`network-banner ${online ? "restored" : "offline"}`} role="status" aria-live="polite" data-testid="network-banner">
      <WifiOff size={16} aria-hidden="true" />
      <span>{online ? dictionary.reconnecting : dictionary.offline}</span>
    </div>
  );
}
