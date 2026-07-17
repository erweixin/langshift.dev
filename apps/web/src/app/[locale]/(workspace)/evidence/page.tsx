import { notFound } from "next/navigation";
import { Download, ExternalLink, FileText, Filter, ShieldCheck } from "lucide-react";
import { LevelBadge } from "@/components/level-badge";
import { isLocale } from "@/i18n/config";
import { demoEvidence } from "@/lib/model";

export default async function EvidencePage({ params }: { params: Promise<{ locale: string }> }) {
  const { locale } = await params; if (!isLocale(locale)) notFound(); const zh = locale === "zh-CN";
  return <div className="page-wrap"><header className="page-head"><div><p className="eyebrow">{zh ? "成长记录" : "Evidence ledger"}</p><h1>{zh ? "每个判断，都能追溯到依据。" : "Every judgment has a traceable basis."}</h1><p>{zh ? "这里不是积分表，而是你完成的工作、反思、评审和可验证成果。" : "This is not a scorecard. It is the work, reflection, review, and verifiable outcomes behind your route."}</p></div><button className="button"><Download />{zh ? "导出记录" : "Export ledger"}</button></header>
    <section className="evidence-summary grid-3"><article className="card"><p className="section-label">{zh ? "已记录" : "Recorded"}</p><strong>12</strong><span>{zh ? "项证据" : "evidence items"}</span></article><article className="card"><p className="section-label">{zh ? "已应用" : "Applied"}</p><strong>3</strong><span>{zh ? "个项目场景" : "project contexts"}</span></article><article className="card"><p className="section-label">{zh ? "评审验证" : "Reviewer verified"}</p><strong>2</strong><span>{zh ? "项独立验证" : "independent checks"}</span></article></section>
    <section className="ledger card"><header><div><p className="section-label">Ledger</p><h2>{zh ? "最近证据" : "Recent evidence"}</h2></div><button className="button ghost"><Filter />{zh ? "筛选" : "Filter"}</button></header>{demoEvidence.map((item) => <article className="ledger-row" key={item.id}><span className="ledger-icon">{item.level === "applied" ? <ShieldCheck /> : <FileText />}</span><div><h3>{item.title}</h3><p>{item.type} · {item.state} · {item.date}</p></div><LevelBadge locale={locale} level={item.level} /><button className="icon-button" aria-label={`${zh ? "打开" : "Open"} ${item.title}`}><ExternalLink /></button></article>)}</section>
    <section className="evidence-note card soft"><ShieldCheck /><div><h2>{zh ? "证据等级不是分数" : "Evidence levels are not scores"}</h2><p>{zh ? "等级只描述依据强度：推断、你确认、任务展示、项目应用、评审验证。它们不会被转换为虚假的精确百分比。" : "Levels describe basis strength: inferred, confirmed by you, demonstrated in a task, applied in a project, or reviewer verified. They are never turned into false precision."}</p></div></section>
  </div>;
}
