"use client";

import { FormEvent, useEffect, useState } from "react";
import { useSearchParams } from "next/navigation";
import { AlertCircle, ArrowLeft, CheckCircle2, Clock3, CloudOff, FileCode2, LoaderCircle, RotateCcw, Send, Sparkles } from "lucide-react";
import type { Locale } from "@/i18n/config";
import { apiRequest, newIdempotencyKey } from "@/lib/api/client";
import { demoMode } from "@/lib/demo";
import { demoTask } from "@/lib/model";
import { useProductWorkspace, type DailyTask } from "@/lib/use-product-workspace";
import { WorkspaceLoadError } from "./workspace-load-error";

type Phase = "work" | "submitting" | "queued" | "reviewed" | "error";
type ReviewDocument = { schema_version: 1; verdict: "pass" | "needs_revision"; summary: string; deterministic_results: { checks: Array<{ name: string; status: string; evidence: string }> }; dimensions: Array<{ id: string; score: number; rationale: string; evidence_quotes: string[] }>; strengths: string[]; improvements: string[]; capability_evidence: Array<{ capability_id: string; level: string; statement: string }>; next_action: string; uncertainty: string };
type ReviewResource = { id: string; status: string; evidence_id: string; review: ReviewDocument; reviewed_at: string };
type ReviewRecovery = NonNullable<DailyTask["review_recovery"]>;

export function TaskWorkspace({ locale }: { locale: Locale }) {
  const zh = locale === "zh-CN";
  const query = useSearchParams();
  const workspace = useProductWorkspace(locale, !demoMode);
  const requestedTaskID = query.get("task");
  const task = workspace.currentRouteTasks.find((item) => item.id === requestedTaskID) ?? workspace.currentTask;
  const [content, setContent] = useState("");
  const [understanding, setUnderstanding] = useState("");
  const [phase, setPhase] = useState<Phase>("work");
  const [restored, setRestored] = useState(false);
  const [error, setError] = useState("");
  const [submissionID, setSubmissionID] = useState("");
  const [taskVersion, setTaskVersion] = useState<number | null>(null);
  const [taskStatus, setTaskStatus] = useState<string | null>(null);
  const [minutes, setMinutes] = useState<number | null>(demoMode ? demoTask.minutes : null);
  const [easier, setEasier] = useState(false);
  const [review, setReview] = useState<ReviewResource | null>(null);
  const activeVersion = taskVersion ?? task?.version ?? 0;
  const activeStatus = taskStatus ?? task?.status ?? "";
  const taskDocument = task?.task;
  const draftKey = `lites:draft:${demoMode ? demoTask.id : task?.id ?? "unresolved"}`;
  const ready = content.trim().length > 0 && understanding.trim().length > 0 && (demoMode || activeStatus === "in_progress");

  useEffect(() => {
    const raw = localStorage.getItem(draftKey);
    if (!raw) return;
    const frame = window.requestAnimationFrame(() => {
      try {
        const draft = JSON.parse(raw) as { content?: string; understanding?: string };
        setContent(draft.content ?? ""); setUnderstanding(draft.understanding ?? ""); setRestored(Boolean(draft.content || draft.understanding));
      } catch { localStorage.removeItem(draftKey); }
    });
    return () => window.cancelAnimationFrame(frame);
  }, [draftKey]);
  useEffect(() => {
    if (!content && !understanding) return;
    const timer = window.setTimeout(() => localStorage.setItem(draftKey, JSON.stringify({ content, understanding, savedAt: Date.now() })), 350);
    return () => window.clearTimeout(timer);
  }, [content, draftKey, understanding]);
  useEffect(() => {
    if ((!content && !understanding) || phase === "queued" || phase === "reviewed") return;
    const warn = (event: BeforeUnloadEvent) => event.preventDefault();
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [content, understanding, phase]);

  async function readTask(taskID: string, signal?: AbortSignal) {
    const taskList = await apiRequest<{ items: DailyTask[] }>("/v1/daily-tasks", { accept: "application/vnd.lites.daily-tasks.v2+json", signal });
    return taskList.items.find((item) => item.id === taskID) ?? null;
  }

  async function finishReview(updated: DailyTask, reviewID: string, signal?: AbortSignal) {
    const completed = await apiRequest<ReviewResource>(`/v1/reviews/${reviewID}`, { accept: "application/vnd.lites.review.v2+json", signal });
    setReview(completed);
    setSubmissionID(updated.review_recovery?.submission_id ?? updated.current_submission_id ?? "");
    setTaskVersion(updated.version);
    setTaskStatus(updated.status);
    setPhase("reviewed");
    localStorage.removeItem(draftKey);
  }

  async function pollReview(taskID: string, signal?: AbortSignal) {
    setPhase("queued");
    for (let attempt = 0; attempt < 120; attempt += 1) {
      const updated = await readTask(taskID, signal);
      if (!updated) throw new Error("daily_task_not_found");
      setTaskVersion(updated.version);
      setTaskStatus(updated.status);
      const recovery = updated.review_recovery;
      const reviewID = updated.current_review_id ?? recovery?.review_id;
      if (reviewID) {
        await finishReview(updated, reviewID, signal);
        return;
      }
      if (recovery?.generation_status === "failed" || recovery?.generation_status === "superseded") {
        throw new Error(recovery.failure_reason ? `review_${recovery.failure_reason}` : `review_${recovery.generation_status}`);
      }
      await new Promise((resolve) => window.setTimeout(resolve, 1500));
    }
    throw new Error("review_timeout");
  }

  async function admitReview(current: DailyTask, recovery: ReviewRecovery, signal?: AbortSignal) {
    const rubric = workspace.missions.rubrics.find((item) => item.practice_kind === current.practice_kind);
    const rubricID = recovery.rubric_version_id ?? rubric?.id;
    if (!rubricID) throw new Error("rubric_revision_unavailable");
    await apiRequest("/v1/reviews", {
      method: "POST",
      signal,
      contentType: "application/vnd.lites.review-generate.v2+json",
      idempotencyKey: newIdempotencyKey(),
      ifMatch: `"${current.version}"`,
      body: {
        request_id: newIdempotencyKey(),
        submission_id: recovery.submission_id,
        rubric_version_id: rubricID,
        expected_submission_revision: recovery.submission_revision,
        expected_task_version: current.version,
      },
    });
    setSubmissionID(recovery.submission_id);
    localStorage.removeItem(draftKey);
    await pollReview(current.id, signal);
  }

  async function restoreReview(current: DailyTask, signal?: AbortSignal) {
    try {
      setError("");
      const recovery = current.review_recovery;
      const reviewID = current.current_review_id ?? recovery?.review_id;
      if (reviewID) {
        await finishReview(current, reviewID, signal);
        return;
      }
      if (!recovery) throw new Error("submission_recovery_unavailable");
      setSubmissionID(recovery.submission_id);
      setTaskVersion(current.version);
      setTaskStatus(current.status);
      if (recovery.generation_status === "generating") {
        await pollReview(current.id, signal);
        return;
      }
      if (recovery.generation_status === "failed" || recovery.generation_status === "superseded") {
        throw new Error(recovery.failure_reason ? `review_${recovery.failure_reason}` : `review_${recovery.generation_status}`);
      }
      if (!recovery.generation_id && current.status === "submitted") {
        await admitReview(current, recovery, signal);
        return;
      }
      throw new Error("review_recovery_state_invalid");
    } catch (cause) {
      if (cause instanceof DOMException && cause.name === "AbortError") return;
      setError(cause instanceof Error ? cause.message : "review_recovery_failed");
      setPhase("error");
    }
  }

  async function retryReview() {
    if (!task) return;
    setError("");
    setPhase("submitting");
    try {
      const current = await readTask(task.id);
      if (!current) throw new Error("daily_task_not_found");
      const reviewID = current.current_review_id ?? current.review_recovery?.review_id;
      if (reviewID) {
        await finishReview(current, reviewID);
        return;
      }
      const recovery = current.review_recovery;
      if (!recovery) throw new Error("submission_recovery_unavailable");
      if (recovery.generation_status === "generating") await pollReview(current.id);
      else await admitReview(current, recovery);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "review_retry_failed");
      setPhase("error");
    }
  }

  useEffect(() => {
    if (demoMode || workspace.loading || !task || !["submitted", "reviewing", "completed"].includes(task.status)) return;
    const controller = new AbortController();
    queueMicrotask(() => void restoreReview(task, controller.signal));
    return () => controller.abort();
  // The task identity/version is the server continuation token. Local phase is
  // intentionally excluded so polling state cannot restart the recovery effect.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [task?.id, task?.version, workspace.loading]);

  async function mutateTask(action: "start" | "lower_difficulty") {
    if (!task || !activeVersion) return;
    const result = await apiRequest<{ version: number; status: string }>(`/v1/daily-tasks/${task.id}`, { method: "PATCH", contentType: "application/vnd.lites.daily-task-update.v2+json", idempotencyKey: newIdempotencyKey(), ifMatch: `"${activeVersion}"`, body: { request_id: newIdempotencyKey(), action, expected_task_version: activeVersion, reschedule_for: "" } });
    setTaskVersion(result.version); setTaskStatus(result.status);
  }

  async function beginTask() {
    setError("");
    try { await mutateTask("start"); } catch (cause) { setError(cause instanceof Error ? cause.message : "task_start_failed"); setPhase("error"); }
  }

  async function submit(event: FormEvent) {
    event.preventDefault(); if (!ready || phase === "submitting") return;
    if (!navigator.onLine) { setError(zh ? "当前离线：草稿仍在本机，尚未提交。" : "You are offline: the draft remains on this device and is not submitted."); setPhase("error"); return; }
    setPhase("submitting"); setError("");
    try {
      if (demoMode) {
        await new Promise((resolve) => window.setTimeout(resolve, 500));
        setSubmissionID("demo-submission"); setPhase("queued");
        await new Promise((resolve) => window.setTimeout(resolve, 900));
        setPhase("reviewed"); localStorage.removeItem(draftKey); return;
      }
      if (!task) throw new Error("daily_task_required");
      const submission = await apiRequest<{ id: string; submission_revision: number; daily_task_version: number }>("/v1/submissions", { method: "POST", contentType: "application/vnd.lites.submission-create.v2+json", idempotencyKey: newIdempotencyKey(), ifMatch: `"${activeVersion}"`, body: { request_id: newIdempotencyKey(), daily_task_id: task.id, submission_kind: task.practice_kind, content, understanding, expected_task_version: activeVersion } });
      setSubmissionID(submission.id); setTaskVersion(submission.daily_task_version); setTaskStatus("submitted");
      const recovery: ReviewRecovery = { submission_id: submission.id, submission_revision: submission.submission_revision, generation_id: null, run_id: null, rubric_version_id: null, generation_status: null, review_id: null, evidence_id: null, failure_reason: null, generation_created_at: null, generation_updated_at: null, generation_completed_at: null };
      await admitReview({ ...task, version: submission.daily_task_version, status: "submitted", current_submission_id: submission.id, review_recovery: recovery }, recovery);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "request_failed"); setPhase("error");
    }
  }

  async function lowerDifficulty() {
    if (easier || !navigator.onLine) {
      if (!navigator.onLine) { setError(zh ? "离线时不能更改服务器任务难度。" : "Task difficulty cannot be changed on the server while offline."); setPhase("error"); }
      return;
    }
    try {
      if (demoMode) await new Promise((resolve) => window.setTimeout(resolve, 300));
      else await mutateTask("lower_difficulty");
      setMinutes(demoMode ? 15 : Math.max(5, Math.floor((minutes ?? task?.estimated_minutes ?? 30) * 2 / 3))); setEasier(true);
    } catch (cause) { setError(cause instanceof Error ? cause.message : "difficulty_update_failed"); setPhase("error"); }
  }

  if (!demoMode && workspace.loading) return <div className="page-wrap"><section className="card">{zh ? "正在读取今日任务…" : "Loading today’s task…"}</section></div>;
  if (!demoMode && workspace.error && !task) return <div className="page-wrap"><WorkspaceLoadError locale={locale} message={workspace.error} loading={workspace.loading} onRetry={workspace.retry} /></div>;
  if (!demoMode && !task) return <div className="page-wrap"><a href={`/${locale}/today`} className="inline-link"><ArrowLeft />{zh ? "返回今日" : "Back to Today"}</a><section className="card"><h1>{zh ? "没有可执行任务" : "No executable task"}</h1><p>{zh ? "请先激活路线并等待 Daily Planner。" : "Activate a route and wait for Daily Planner first."}</p><button type="button" className="button" onClick={() => void workspace.retry()}>{zh ? "重新读取" : "Reload tasks"}</button></section></div>;

  if (phase === "reviewed") {
    const document = review?.review;
    return <div className="page-wrap review-result"><a href={`/${locale}/today`} className="inline-link"><ArrowLeft />{zh ? "返回今日" : "Back to Today"}</a><section className="review-hero card"><span className="review-seal"><CheckCircle2 /></span><p className="eyebrow">{zh ? "评审已完成" : "Review complete"}</p><h1>{demoMode ? (zh ? "你证明了状态机判断，也暴露了一个边界。" : "You proved the state-machine judgment—and exposed one boundary.") : document?.summary}</h1><p>{zh ? "结果已关联到不可变证据记录，不会自动夸大能力等级。" : "The result is linked to immutable evidence and does not automatically overstate capability."}</p></section><div className="grid-2"><section className="card"><p className="section-label">{zh ? "做得好的地方" : "What held up"}</p><h2>{demoMode ? (zh ? "转换规则清晰且可测试" : "Transition rules are explicit and testable") : document?.strengths[0] ?? document?.dimensions[0]?.rationale}</h2></section><section className="card"><p className="section-label">{zh ? "下一处边界" : "Next boundary"}</p><h2>{demoMode ? (zh ? "重试与副作用仍需分离" : "Retry and effects still need separation") : document?.improvements[0] ?? document?.next_action}</h2></section></div><section className="card review-evidence"><div><p className="section-label">Evidence</p><h2>{document?.capability_evidence[0]?.capability_id ?? "Durable run lifecycle"}</h2><p>{demoMode ? (zh ? "依据：本次提交和 rubric 评审。等级：已展示。" : "Basis: this submission and rubric review. Level: Demonstrated.") : document?.capability_evidence[0]?.statement}</p></div><span className="chip">{document?.capability_evidence[0]?.level ?? (zh ? "已展示" : "Demonstrated")}</span></section><a className="button primary" href={`/${locale}/evidence`}>{zh ? "查看成长记录" : "Open evidence record"}</a></div>;
  }

  const title = demoMode ? demoTask.title : taskDocument?.title ?? "";
  const why = demoMode ? demoTask.why : taskDocument?.why_this_task ?? "";
  const criterion = demoMode ? demoTask.judgment : taskDocument?.practice.success_criteria[0] ?? "";
  const displayedMinutes = minutes ?? task?.estimated_minutes ?? taskDocument?.estimated_minutes;
  return <div className="task-page"><aside className="task-brief"><a href={`/${locale}/today`} className="inline-link"><ArrowLeft />{zh ? "今日一步" : "Today"}</a><p className="eyebrow">{zh ? "任务简报" : "Task brief"}</p><h1>{title}</h1><p>{why}</p><div className="brief-block"><strong>{zh ? "完成标准" : "Definition of done"}</strong><p>{criterion}</p></div><div className="task-facts"><span><Clock3 /> {displayedMinutes} {zh ? "分钟" : "min"}</span><span><FileCode2 /> {demoMode ? (zh ? "代码或文字" : "Code or writing") : task?.practice_kind}</span></div><button className="button ghost difficulty-button" type="button" onClick={lowerDifficulty} disabled={easier}>{easier ? (zh ? "已调整为更容易" : "Adjusted to easier") : (zh ? "降低难度" : "Make this easier")}</button><div className="coach-nudge"><Sparkles /><p><strong>Coach</strong><br />{demoMode ? (zh ? "先写出允许的转换，再问谁拥有改变状态的权限。" : "Write allowed transitions first, then ask who owns the authority to change state.") : taskDocument?.explanation[0]?.content}</p></div></aside>
    <section className="task-editor" aria-labelledby="task-editor-title"><header><div><p className="section-label">Workspace</p><h2 id="task-editor-title">{zh ? "你的解答" : "Your response"}</h2></div><span className="draft-status"><CloudOff />{zh ? "本机草稿 · 自动保存" : "Device draft · autosaved"}</span></header>{restored && <div className="draft-restored" role="status">{zh ? "已恢复上次未提交的本机草稿。" : "Restored an unsubmitted draft from this device."}<button type="button" aria-label={zh ? "关闭草稿恢复提示" : "Dismiss draft restoration notice"} onClick={() => setRestored(false)}>×</button></div>}
      {!demoMode && activeStatus === "scheduled" && <section className="card"><p>{zh ? "开始后任务状态会由服务器确认，再开放提交。" : "Start the task to confirm server state before submission."}</p><button className="button primary" type="button" onClick={() => void beginTask()}>{zh ? "开始任务" : "Begin task"}</button></section>}
      <form onSubmit={submit} className="task-form"><label className="field"><span>{zh ? "工作成果" : "Work output"}</span><textarea name="task-content" autoComplete="off" required data-testid="task-content" value={content} onChange={(event) => setContent(event.target.value)} placeholder={taskDocument?.practice.instructions ?? (zh ? "描述状态、允许的转换和拒绝规则…" : "Describe the states, allowed transitions, and rejection rules…")} /></label><label className="field"><span>{zh ? "你的理解" : "Your understanding"}</span><textarea name="understanding" autoComplete="off" required value={understanding} onChange={(event) => setUnderstanding(event.target.value)} placeholder={zh ? "说明你的关键判断、边界与权衡…" : "Explain your key judgment, boundaries, and trade-offs…"} /></label>{phase === "error" && <div className="submit-error" role="alert"><AlertCircle /> <span>{error}</span>{activeStatus === "submitted" || activeStatus === "reviewing" ? <button type="button" className="text-button" onClick={() => void retryReview()}><RotateCcw />{zh ? "重试评审" : "Retry review"}</button> : <button type="button" className="text-button" onClick={() => setPhase("work")}><RotateCcw />{zh ? "返回修改" : "Return to draft"}</button>}</div>}{phase === "queued" && <div className="queued-state" role="status" aria-live="polite"><LoaderCircle className="spinner-icon" /><div><strong>{zh ? "提交已确认，评审排队中" : "Submission confirmed; review is queued"}</strong><p>{zh ? `提交 ${submissionID} 已由服务器确认；刷新后仍会从服务器继续。` : `Submission ${submissionID} is server-confirmed and will resume after refresh.`}</p></div></div>}<footer><p>{zh ? "只有服务器确认后才算已提交。" : "Nothing is submitted until the server confirms it."}</p><button className="button primary" type="submit" disabled={!ready || phase === "submitting" || phase === "queued"}>{phase === "submitting" ? <><LoaderCircle className="spinner-icon" />{zh ? "正在确认…" : "Confirming…"}</> : <><Send />{zh ? "提交并请求评审" : "Submit for review"}</>}</button></footer></form>
    </section></div>;
}
