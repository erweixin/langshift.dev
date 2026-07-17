"use client";

import { SWRConfig } from "swr";
import type { Dictionary } from "@/i18n/dictionaries";
import { OfflineBanner } from "./offline-banner";
import { ServiceWorkerRegistration } from "./service-worker-registration";
import { useRealtime } from "@/lib/use-realtime";

function RealtimeBridge() {
  useRealtime(process.env.NEXT_PUBLIC_LITES_DEMO_MODE !== "true");
  return null;
}

export function Providers({ dictionary, children }: { dictionary: Dictionary; children: React.ReactNode }) {
  return (
    <SWRConfig value={{ revalidateOnFocus: true, shouldRetryOnError: true, errorRetryCount: 3 }}>
      <ServiceWorkerRegistration />
      <RealtimeBridge />
      <OfflineBanner dictionary={dictionary} />
      {children}
    </SWRConfig>
  );
}
