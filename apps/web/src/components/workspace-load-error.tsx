"use client";

import { RotateCcw } from "lucide-react";
import type { Locale } from "@/i18n/config";

export function WorkspaceLoadError({
  locale,
  message,
  loading,
  onRetry,
}: {
  locale: Locale;
  message: string;
  loading: boolean;
  onRetry: () => Promise<unknown>;
}) {
  const zh = locale === "zh-CN";
  return (
    <section className="card error-note" role="alert">
      <span>{message}</span>
      <button
        className="button"
        type="button"
        disabled={loading}
        onClick={() => void onRetry()}
      >
        <RotateCcw aria-hidden="true" />
        {loading ? (zh ? "正在重试…" : "Retrying…") : zh ? "重试" : "Retry"}
      </button>
    </section>
  );
}
