"use client";

import Link from "next/link";
import { Download, FileText, ShieldCheck } from "lucide-react";
import { useState } from "react";
import type { Locale } from "@/i18n/config";
import { demoMode } from "@/lib/demo";
import { demoEvidence, type EvidenceLevel } from "@/lib/model";
import { useProductWorkspace, type Evidence } from "@/lib/use-product-workspace";
import { EvidenceSharing } from "./evidence-sharing";
import { LevelBadge } from "./level-badge";

const levels = new Set<EvidenceLevel>(["inferred", "user_confirmed", "demonstrated", "applied", "reviewer_verified"]);

function evidencePresentation(item: Evidence, locale: Locale) {
  const content = item.content && typeof item.content === "object" ? item.content as Record<string, unknown> : {};
  const title = typeof content.title === "string" ? content.title : typeof content.statement === "string" ? content.statement : item.evidence_type.replaceAll("_", " ");
  const value = typeof content.level === "string" && levels.has(content.level as EvidenceLevel) ? content.level as EvidenceLevel : null;
  return { title, level: value, date: new Intl.DateTimeFormat(locale, { dateStyle: "medium" }).format(new Date(item.recorded_at)) };
}

export function EvidenceWorkspace({ locale }: { locale: Locale }) {
  const zh = locale === "zh-CN";
  const workspace = useProductWorkspace(locale, !demoMode);
  const [filter, setFilter] = useState("all");
  const types = [...new Set(workspace.evidence.map((item) => item.evidence_type))];
  const items = filter === "all" ? workspace.evidence : workspace.evidence.filter((item) => item.evidence_type === filter);
  const applied = workspace.evidence.filter((item) => evidencePresentation(item, locale).level === "applied").length;
  const verified = workspace.evidence.filter((item) => evidencePresentation(item, locale).level === "reviewer_verified").length;
  return <div className="page-wrap"><header className="page-head"><div><p className="eyebrow">{zh ? "成长记录" : "Evidence ledger"}</p><h1>{zh ? "每个判断，都能追溯到依据。" : "Every judgment has a traceable basis."}</h1><p>{zh ? "这里不是积分表，而是你完成的工作、反思、评审和可验证成果。" : "This is not a scorecard. It is the work, reflection, review, and verifiable outcomes behind your route."}</p></div><Link className="button" href={`/${locale}/settings#data`}><Download />{zh ? "导出记录" : "Export ledger"}</Link></header>
    {!demoMode && workspace.error && <section className="card error-note" role="alert">{workspace.error}</section>}
    <section className="evidence-summary grid-3"><article className="card"><p className="section-label">{zh ? "已记录" : "Recorded"}</p><strong>{demoMode ? 12 : workspace.evidence.length}</strong><span>{zh ? "项证据" : "evidence items"}</span></article><article className="card"><p className="section-label">{zh ? "已应用" : "Applied"}</p><strong>{demoMode ? 3 : applied}</strong><span>{zh ? "个项目场景" : "project contexts"}</span></article><article className="card"><p className="section-label">{zh ? "评审验证" : "Reviewer verified"}</p><strong>{demoMode ? 2 : verified}</strong><span>{zh ? "项独立验证" : "independent checks"}</span></article></section>
    <section className="ledger card"><header><div><p className="section-label">Ledger</p><h2>{zh ? "最近证据" : "Recent evidence"}</h2></div>{!demoMode && <label className="field"><span>{zh ? "类型" : "Type"}</span><select value={filter} onChange={(event) => setFilter(event.target.value)}><option value="all">{zh ? "全部" : "All"}</option>{types.map((type) => <option value={type} key={type}>{type}</option>)}</select></label>}</header>{demoMode ? demoEvidence.map((item) => <article className="ledger-row" key={item.id}><span className="ledger-icon">{item.level === "applied" ? <ShieldCheck /> : <FileText />}</span><div><h3>{item.title}</h3><p>{item.type} · {item.state} · {item.date}</p></div><LevelBadge locale={locale} level={item.level} /></article>) : workspace.loading ? <p>{zh ? "正在读取证据…" : "Loading evidence…"}</p> : items.length ? items.map((item) => { const view = evidencePresentation(item, locale); return <article className="ledger-row" key={item.id}><span className="ledger-icon">{view.level === "applied" || view.level === "reviewer_verified" ? <ShieldCheck /> : <FileText />}</span><div><h3>{view.title}</h3><p>{item.source_kind} · {item.status} · {view.date}</p></div>{view.level ? <LevelBadge locale={locale} level={view.level} /> : <span className="chip">{item.evidence_type}</span>}</article>; }) : <p className="muted">{zh ? "尚无证据。完成任务或项目后，不可变记录会显示在这里。" : "No evidence yet. Immutable records appear here after tasks or projects."}</p>}</section>
    <section className="evidence-note card soft"><ShieldCheck /><div><h2>{zh ? "证据等级不是分数" : "Evidence levels are not scores"}</h2><p>{zh ? "等级只描述依据强度：推断、你确认、任务展示、项目应用、评审验证。它们不会被转换为虚假的精确百分比。" : "Levels describe basis strength: inferred, confirmed by you, demonstrated in a task, applied in a project, or reviewer verified. They are never turned into false precision."}</p></div></section>
    <EvidenceSharing locale={locale} />
  </div>;
}
