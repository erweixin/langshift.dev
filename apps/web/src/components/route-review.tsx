"use client";

import Link from "next/link";
import { useState } from "react";
import { ArrowRight, Check, Pencil, Sparkles } from "lucide-react";
import type { Locale } from "@/i18n/config";
import { demoCapabilities } from "@/lib/model";
import { LevelBadge } from "./level-badge";

export function RouteReview({ locale, from, to }: { locale: Locale; from: string; to: string }) {
  const zh = locale === "zh-CN";
  const [confirmed, setConfirmed] = useState<string[]>([]);
  const allConfirmed = confirmed.length === demoCapabilities.length;
  return <main className="route-shell" id="main-content">
    <header className="route-top"><p className="eyebrow"><Sparkles size={14} /> {zh ? "Coach 路线假设" : "Coach route hypothesis"}</p><h1>{from}<br /><span>→ {to}</span></h1><p>{zh ? "这不是固定课程。每项判断都标明依据，并会随证据更新。" : "This is not a fixed curriculum. Every judgment exposes its basis and changes when evidence changes."}</p></header>
    <section className="bridge-card card"><div><p className="section-label">{zh ? "迁移桥" : "Capability bridge"}</p><h2>{zh ? "从界面状态与异步流，迁移到可恢复的 Agent 运行生命周期" : "From UI state and async flows to recoverable agent run lifecycles"}</h2><p>{zh ? "共同点是状态转换；关键差异是进程崩溃、重试和不可逆副作用。" : "The shared primitive is state transition. The hard difference is surviving crashes, retries, and irreversible effects."}</p></div><button className="icon-button" aria-label={zh ? "编辑迁移桥" : "Edit bridge"}><Pencil /></button></section>
    <section className="route-capabilities"><div className="section-intro"><p className="section-label">{zh ? "请确认或修正" : "Confirm or correct"}</p><h2>{zh ? "我们认为这些能力可以迁移" : "We think these capabilities can transfer"}</h2></div>{demoCapabilities.map((capability) => { const selected = confirmed.includes(capability.id); return <article className="capability-row" key={capability.id}><button type="button" aria-label={`${selected ? (zh ? "取消确认" : "Unconfirm") : (zh ? "确认" : "Confirm")} ${capability.name}`} aria-pressed={selected} className={selected ? "confirm-control selected" : "confirm-control"} onClick={() => setConfirmed((items) => selected ? items.filter((id) => id !== capability.id) : [...items, capability.id])}>{selected ? <Check aria-hidden="true" /> : <span />}</button><div><h3>{capability.name}</h3><p>{capability.basis}</p></div><LevelBadge locale={locale} level={capability.level} /></article>; })}</section>
    <footer className="route-actions"><p>{allConfirmed ? (zh ? "路线已由你确认，可以开始第一项验证。" : "You confirmed the route. The first test is ready.") : (zh ? `还需确认 ${demoCapabilities.length - confirmed.length} 项；也可以进入后在地图中修正。` : `${demoCapabilities.length - confirmed.length} judgments remain; you can also correct them later in the map.`)}</p><Link className="button primary" href={`/${locale}/register?claim=route`}>{zh ? "保存路线并继续" : "Save route and continue"}<ArrowRight /></Link></footer>
  </main>;
}
