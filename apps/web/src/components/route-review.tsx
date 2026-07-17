"use client";

import Link from "next/link";
import { useEffect, useRef, useState } from "react";
import { AlertCircle, ArrowRight, Check, LoaderCircle, Pencil, Sparkles } from "lucide-react";
import type { Locale } from "@/i18n/config";
import { ApiError, apiRequest, newIdempotencyKey } from "@/lib/api/client";
import { LevelBadge } from "./level-badge";

type Assessment = {
  statement: string;
  capability_ids: string[];
  evidence_ids: string[];
  confidence: "inferred" | "supported" | "verified";
};

type RouteDocument = {
  schema_version: 1;
  summary: string;
  transferable_experience: Assessment[];
  gaps: Assessment[];
  bridge: Array<{ id: string; title: string; rationale: string; from_capability_ids: string[]; to_capability_ids: string[] }>;
  stages: Array<{ id: string; title: string; outcome: string; capability_ids: string[]; evidence_required: string[] }>;
  first_task: { title: string; objective: string; estimated_minutes: number; difficulty: "easy" | "standard" | "stretch"; capability_ids: string[]; success_criteria: string[] };
};

type OnboardingRoute = {
  id: string;
  version: number;
  status: "collecting" | "route_generating" | "route_ready" | "route_failed";
  mission_id: string | null;
  route_revision_id: string | null;
  route: RouteDocument | null;
  claim_version: number | null;
  updated_at: string;
};

type RouteReviewProps = {
  locale: Locale;
  from: string;
  to: string;
  onboardingSessionID?: string;
  onboardingVersion?: number;
  sourceRoleProfileID?: string;
  targetRoleProfileID?: string;
};

const confidenceLevel = { inferred: "inferred", supported: "demonstrated", verified: "reviewer_verified" } as const;

export function RouteReview({ locale, from, to, onboardingSessionID, onboardingVersion, sourceRoleProfileID, targetRoleProfileID }: RouteReviewProps) {
  const zh = locale === "zh-CN";
  const requestKey = useRef<string | null>(null);
  const clientRequestID = useRef<string | null>(null);
  const [resource, setResource] = useState<OnboardingRoute | null>(null);
  const [error, setError] = useState("");
  const [confirmed, setConfirmed] = useState<string[]>([]);
  const [editingBridge, setEditingBridge] = useState(false);
  const [bridgeCorrection, setBridgeCorrection] = useState("");
  const [bridgeDraft, setBridgeDraft] = useState("");
  const configurationError = !onboardingSessionID || !onboardingVersion || !targetRoleProfileID
    ? (zh ? "路线链接缺少会话或角色信息，请返回重新创建。" : "This route link is missing its session or role context. Return and create it again.")
    : "";

  useEffect(() => {
    if (!onboardingSessionID || !onboardingVersion || !targetRoleProfileID) return;
    const controller = new AbortController();
    requestKey.current ??= newIdempotencyKey();
    clientRequestID.current ??= newIdempotencyKey();
    const key = requestKey.current;
    const requestID = clientRequestID.current;
    let stopped = false;

    async function load() {
      try {
        let current = await apiRequest<OnboardingRoute>(`/v1/onboarding-sessions/${onboardingSessionID}`, { signal: controller.signal });
        if ((current.status === "collecting" || current.status === "route_failed") && !stopped) {
          await apiRequest(`/v1/onboarding-sessions/${onboardingSessionID}/route-preview`, {
            method: "POST",
            signal: controller.signal,
            idempotencyKey: key,
            ifMatch: `"${current.version}"`,
            body: {
              request_id: requestID,
              source_role_profile_id: sourceRoleProfileID ?? "",
              target_role_profile_id: targetRoleProfileID,
              confirmed_claim_ids: [],
              expected_onboarding_version: current.version,
            },
          });
        }
        for (let attempt = 0; attempt < 120 && !stopped; attempt += 1) {
          current = await apiRequest<OnboardingRoute>(`/v1/onboarding-sessions/${onboardingSessionID}`, { signal: controller.signal });
          setResource(current);
          if (current.status === "route_ready" && current.route && current.claim_version) return;
          if (current.status === "route_failed") throw new ApiError("route_generation_failed", 409, null);
          await new Promise((resolve) => window.setTimeout(resolve, 1500));
        }
        if (!stopped) throw new ApiError("route_generation_timeout", 504, null);
      } catch (caught) {
        if (stopped || caught instanceof DOMException && caught.name === "AbortError") return;
        setError(zh ? "Coach 暂时无法完成路线。请刷新重试；匿名会话仍安全保留。" : "Coach could not finish the route yet. Refresh to retry; your anonymous session is still safe.");
      }
    }
    void load();
    return () => { stopped = true; controller.abort(); };
  }, [onboardingSessionID, onboardingVersion, sourceRoleProfileID, targetRoleProfileID, zh]);

  const route = resource?.route;
  const visibleError = configurationError || error;
  const assessments = route ? [...route.transferable_experience, ...route.gaps] : [];
  const allConfirmed = assessments.length > 0 && confirmed.length === assessments.length;
  const primaryBridge = bridgeCorrection || route?.bridge[0]?.title || "";
  const registrationQuery = new URLSearchParams();
  if (onboardingSessionID && resource?.claim_version) {
    registrationQuery.set("onboarding_session", onboardingSessionID);
    registrationQuery.set("onboarding_version", String(resource.claim_version));
  }
  const registrationURL = `/${locale}/register${registrationQuery.size ? `?${registrationQuery.toString()}` : ""}`;

  return <main className="route-shell" id="main-content">
    <header className="route-top"><p className="eyebrow"><Sparkles size={14} /> {zh ? "Coach 路线假设" : "Coach route hypothesis"}</p><h1>{from}<br /><span>→ {to}</span></h1><p>{route?.summary ?? (zh ? "Coach 正在基于已发布的角色能力与匿名输入生成路线。" : "Coach is grounding your route in the published role capabilities and your anonymous intake.")}</p></header>
    {!route && <section className="bridge-card card" aria-live="polite"><div><p className="section-label">{visibleError ? (zh ? "生成未完成" : "Generation incomplete") : (zh ? "正在生成" : "Generating")}</p><h2>{visibleError ? (zh ? "路线暂时不可用" : "The route is not available yet") : (zh ? "Agent 正在构建可验证的能力桥" : "The agent is building an evidence-backed capability bridge")}</h2><p>{visibleError || (zh ? "这通常需要几十秒。页面会自动读取 durable worker 的结果。" : "This usually takes tens of seconds. The page reads the durable worker result automatically.")}</p></div>{visibleError ? <AlertCircle /> : <LoaderCircle className="spin" />}</section>}
    {route && <>
      <section className="bridge-card card"><div><p className="section-label">{zh ? "迁移桥" : "Capability bridge"}{bridgeCorrection ? ` · ${zh ? "已由你修正" : "Corrected by you"}` : ""}</p>{editingBridge ? <div className="bridge-editor"><label className="field"><span>{zh ? "修正迁移关系" : "Correct the transfer relationship"}</span><textarea name="bridge-correction" autoComplete="off" value={bridgeDraft} onChange={(event) => setBridgeDraft(event.target.value)} /></label><div><button className="button ghost" type="button" onClick={() => { setBridgeDraft(primaryBridge); setEditingBridge(false); }}>{zh ? "取消" : "Cancel"}</button><button className="button primary" type="button" disabled={!bridgeDraft.trim()} onClick={() => { setBridgeCorrection(bridgeDraft.trim()); setEditingBridge(false); }}>{zh ? "保存修正" : "Save correction"}</button></div></div> : <><h2>{primaryBridge}</h2><p>{route.bridge[0]?.rationale}</p></>}</div><button type="button" className="icon-button" aria-label={zh ? "编辑迁移桥" : "Edit bridge"} aria-expanded={editingBridge} onClick={() => { setBridgeDraft(primaryBridge); setEditingBridge(true); }}><Pencil /></button></section>
      <section className="route-capabilities"><div className="section-intro"><p className="section-label">{zh ? "请确认或修正" : "Confirm or correct"}</p><h2>{zh ? "每项判断都保留依据与置信度" : "Every judgment keeps its basis and confidence"}</h2></div>{assessments.map((assessment, index) => { const id = `${index}:${assessment.capability_ids.join(",")}`; const selected = confirmed.includes(id); return <article className="capability-row" key={id}><button type="button" aria-label={`${selected ? (zh ? "取消确认" : "Unconfirm") : (zh ? "确认" : "Confirm")} ${assessment.statement}`} aria-pressed={selected} className={selected ? "confirm-control selected" : "confirm-control"} onClick={() => setConfirmed((items) => selected ? items.filter((item) => item !== id) : [...items, id])}>{selected ? <Check aria-hidden="true" /> : <span />}</button><div><h3>{assessment.statement}</h3><p>{assessment.evidence_ids.length ? `${zh ? "证据" : "Evidence"}: ${assessment.evidence_ids.join(", ")}` : `${zh ? "能力" : "Capabilities"}: ${assessment.capability_ids.join(", ")}`}</p></div><LevelBadge locale={locale} level={confidenceLevel[assessment.confidence]} /></article>; })}</section>
      <section className="route-capabilities"><div className="section-intro"><p className="section-label">{zh ? "执行路线" : "Execution route"}</p><h2>{zh ? "从路线到第一项可验证任务" : "From route to the first verifiable task"}</h2></div>{route.stages.map((stage, index) => <article className="capability-row" key={stage.id}><span className="confirm-control selected">{index + 1}</span><div><h3>{stage.title}</h3><p>{stage.outcome}</p></div><span className="chip">{stage.evidence_required.length} {zh ? "项证据" : "evidence"}</span></article>)}<article className="card"><p className="section-label">{zh ? "第一项任务" : "First task"} · {route.first_task.estimated_minutes} min</p><h3>{route.first_task.title}</h3><p>{route.first_task.objective}</p></article></section>
      <footer className="route-actions"><p>{allConfirmed ? (zh ? "你已检查全部判断。注册后，路线会幂等归属到你的工作区。" : "You reviewed every judgment. After registration, the route is attached idempotently to your workspace.") : (zh ? `还有 ${assessments.length - confirmed.length} 项未确认；也可以进入工作区后继续修正。` : `${assessments.length - confirmed.length} judgments remain; you can continue correcting them in the workspace.`)}</p><Link className="button primary" aria-disabled={!resource?.claim_version} href={resource?.claim_version ? registrationURL : "#"}>{zh ? "保存路线并继续" : "Save route and continue"}<ArrowRight /></Link></footer>
    </>}
  </main>;
}
