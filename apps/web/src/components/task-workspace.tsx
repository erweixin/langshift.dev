"use client";

import { FormEvent, useEffect, useState } from "react";
import { AlertCircle, ArrowLeft, CheckCircle2, Clock3, CloudOff, FileCode2, LoaderCircle, RotateCcw, Send, Sparkles } from "lucide-react";
import type { Locale } from "@/i18n/config";
import { apiRequest, newIdempotencyKey } from "@/lib/api/client";
import { demoTask } from "@/lib/model";

type Phase = "work" | "submitting" | "queued" | "reviewed" | "error";
const draftKey = "lites:draft:demo-task-run-state";

export function TaskWorkspace({ locale }: { locale: Locale }) {
  const zh = locale === "zh-CN";
  const [content, setContent] = useState("");
  const [understanding, setUnderstanding] = useState("");
  const [phase, setPhase] = useState<Phase>("work");
  const [restored, setRestored] = useState(false);
  const [error, setError] = useState("");
  const [submissionID, setSubmissionID] = useState("");
  const ready = content.trim().length > 0 && understanding.trim().length > 0;

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
  }, []);
  useEffect(() => {
    if (!content && !understanding) return;
    const timer = window.setTimeout(() => localStorage.setItem(draftKey, JSON.stringify({ content, understanding, savedAt: Date.now() })), 350);
    return () => window.clearTimeout(timer);
  }, [content, understanding]);
  useEffect(() => {
    if ((!content && !understanding) || phase === "queued" || phase === "reviewed") return;
    const warn = (event: BeforeUnloadEvent) => event.preventDefault();
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [content, understanding, phase]);

  async function submit(event: FormEvent) {
    event.preventDefault(); if (!ready || phase === "submitting") return;
    if (!navigator.onLine) { setError(zh ? "当前离线：草稿仍在本机，尚未提交。" : "You are offline: the draft remains on this device and is not submitted."); setPhase("error"); return; }
    setPhase("submitting"); setError("");
    try {
      if (process.env.NEXT_PUBLIC_LITES_DEMO_MODE === "true") {
        await new Promise((resolve) => window.setTimeout(resolve, 500));
        setSubmissionID("demo-submission"); setPhase("queued");
        await new Promise((resolve) => window.setTimeout(resolve, 900));
        setPhase("reviewed"); localStorage.removeItem(draftKey); return;
      }
      const submission = await apiRequest<{ id: string; version: number }>("/v1/submissions", { method: "POST", idempotencyKey: newIdempotencyKey(), body: { request_id: newIdempotencyKey(), daily_task_id: demoTask.id, submission_kind: "writing", content, understanding } });
      setSubmissionID(submission.id);
      await apiRequest("/v1/reviews", { method: "POST", idempotencyKey: newIdempotencyKey(), ifMatch: `\"${submission.version}\"`, body: { request_id: newIdempotencyKey(), submission_id: submission.id, rubric_version_id: "task-run-state-v1", expected_submission_revision: submission.version } });
      setPhase("queued"); localStorage.removeItem(draftKey);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "request_failed"); setPhase("error");
    }
  }

  if (phase === "reviewed") return <div className="page-wrap review-result"><a href={`/${locale}/today`} className="inline-link"><ArrowLeft />{zh ? "返回今日" : "Back to Today"}</a><section className="review-hero card"><span className="review-seal"><CheckCircle2 /></span><p className="eyebrow">{zh ? "评审已完成" : "Review complete"}</p><h1>{zh ? "你证明了状态机判断，\n也暴露了一个边界。" : "You proved the state-machine judgment—and exposed one boundary."}</h1><p>{zh ? "这项结果已关联到证据记录；它不会自动夸大你的能力等级。" : "This result is linked to your evidence record; it does not automatically overstate your capability level."}</p></section><div className="grid-2"><section className="card"><p className="section-label">{zh ? "做得好的地方" : "What held up"}</p><h2>{zh ? "转换规则清晰且可测试" : "Transition rules are explicit and testable"}</h2><p className="muted">{zh ? "你把 completed → running 作为非法转换，而不是在调用方约定不这样做。" : "You made completed → running an invalid transition rather than relying on callers to behave."}</p></section><section className="card"><p className="section-label">{zh ? "下一处边界" : "Next boundary"}</p><h2>{zh ? "重试与副作用仍需分离" : "Retry and effects still need separation"}</h2><p className="muted">{zh ? "下一项任务将验证崩溃发生在外部效果之后时如何恢复。" : "The next task will test recovery when a crash occurs after an external effect."}</p></section></div><section className="card review-evidence"><div><p className="section-label">Evidence</p><h2>Durable run lifecycle</h2><p>{zh ? "依据：本次提交和 rubric 评审。等级：已展示。" : "Basis: this submission and rubric review. Level: Demonstrated."}</p></div><span className="chip">{zh ? "已展示" : "Demonstrated"}</span></section><a className="button primary" href={`/${locale}/evidence`}>{zh ? "查看成长记录" : "Open evidence record"}</a></div>;

  return <div className="task-page"><aside className="task-brief"><a href={`/${locale}/today`} className="inline-link"><ArrowLeft />{zh ? "今日一步" : "Today"}</a><p className="eyebrow">{zh ? "任务简报" : "Task brief"}</p><h1>{demoTask.title}</h1><p>{demoTask.why}</p><div className="brief-block"><strong>{zh ? "完成标准" : "Definition of done"}</strong><p>{demoTask.judgment}</p></div><div className="task-facts"><span><Clock3 /> {demoTask.minutes} {zh ? "分钟" : "min"}</span><span><FileCode2 /> {zh ? "代码或文字" : "Code or writing"}</span></div><div className="coach-nudge"><Sparkles /><p><strong>Coach</strong><br />{zh ? "先写出允许的转换，再问谁拥有改变状态的权限。" : "Write allowed transitions first, then ask who owns the authority to change state."}</p></div></aside>
    <section className="task-editor" aria-labelledby="task-editor-title"><header><div><p className="section-label">Workspace</p><h2 id="task-editor-title">{zh ? "你的解答" : "Your response"}</h2></div><span className="draft-status"><CloudOff />{zh ? "本机草稿 · 自动保存" : "Device draft · autosaved"}</span></header>{restored && <div className="draft-restored" role="status">{zh ? "已恢复上次未提交的本机草稿。" : "Restored an unsubmitted draft from this device."}<button type="button" aria-label={zh ? "关闭草稿恢复提示" : "Dismiss draft restoration notice"} onClick={() => setRestored(false)}>×</button></div>}
      <form onSubmit={submit} className="task-form"><label className="field"><span>{zh ? "状态与转换" : "State and transition model"}</span><textarea name="task-content" autoComplete="off" required data-testid="task-content" value={content} onChange={(e) => setContent(e.target.value)} placeholder={zh ? "描述状态、允许的转换和拒绝规则…" : "Describe the states, allowed transitions, and rejection rules…"} /></label><label className="field"><span>{zh ? "你的理解" : "Your understanding"}</span><textarea name="understanding" autoComplete="off" required value={understanding} onChange={(e) => setUnderstanding(e.target.value)} placeholder={zh ? "为什么 completed → running 必须被拒绝？" : "Why must completed → running be rejected?"} /></label>{phase === "error" && <div className="submit-error" role="alert"><AlertCircle /> <span>{error}</span><button type="button" className="text-button" onClick={() => setPhase("work")}><RotateCcw />{zh ? "返回修改" : "Return to draft"}</button></div>}{phase === "queued" && <div className="queued-state" role="status" aria-live="polite"><LoaderCircle className="spinner-icon" /><div><strong>{zh ? "提交已确认，评审排队中" : "Submission confirmed; review is queued"}</strong><p>{zh ? `提交 ${submissionID} 已由服务器确认。评审结果会通过实时事件到达。` : `Submission ${submissionID} is server-confirmed. Review results arrive through realtime events.`}</p></div></div>}<footer><p>{zh ? "只有服务器确认后才算已提交。" : "Nothing is submitted until the server confirms it."}</p><button className="button primary" type="submit" disabled={phase === "submitting" || phase === "queued"}>{phase === "submitting" ? <><LoaderCircle className="spinner-icon" />{zh ? "正在确认…" : "Confirming…"}</> : <><Send />{zh ? "提交并请求评审" : "Submit for review"}</>}</button></footer></form>
    </section></div>;
}
