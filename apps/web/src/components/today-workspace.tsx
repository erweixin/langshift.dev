"use client";

import Link from "next/link";
import { ArrowRight, CalendarDays, CheckCircle2, Clock3, Layers3, Sparkles } from "lucide-react";
import type { Locale } from "@/i18n/config";
import { demoMode } from "@/lib/demo";
import { demoMission, demoTask } from "@/lib/model";
import { useProductWorkspace } from "@/lib/use-product-workspace";
import { WorkspaceLoadError } from "./workspace-load-error";

export function TodayWorkspace({ locale }: { locale: Locale }) {
  const zh = locale === "zh-CN";
  const workspace = useProductWorkspace(locale, !demoMode, true);
  const today = new Intl.DateTimeFormat(locale, { weekday: "short", month: "short", day: "numeric" }).format(new Date());
  const focus = workspace.missions.focus;
  const task = workspace.currentTask;
  const route = workspace.route?.route;
  const latestEvidence = workspace.evidence.find((item) => item.mission_id === focus?.id) ?? workspace.evidence[0];
  const missionName = focus ? workspace.missions.roleNames.get(focus.target_role_profile_id) ?? `${zh ? "成长目标" : "Mission"} ${focus.id.slice(0, 8)}` : "";
  const taskTitle = demoMode ? demoTask.title : task?.task?.title ?? route?.first_task.title;
  const taskWhy = demoMode ? demoTask.why : task?.task?.why_this_task ?? task?.task?.objective ?? route?.first_task.objective;
  const taskMinutes = demoMode ? demoTask.minutes : task?.estimated_minutes ?? route?.first_task.estimated_minutes;
  const criterion = demoMode ? demoTask.judgment : task?.task?.practice.success_criteria?.[0] ?? route?.first_task.success_criteria[0];
  const stageCount = demoMode ? 4 : route?.stages.length ?? 0;
  const activeStage = demoMode ? 1 : stageCount ? 1 : 0;

  return <div className="page-wrap today-page"><header className="page-head"><div><p className="eyebrow">{zh ? "今日一步" : "One step today"}</p><h1>{zh ? "早上好。" : "Good morning."}</h1><p>{zh ? "今天只需要完成一项能验证判断的工作。" : "Today needs one piece of work that can test a judgment."}</p></div><div className="date-pill"><CalendarDays /> {today}</div></header>
    {!demoMode && workspace.error && <WorkspaceLoadError locale={locale} message={workspace.error} loading={workspace.loading} onRetry={workspace.retry} />}
    {!demoMode && workspace.loading && <section className="focus-card" aria-live="polite"><p>{zh ? "正在读取 Focus、路线和今日任务…" : "Loading your Focus, route, and task…"}</p></section>}
    {!demoMode && !workspace.loading && !focus && <section className="focus-card"><div className="focus-content"><div><p className="section-label">Mission</p><h2>{zh ? "先创建一个可执行的成长目标" : "Create an executable growth goal first"}</h2><p>{zh ? "选择当前与目标角色后，Coach 会生成可验证路线。" : "Choose current and target roles, then Coach will build a verifiable route."}</p></div><Link className="button primary" href={`/${locale}/onboarding?new=mission`}>{zh ? "创建 Mission" : "Create Mission"}<ArrowRight /></Link></div></section>}
    {(demoMode || focus && !workspace.loading) && <>
      <section className="focus-card"><div className="focus-meta"><span><Sparkles /> {zh ? "当前 Focus" : "Current Focus"}</span><span>{demoMode ? demoMission.stage : missionName}</span></div><div className="focus-content"><div><p className="section-label">{zh ? "迁移桥任务" : "Bridge task"}</p><h2>{taskTitle ?? (zh ? "等待 Daily Planner 生成任务" : "Waiting for Daily Planner")}</h2><p>{taskWhy ?? (zh ? "路线已就绪后，Planner 会根据 Focus 与最新证据生成唯一今日任务。" : "Once the route is ready, Planner creates one daily task from the Focus and latest evidence.")}</p>{taskMinutes && <div className="task-facts"><span><Clock3 /> {taskMinutes} {zh ? "分钟" : "min"}</span><span><Layers3 /> {task?.practice_kind ?? (zh ? "代码、文字或设计" : "Code, writing, or design")}</span></div>}</div>{task ? <Link className="button primary" href={`/${locale}/task?task=${encodeURIComponent(task.id)}`}>{zh ? "开始任务" : "Start task"}<ArrowRight /></Link> : <Link className="button ghost" href={`/${locale}/map`}>{zh ? "查看路线状态" : "Inspect route status"}<ArrowRight /></Link>}</div>{criterion && <footer><strong>{zh ? "完成标准" : "Definition of done"}</strong><span>{criterion}</span></footer>}</section>
      <div className="grid-2 today-lower"><section className="card"><div className="stat-line"><p className="section-label">{zh ? "路线位置" : "Route position"}</p><span className="chip">{activeStage ? `Stage ${activeStage} of ${stageCount}` : (zh ? "等待路线" : "Route pending")}</span></div><h2>{demoMode ? (zh ? "建立运行时心智模型" : "Build the runtime mental model") : route?.stages[0]?.title ?? (zh ? "路线生成中" : "Route is being generated")}</h2>{stageCount > 0 && <div className="progress-track" role="progressbar" aria-label={`Stage ${activeStage} of ${stageCount}`} aria-valuemin={0} aria-valuemax={stageCount} aria-valuenow={activeStage}><span style={{ width: `${100 / stageCount}%` }} /></div>}<p className="muted">{demoMode ? (zh ? "下一个里程碑：恢复与副作用对账" : "Next milestone: recovery and effect reconciliation") : route?.stages[1]?.outcome ?? route?.summary}</p><Link className="inline-link" href={`/${locale}/map`}>{zh ? "查看迁移地图" : "Open migration map"}<ArrowRight /></Link></section>
        <section className="card soft"><p className="section-label">{zh ? "最近证据" : "Latest evidence"}</p>{latestEvidence ? <div className="evidence-mini"><CheckCircle2 /><div><h3>{latestEvidence.evidence_type}</h3><p>{latestEvidence.source_kind} · {new Intl.DateTimeFormat(locale, { dateStyle: "medium" }).format(new Date(latestEvidence.recorded_at))}</p></div></div> : <p className="muted">{zh ? "完成第一个任务后，证据会显示在这里。" : "Evidence appears here after your first completed task."}</p>}<Link className="inline-link" href={`/${locale}/evidence`}>{zh ? "查看依据" : "Inspect basis"}<ArrowRight /></Link></section></div>
    </>}
  </div>;
}
