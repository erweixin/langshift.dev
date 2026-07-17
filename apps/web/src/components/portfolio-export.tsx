"use client";

import { useState } from "react";
import { AlertCircle, CheckCircle2, Download, LoaderCircle } from "lucide-react";
import { apiRequest, newIdempotencyKey } from "@/lib/api/client";
import type { Locale } from "@/i18n/config";

type State = "idle" | "requesting" | "processing" | "ready" | "error";

export function PortfolioExport({ locale }: { locale: Locale }) {
  const zh = locale === "zh-CN"; const [format, setFormat] = useState("pdf"); const [state, setState] = useState<State>("idle"); const [message, setMessage] = useState("");
  async function start() {
    if (!navigator.onLine) { setState("error"); setMessage(zh ? "离线时无法请求导出。" : "An export cannot be requested offline."); return; }
    setState("requesting"); setMessage("");
    try {
      if (process.env.NEXT_PUBLIC_LITES_DEMO_MODE === "true") { await new Promise((resolve) => setTimeout(resolve, 450)); setState("processing"); await new Promise((resolve) => setTimeout(resolve, 750)); setState("ready"); return; }
      await apiRequest("/v1/portfolio-exports", { method: "POST", idempotencyKey: newIdempotencyKey(), body: { request_id: newIdempotencyKey(), project_id: "project-code", project_version: 1, workspace_revision: "workspace-v1", artifact_revisions: ["artifact-run-service-v1"], evidence_ids: ["ev-1", "ev-3"], format } });
      setState("processing");
    } catch (cause) { setState("error"); setMessage(cause instanceof Error ? cause.message : "export_failed"); }
  }
  return <section className="export-panel card"><div><p className="section-label">{zh ? "作品集导出" : "Portfolio export"}</p><h2>{zh ? "生成可验证的项目包" : "Generate a verifiable project package"}</h2><p>{zh ? "包含已选择的 Artifact、版本、证据依据和评审来源。" : "Includes selected artifacts, revisions, evidence basis, and review provenance."}</p></div><div className="export-controls"><label className="field"><span>Format</span><select name="export-format" value={format} onChange={(event) => setFormat(event.target.value)}><option value="pdf">PDF</option><option value="html">HTML</option><option value="zip">ZIP archive</option></select></label>{state === "ready" ? <button className="button primary"><CheckCircle2 />{zh ? "下载已验证导出" : "Download verified export"}</button> : <button className="button primary" type="button" onClick={start} disabled={state === "requesting" || state === "processing"}>{state === "idle" || state === "error" ? <><Download />{zh ? "请求导出" : "Request export"}</> : <><LoaderCircle className="spinner-icon" />{state === "requesting" ? (zh ? "正在确认…" : "Confirming…") : (zh ? "正在构建…" : "Building…")}</>}</button>}{state === "processing" && <p className="pending-note" role="status" aria-live="polite">{zh ? "请求已确认。只有可信收据验证后才会提供下载。" : "Request confirmed. Download appears only after a trusted receipt is verified."}</p>}{state === "error" && <p className="error-note" role="alert"><AlertCircle />{message}</p>}</div></section>;
}
