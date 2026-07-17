"use client";

import { useEffect, useState } from "react";
import { AlertTriangle, CheckCircle2, CircleHelp, RefreshCw } from "lucide-react";
import type { Locale } from "@/i18n/config";

type ComponentState = "operational" | "degraded" | "major_outage" | "maintenance" | "unknown";
type StatusDocument = { schema_version: 1; overall: ComponentState; generated_at: string; valid_until: string; components: { id: string; name: string; state: ComponentState; message?: string }[]; incidents: { id: string; title: string; state: "investigating" | "identified" | "monitoring" | "resolved"; started_at: string; updated_at: string; message: string }[] };

async function readCurrentStatus(signal?: AbortSignal) {
  const response = await fetch("/api/v1/public/status", { cache: "no-store", signal, headers: { Accept: "application/vnd.lites.public-status.v1+json" } });
  if (!response.ok) throw new Error("status unavailable");
  const result = await response.json() as StatusDocument;
  if (result.schema_version !== 1 || !Array.isArray(result.components) || !Array.isArray(result.incidents) || !Date.parse(result.generated_at) || !Date.parse(result.valid_until) || Date.parse(result.valid_until) <= Date.now()) throw new Error("status stale");
  return result;
}

function stateLabel(state: ComponentState, zh: boolean) {
  const labels = zh ? { operational: "正常", degraded: "性能下降", major_outage: "重大中断", maintenance: "维护中", unknown: "未知" } : { operational: "Operational", degraded: "Degraded", major_outage: "Major outage", maintenance: "Maintenance", unknown: "Unknown" };
  return labels[state];
}

export function StatusPage({ locale }: { locale: Locale }) {
  const zh = locale === "zh-CN";
  const [document, setDocument] = useState<StatusDocument | null>(null);
  const [state, setState] = useState<"loading" | "ready" | "unknown">("loading");
  async function refresh(signal?: AbortSignal) {
    setState("loading");
    try {
      const result = await readCurrentStatus(signal);
      setDocument(result);
      setState("ready");
    } catch (error) {
      if ((error as Error).name === "AbortError") return;
      setDocument(null);
      setState("unknown");
    }
  }
  useEffect(() => {
    const controller = new AbortController();
    void readCurrentStatus(controller.signal).then((result) => { setDocument(result); setState("ready"); }).catch((error: Error) => { if (error.name !== "AbortError") { setDocument(null); setState("unknown"); } });
    return () => controller.abort();
  }, []);
  const overall = state === "ready" && document ? document.overall : "unknown";
  const Icon = overall === "operational" ? CheckCircle2 : overall === "unknown" ? CircleHelp : AlertTriangle;
  return <div className="status-page"><header className={`status-hero state-${overall}`}><Icon aria-hidden="true" /><div><p className="eyebrow">Lites Cloud Status</p><h1>{stateLabel(overall, zh)}</h1><p>{overall === "unknown" ? (zh ? "无法取得仍在有效期内、可验证的状态快照。为避免错误报绿，当前状态显示为未知。" : "No current verifiable status snapshot is available. To avoid false green, status is shown as unknown.") : (zh ? "状态来自带有效期的生产监控快照。" : "Status comes from an expiring production monitoring snapshot.")}</p></div><button className="button" type="button" onClick={() => refresh()} disabled={state === "loading"}><RefreshCw aria-hidden="true" />{state === "loading" ? (zh ? "正在检查…" : "Checking…") : (zh ? "刷新" : "Refresh")}</button></header>
    {document && state === "ready" ? <><section className="status-components"><div className="public-section-head"><p className="section-label">{zh ? "组件" : "Components"}</p><h2>{zh ? "当前服务状态" : "Current service health"}</h2></div>{document.components.map((component) => <article key={component.id}><span className={`status-dot-large state-${component.state}`} /><div><strong>{component.name}</strong>{component.message && <p>{component.message}</p>}</div><span>{stateLabel(component.state, zh)}</span></article>)}</section><section className="status-incidents"><div className="public-section-head"><p className="section-label">{zh ? "事件" : "Incidents"}</p><h2>{zh ? "正在处理与最近事件" : "Active and recent incidents"}</h2></div>{document.incidents.length === 0 ? <p className="card muted">{zh ? "当前快照没有活动事件。" : "No active incidents in the current snapshot."}</p> : document.incidents.map((incident) => <article className="card" key={incident.id}><span className="chip">{incident.state}</span><h3>{incident.title}</h3><p>{incident.message}</p><time dateTime={incident.updated_at}>{new Date(incident.updated_at).toLocaleString(locale)}</time></article>)}</section><p className="status-freshness">{zh ? "快照生成" : "Snapshot generated"}: <time dateTime={document.generated_at}>{new Date(document.generated_at).toLocaleString(locale)}</time> · {zh ? "有效至" : "valid until"}: <time dateTime={document.valid_until}>{new Date(document.valid_until).toLocaleString(locale)}</time></p></> : <section className="status-unknown card"><CircleHelp aria-hidden="true" /><div><h2>{zh ? "状态数据不可用" : "Status data unavailable"}</h2><p>{zh ? "这不表示服务正常或中断。请稍后刷新；若你的工作受影响，请通过 Support Center 创建工单。" : "This does not mean the service is healthy or down. Refresh later; if your work is affected, open a case in Support Center."}</p></div></section>}
  </div>;
}
