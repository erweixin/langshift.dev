"use client";

import { useState } from "react";
import { AlertCircle, CheckCircle2, Download, LoaderCircle } from "lucide-react";
import { apiRequest, newIdempotencyKey } from "@/lib/api/client";
import type { Locale } from "@/i18n/config";

type State = "idle" | "requesting" | "processing" | "ready" | "error";
type ExportSnapshot = { projectID: string; projectVersion: number; workspaceBindingVersion: number; workspaceRevision: string; artifactRevisionIDs: string[] };

export function PortfolioExport({ locale, snapshot }: { locale: Locale; snapshot?: ExportSnapshot }) {
  const zh = locale === "zh-CN"; const [format, setFormat] = useState("pdf"); const [state, setState] = useState<State>("idle"); const [message, setMessage] = useState("");
  async function start() {
    if (!navigator.onLine) { setState("error"); setMessage(zh ? "离线时无法请求导出。" : "An export cannot be requested offline."); return; }
    setState("requesting"); setMessage("");
    try {
      if (process.env.NEXT_PUBLIC_LITES_DEMO_MODE === "true") { await new Promise((resolve) => setTimeout(resolve, 450)); setState("processing"); await new Promise((resolve) => setTimeout(resolve, 750)); setState("ready"); return; }
      if (!snapshot) throw new Error("export_snapshot_required");
      await apiRequest("/v1/portfolio-exports", { method: "POST", contentType: "application/vnd.lites.portfolio-export.v2+json", idempotencyKey: newIdempotencyKey(), ifMatch: `"${snapshot.projectVersion}"`, body: { request_id: newIdempotencyKey(), project_id: snapshot.projectID, expected_project_version: snapshot.projectVersion, expected_workspace_binding_version: snapshot.workspaceBindingVersion, workspace_revision: snapshot.workspaceRevision, artifact_revision_ids: snapshot.artifactRevisionIDs, format } });
      setState("processing");
    } catch (cause) { setState("error"); setMessage(cause instanceof Error ? cause.message : "export_failed"); }
  }
  return <section className="export-panel card"><div><p className="section-label">{zh ? "作品集导出" : "Portfolio export"}</p><h2>{zh ? "生成可验证的项目包" : "Generate a verifiable project package"}</h2><p>{snapshot || process.env.NEXT_PUBLIC_LITES_DEMO_MODE === "true" ? (zh ? "包含已选择的 Artifact、版本、证据依据和评审来源。" : "Includes selected artifacts, revisions, evidence basis, and review provenance.") : (zh ? "完成项目、Workspace 和至少一个 Artifact revision 后才能生成精确导出。" : "Complete a project, Workspace, and at least one Artifact revision before requesting an exact export.")}</p></div><div className="export-controls"><label className="field"><span>Format</span><select name="export-format" value={format} onChange={(event) => setFormat(event.target.value)}><option value="pdf">PDF</option><option value="html">HTML</option><option value="zip">ZIP archive</option></select></label>{state === "ready" ? <button className="button primary"><CheckCircle2 />{zh ? "导出已完成" : "Verified export ready"}</button> : <button className="button primary" type="button" onClick={start} disabled={!snapshot && process.env.NEXT_PUBLIC_LITES_DEMO_MODE !== "true" || state === "requesting" || state === "processing"}>{state === "idle" || state === "error" ? <><Download />{zh ? "请求导出" : "Request export"}</> : <><LoaderCircle className="spinner-icon" />{state === "requesting" ? (zh ? "正在确认…" : "Confirming…") : (zh ? "正在构建…" : "Building…")}</>}</button>}{state === "processing" && <p className="pending-note" role="status" aria-live="polite">{zh ? "请求已确认。只有可信收据验证后才会提供下载。" : "Request confirmed. Download appears only after a trusted receipt is verified."}</p>}{state === "error" && <p className="error-note" role="alert"><AlertCircle />{message}</p>}</div></section>;
}
