"use client";

import Link from "next/link";
import { useMemo, useState } from "react";
import { ArrowRight, Check, MessageSquareText, Route, Sparkles, TimerReset } from "lucide-react";
import type { Locale } from "@/i18n/config";
import { Brand } from "./brand";

type Mode = "quick" | "natural";

export function OnboardingFlow({ locale }: { locale: Locale }) {
  const zh = locale === "zh-CN";
  const [mode, setMode] = useState<Mode | null>(null);
  const [step, setStep] = useState(0);
  const [known, setKnown] = useState("Frontend engineering");
  const [target, setTarget] = useState("Cloud agent engineering");
  const [difficulty, setDifficulty] = useState("balanced");
  const [story, setStory] = useState("");
  const [naturalStructured, setNaturalStructured] = useState(false);
  const summary = useMemo(() => ({ known: known.trim() || (zh ? "现有经验" : "Your current experience"), target: target.trim() || (zh ? "下一目标" : "Your next direction") }), [known, target, zh]);

  if (!mode) return (
    <main className="onboarding-shell" id="main-content">
      <header className="onboarding-top"><Brand locale={locale} /><Link href={`/${locale}/register`}>{zh ? "登录 / 注册" : "Sign in / register"}</Link></header>
      <section className="onboarding-hero">
        <p className="eyebrow">{zh ? "从你已会的开始" : "Start with what is already yours"}</p>
        <h1>{zh ? "不是从零开始，\n而是换一条路继续成长。" : "You are not starting over.\nYou are changing direction."}</h1>
        <p>{zh ? "Lites 找出可迁移能力，用真实任务验证它，再把结果变成你能带走的证据。" : "Lites finds the capabilities that transfer, tests them in real work, and turns the result into evidence you can carry forward."}</p>
      </section>
      <section className="mode-grid" aria-label={zh ? "选择引导方式" : "Choose an onboarding path"}>
        <button type="button" onClick={() => setMode("quick")} className="mode-card" data-testid="quick-onboarding"><span className="mode-icon"><TimerReset /></span><span><strong>{zh ? "快速开始" : "Quick start"}</strong><small>{zh ? "3 个选择，大约 2 分钟" : "Three choices, about two minutes"}</small></span><ArrowRight /></button>
        <button type="button" onClick={() => setMode("natural")} className="mode-card" data-testid="natural-onboarding"><span className="mode-icon alt"><MessageSquareText /></span><span><strong>{zh ? "聊聊你的经历" : "Tell us your story"}</strong><small>{zh ? "用自己的话描述，Coach 帮你整理" : "Use your own words; Coach structures the route"}</small></span><ArrowRight /></button>
      </section>
      <p className="privacy-note">{zh ? "默认私密。你的路线和证据由你控制。" : "Private by default. You control your route and evidence."}</p>
    </main>
  );

  const quickSteps = [
    <div className="form-grid" key="known"><p className="eyebrow">01 · {zh ? "现在" : "Now"}</p><h2>{zh ? "你已经在哪些事情上有经验？" : "Where do you already have real experience?"}</h2><label className="field"><span>{zh ? "领域、角色或一组能力" : "Field, role, or capability set"}</span><input name="current-experience" autoComplete="organization-title" value={known} onChange={(e) => setKnown(e.target.value)} placeholder="e.g. Frontend engineering…" /></label><div className="suggestions">{["Frontend engineering", "Product design", "Operations", "Research"].map((item) => <button type="button" className="chip-button" onClick={() => setKnown(item)} key={item}>{item}</button>)}</div></div>,
    <div className="form-grid" key="target"><p className="eyebrow">02 · {zh ? "下一步" : "Next"}</p><h2>{zh ? "你想把这些能力迁移到哪里？" : "Where do you want those capabilities to travel?"}</h2><label className="field"><span>{zh ? "目标方向" : "Target direction"}</span><input name="target-direction" autoComplete="off" value={target} onChange={(e) => setTarget(e.target.value)} placeholder="e.g. Cloud agent engineering…" /></label><div className="suggestions">{["Cloud agent engineering", "AI product management", "Developer relations", "Independent creator"].map((item) => <button type="button" className="chip-button" onClick={() => setTarget(item)} key={item}>{item}</button>)}</div></div>,
    <div className="form-grid" key="pace"><p className="eyebrow">03 · {zh ? "节奏" : "Pace"}</p><h2>{zh ? "这条路线应该怎样挑战你？" : "How should this route challenge you?"}</h2><div className="choice-stack">{([ ["steady", zh ? "稳步" : "Steady", "15–20 min"], ["balanced", zh ? "平衡" : "Balanced", "20–35 min"], ["stretch", zh ? "拉伸" : "Stretch", "35–50 min"] ] as const).map(([value, title, note]) => <label className={difficulty === value ? "choice-card selected" : "choice-card"} key={value}><input type="radio" name="difficulty" value={value} checked={difficulty === value} onChange={() => setDifficulty(value)} /><span><strong>{title}</strong><small>{note}</small></span>{difficulty === value && <Check />}</label>)}</div></div>,
  ];

  const isNatural = mode === "natural";
  const complete = isNatural ? naturalStructured : step === quickSteps.length;
  return (
    <main className="onboarding-shell flow" id="main-content">
      <header className="onboarding-top"><Brand locale={locale} /><button className="text-button" type="button" onClick={() => { setMode(null); setStep(0); }}>{zh ? "更换方式" : "Change path"}</button></header>
      <div className="onboarding-flow-card">
        {!isNatural && step < quickSteps.length && quickSteps[step]}
        {isNatural && !complete && <div className="form-grid"><p className="eyebrow"><Sparkles size={14} /> Coach intake</p><h2>{zh ? "说说你走过的路，以及接下来想去哪。" : "Tell us where you have been and where you want to go."}</h2><p className="muted">{zh ? "不需要履历格式。可以写项目、转折、困惑和期待。" : "No résumé format needed. Projects, turning points, uncertainty, and hopes are all useful."}</p><label className="field"><span>{zh ? "你的故事" : "Your story"}</span><textarea name="experience-story" autoComplete="off" value={story} onChange={(e) => setStory(e.target.value)} placeholder={zh ? "我做了几年……最近我发现……下一步想……" : "I have spent the last few years… Recently I noticed… Next I want to…"} /></label><button className="button primary" type="button" disabled={!story.trim()} onClick={() => { setKnown(zh ? "从经历中提取的现有能力" : "Capabilities extracted from your story"); setTarget(zh ? "你描述的下一方向" : "The next direction in your story"); setNaturalStructured(true); }}>{zh ? "让 Coach 整理" : "Let Coach structure it"}<ArrowRight /></button></div>}
        {complete && <div className="route-preview"><span className="mode-icon"><Route /></span><p className="eyebrow">{zh ? "路线草案已准备" : "Route draft ready"}</p><h2>{summary.known}<br /><span>→ {summary.target}</span></h2><p>{zh ? "这只是一个可修正的假设。下一步先确认能力桥，再用任务验证。" : "This is a revisable hypothesis. Next, confirm the capability bridge and test it through work."}</p><Link className="button primary" href={`/${locale}/route?from=${encodeURIComponent(summary.known)}&to=${encodeURIComponent(summary.target)}`}>{zh ? "查看我的路线" : "Review my route"}<ArrowRight /></Link></div>}
        {!isNatural && step < quickSteps.length && <footer className="flow-actions"><button type="button" className="button ghost" disabled={step === 0} onClick={() => setStep((value) => Math.max(0, value - 1))}>{zh ? "返回" : "Back"}</button><span>{step + 1} / {quickSteps.length}</span><button type="button" className="button primary" onClick={() => setStep((value) => value + 1)}>{step === quickSteps.length - 1 ? (zh ? "生成路线" : "Build route") : (zh ? "继续" : "Continue")}<ArrowRight /></button></footer>}
      </div>
    </main>
  );
}
