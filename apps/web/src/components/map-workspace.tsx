"use client";

import { ArrowDown, Check, CircleDashed, LockKeyhole, MoveRight, Sparkles } from "lucide-react";
import type { Locale } from "@/i18n/config";
import { demoMode } from "@/lib/demo";
import { demoCapabilities, demoMission } from "@/lib/model";
import { useProductWorkspace } from "@/lib/use-product-workspace";
import { LevelBadge } from "./level-badge";

const confidenceLevel = { inferred: "inferred", supported: "demonstrated", verified: "reviewer_verified" } as const;

export function MapWorkspace({ locale }: { locale: Locale }) {
  const zh = locale === "zh-CN";
  const workspace = useProductWorkspace(locale, !demoMode);
  const route = workspace.route?.route;
  const focus = workspace.missions.focus;
  const sourceName = focus?.source_role_profile_id ? workspace.missions.roleNames.get(focus.source_role_profile_id) : null;
  const targetName = focus ? workspace.missions.roleNames.get(focus.target_role_profile_id) : null;
  const demoStages = [
    { id: "stage_1", title: zh ? "建立运行时心智模型" : "Build the runtime mental model", outcome: zh ? "状态转换、持久化与恢复" : "Transitions, persistence, and recovery" },
    { id: "stage_2", title: zh ? "处理副作用与对账" : "Handle effects and reconciliation", outcome: zh ? "幂等、收据与补偿" : "Idempotency, receipts, and compensation" },
    { id: "stage_3", title: zh ? "隔离与调度" : "Isolation and scheduling", outcome: zh ? "租户边界、容量与公平性" : "Tenant boundaries, capacity, and fairness" },
    { id: "stage_4", title: zh ? "生产运营" : "Production operations", outcome: zh ? "可观测性、SLO 与事件响应" : "Observability, SLOs, and incidents" },
  ];
  const stages = demoMode ? demoStages : route?.stages ?? [];
  const assessments = route ? [...route.transferable_experience, ...route.gaps] : [];
  return <div className="page-wrap"><header className="page-head"><div><p className="eyebrow">{zh ? "迁移地图" : "Migration map"}</p><h1>{demoMode ? demoMission.path : sourceName && targetName ? `${sourceName} → ${targetName}` : targetName ?? (zh ? "尚无激活路线" : "No active route")}</h1><p>{zh ? "地图是随证据更新的假设，不是固定课表。每项判断都保留来源。" : "The map is a hypothesis updated by evidence, not a fixed syllabus. Every judgment retains its source."}</p></div><button className="button" type="button" onClick={() => document.querySelector<HTMLButtonElement>('[data-testid="coach-open"]')?.click()}><Sparkles />{zh ? "与 Coach 校准" : "Calibrate with Coach"}</button></header>
    {!demoMode && workspace.error && <section className="card error-note" role="alert">{workspace.error}</section>}
    {!demoMode && workspace.loading && <section className="card" aria-live="polite">{zh ? "正在读取路线修订…" : "Loading route revision…"}</section>}
    {!demoMode && !workspace.loading && !route && <section className="card"><h2>{zh ? "路线尚未就绪" : "The route is not ready yet"}</h2><p>{zh ? "Route Planner 的 proposed revision 完成并接受后会显示在这里。" : "This view appears after Route Planner produces and the user accepts a revision."}</p></section>}
    {(demoMode || route) && <><section className="bridge-banner"><div><span>{zh ? "已有能力" : "What you have"}</span><strong>{demoMode ? "UI state & async flows" : route?.bridge[0]?.from_capability_ids.join(", ") || sourceName}</strong></div><MoveRight /><div><span>{zh ? "正在迁移" : "What is moving"}</span><strong>{demoMode ? "Durable agent lifecycle" : route?.bridge[0]?.title}</strong></div></section>
      <div className="map-layout"><section className="stage-path" aria-label={zh ? "阶段路线" : "Stage route"}>{stages.map((stage, index) => { const state = index === 0 ? "active" : index === 1 ? "next" : "locked"; return <div key={stage.id} className={`stage-node ${state}`}><span className="stage-index">{state === "active" ? <CircleDashed /> : state === "locked" ? <LockKeyhole /> : <Check />}</span><div><p>Stage {index + 1}</p><h2>{stage.title}</h2><span>{stage.outcome}</span>{state === "active" && <div className="progress-track"><span style={{ width: "42%" }} /></div>}</div>{index < stages.length - 1 && <ArrowDown className="stage-arrow" />}</div>; })}</section>
        <aside className="capability-panel card"><p className="section-label">{zh ? "能力依据" : "Capability basis"}</p><h2>{zh ? "当前判断" : "Current judgments"}</h2>{demoMode ? demoCapabilities.map((item) => <div className="map-capability" key={item.id}><div><strong>{item.name}</strong><p>{item.basis}</p></div><LevelBadge locale={locale} level={item.level} /></div>) : assessments.map((item, index) => <div className="map-capability" key={`${index}:${item.capability_ids.join(",")}`}><div><strong>{item.statement}</strong><p>{item.evidence_ids.length ? item.evidence_ids.join(", ") : item.capability_ids.join(", ")}</p></div><LevelBadge locale={locale} level={confidenceLevel[item.confidence]} /></div>)}</aside></div></>}
  </div>;
}
