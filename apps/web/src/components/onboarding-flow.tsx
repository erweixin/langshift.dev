"use client";

import Link from "next/link";
import { useEffect, useMemo, useState } from "react";
import { useRouter } from "next/navigation";
import { AlertCircle, ArrowRight, Check, LoaderCircle, MessageSquareText, Route, Sparkles, TimerReset } from "lucide-react";
import type { Locale } from "@/i18n/config";
import { ApiError, apiRequest, newIdempotencyKey } from "@/lib/api/client";
import { Brand } from "./brand";

type Mode = "quick" | "natural";
type RoleCatalogItem = { id: string; slug: string; revision: number; status: "active"; name: string };

export function OnboardingFlow({ locale }: { locale: Locale }) {
  const zh = locale === "zh-CN";
  const router = useRouter();
  const [mode, setMode] = useState<Mode | null>(null);
  const [step, setStep] = useState(0);
  const [known, setKnown] = useState("Frontend engineering");
  const [target, setTarget] = useState("Cloud agent engineering");
  const [difficulty, setDifficulty] = useState("balanced");
  const [story, setStory] = useState("");
  const [creating, setCreating] = useState(false);
  const [error, setError] = useState("");
  const [roles, setRoles] = useState<RoleCatalogItem[]>([]);
  const [sourceRoleID, setSourceRoleID] = useState("");
  const [targetRoleID, setTargetRoleID] = useState("");
  const summary = useMemo(() => ({ known: known.trim() || (zh ? "现有经验" : "Your current experience"), target: target.trim() || (zh ? "下一目标" : "Your next direction") }), [known, target, zh]);

  useEffect(() => {
    const controller = new AbortController();
    apiRequest<{ items: RoleCatalogItem[] }>(`/v1/catalog/roles?locale=${encodeURIComponent(locale)}`, { accept: "application/vnd.lites.role-catalog.v1+json", signal: controller.signal })
      .then((catalog) => {
        setRoles(catalog.items);
        const source = catalog.items.find((role) => role.slug === "role_frontend_developer") ?? catalog.items[0];
        const targetRole = catalog.items.find((role) => role.slug === "role_ai_application_engineer") ?? catalog.items[1] ?? source;
        if (source) { setSourceRoleID(source.id); setKnown(source.name); }
        if (targetRole) { setTargetRoleID(targetRole.id); setTarget(targetRole.name); }
      })
      .catch((caught) => {
        if (caught instanceof DOMException && caught.name === "AbortError") return;
        setError(zh ? "角色目录暂时不可用，请稍后重试。" : "The role catalog is temporarily unavailable. Please try again.");
      });
    return () => controller.abort();
  }, [locale, zh]);

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
    <div className="form-grid" key="known"><p className="eyebrow">01 · {zh ? "现在" : "Now"}</p><h2>{zh ? "你已经在哪些事情上有经验？" : "Where do you already have real experience?"}</h2><label className="field"><span>{zh ? "当前角色" : "Current role"}</span><select name="current-experience" value={sourceRoleID} disabled={!roles.length} onChange={(event) => { const role = roles.find((item) => item.id === event.target.value); setSourceRoleID(event.target.value); if (role) setKnown(role.name); }}>{roles.map((role) => <option value={role.id} key={role.id}>{role.name}</option>)}</select></label></div>,
    <div className="form-grid" key="target"><p className="eyebrow">02 · {zh ? "下一步" : "Next"}</p><h2>{zh ? "你想把这些能力迁移到哪里？" : "Where do you want those capabilities to travel?"}</h2><label className="field"><span>{zh ? "目标角色" : "Target role"}</span><select name="target-direction" value={targetRoleID} disabled={!roles.length} onChange={(event) => { const role = roles.find((item) => item.id === event.target.value); setTargetRoleID(event.target.value); if (role) setTarget(role.name); }}>{roles.map((role) => <option value={role.id} key={role.id}>{role.name}</option>)}</select></label></div>,
    <div className="form-grid" key="pace"><p className="eyebrow">03 · {zh ? "节奏" : "Pace"}</p><h2>{zh ? "这条路线应该怎样挑战你？" : "How should this route challenge you?"}</h2><div className="choice-stack">{([ ["steady", zh ? "稳步" : "Steady", "15–20 min"], ["balanced", zh ? "平衡" : "Balanced", "20–35 min"], ["stretch", zh ? "拉伸" : "Stretch", "35–50 min"] ] as const).map(([value, title, note]) => <label className={difficulty === value ? "choice-card selected" : "choice-card"} key={value}><input type="radio" name="difficulty" value={value} checked={difficulty === value} onChange={() => setDifficulty(value)} /><span><strong>{title}</strong><small>{note}</small></span>{difficulty === value && <Check />}</label>)}</div></div>,
  ];

  const isNatural = mode === "natural";
  const complete = !isNatural && step === quickSteps.length;
  async function createOnboarding() {
    if (creating) return;
    setCreating(true);
    setError("");
    try {
      const requestID = newIdempotencyKey();
      const weeklyMinutes = difficulty === "steady" ? 105 : difficulty === "stretch" ? 300 : 180;
      const result = await apiRequest<{ id: string; version: number }>("/v1/onboarding-sessions", {
        method: "POST",
        idempotencyKey: newIdempotencyKey(),
        body: { request_id: requestID, current_role: summary.known, target_role: summary.target, experience_summary: story.trim() || `${summary.known} → ${summary.target}`, weekly_minutes: weeklyMinutes },
      });
      const query = new URLSearchParams({ from: summary.known, to: summary.target, onboarding_session: result.id, onboarding_version: String(result.version), source_role_profile: sourceRoleID, target_role_profile: targetRoleID });
      router.push(`/${locale}/route?${query.toString()}`);
    } catch (caught) {
      const code = caught instanceof ApiError ? caught.message : "request_failed";
      setError(code === "rate_limited" ? (zh ? "请求过于频繁，请稍后再试。" : "Too many attempts. Please wait and try again.") : (zh ? "暂时无法创建匿名路线，请重试；你填写的内容尚未提交。" : "The anonymous route could not be created. Try again; your input has not been submitted."));
    } finally {
      setCreating(false);
    }
  }
  return (
    <main className="onboarding-shell flow" id="main-content">
      <header className="onboarding-top"><Brand locale={locale} /><button className="text-button" type="button" onClick={() => { setMode(null); setStep(0); }}>{zh ? "更换方式" : "Change path"}</button></header>
      <div className="onboarding-flow-card">
        {!isNatural && step < quickSteps.length && quickSteps[step]}
        {isNatural && <div className="form-grid"><p className="eyebrow"><Sparkles size={14} /> Coach intake</p><h2>{zh ? "说说你走过的路，以及接下来想去哪。" : "Tell us where you have been and where you want to go."}</h2><p className="muted">{zh ? "你的原始描述会被安全保存，并作为结构化 Route Planner 的真实输入；两个角色用于锚定当前职业目录。" : "Your original account is stored securely and passed into the structured Route Planner; the two roles anchor it to the current career catalog."}</p><label className="field"><span>{zh ? "当前角色锚点" : "Current role anchor"}</span><select name="natural-current-role" value={sourceRoleID} disabled={!roles.length} onChange={(event) => { const role = roles.find((item) => item.id === event.target.value); setSourceRoleID(event.target.value); if (role) setKnown(role.name); }}>{roles.map((role) => <option value={role.id} key={role.id}>{role.name}</option>)}</select></label><label className="field"><span>{zh ? "目标角色锚点" : "Target role anchor"}</span><select name="natural-target-role" value={targetRoleID} disabled={!roles.length} onChange={(event) => { const role = roles.find((item) => item.id === event.target.value); setTargetRoleID(event.target.value); if (role) setTarget(role.name); }}>{roles.map((role) => <option value={role.id} key={role.id}>{role.name}</option>)}</select></label><label className="field"><span>{zh ? "你的故事" : "Your story"}</span><textarea name="experience-story" autoComplete="off" value={story} onChange={(e) => setStory(e.target.value)} placeholder={zh ? "我做了几年……最近我发现……下一步想……" : "I have spent the last few years… Recently I noticed… Next I want to…"} /></label>{error && <p className="error-note" role="alert"><AlertCircle />{error}</p>}<button className="button primary" type="button" disabled={creating || !story.trim() || !sourceRoleID || !targetRoleID} onClick={createOnboarding}>{creating ? <LoaderCircle className="spin" /> : null}{zh ? "提交经历并生成路线" : "Submit story and build route"}<ArrowRight /></button></div>}
        {complete && <div className="route-preview"><span className="mode-icon"><Route /></span><p className="eyebrow">{zh ? "路线输入已准备" : "Route input ready"}</p><h2>{summary.known}<br /><span>→ {summary.target}</span></h2><p>{zh ? "这只是一个可修正的假设。下一步会创建隔离的匿名会话，再由你确认能力桥。" : "This is a revisable hypothesis. Next, an isolated anonymous session is created before you confirm the capability bridge."}</p>{error && <p className="error-note" role="alert"><AlertCircle />{error}</p>}<button className="button primary" type="button" disabled={creating || !sourceRoleID || !targetRoleID} onClick={createOnboarding}>{creating ? <LoaderCircle className="spin" /> : null}{zh ? "创建并查看路线" : "Create and review route"}<ArrowRight /></button></div>}
        {!isNatural && step < quickSteps.length && <footer className="flow-actions"><button type="button" className="button ghost" disabled={step === 0} onClick={() => setStep((value) => Math.max(0, value - 1))}>{zh ? "返回" : "Back"}</button><span>{step + 1} / {quickSteps.length}</span><button type="button" className="button primary" onClick={() => setStep((value) => value + 1)}>{step === quickSteps.length - 1 ? (zh ? "生成路线" : "Build route") : (zh ? "继续" : "Continue")}<ArrowRight /></button></footer>}
      </div>
    </main>
  );
}
