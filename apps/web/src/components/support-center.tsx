"use client";

import { FormEvent, useState } from "react";
import { AlertTriangle, CheckCircle2, Clock3, MessageSquareText, RefreshCw, ShieldCheck } from "lucide-react";
import type { Locale } from "@/i18n/config";
import { ApiError, apiRequest, newIdempotencyKey } from "@/lib/api/client";

type SupportMessage = { id: string; author_user_id: string; author_kind: "customer" | "tenant_admin" | "support"; body: string; created_at: string };
type SupportCase = { id: string; reference: string; requester_user_id: string; category: string; priority: string; status: string; subject: string; support_tier: string; version: number; response_due_at: string | null; resolution_due_at: string | null; first_responded_at: string | null; resolved_at: string | null; created_at: string; updated_at: string; messages?: SupportMessage[]; replayed?: boolean };
type State = { kind: "idle" | "working" | "done" | "error"; message?: string };

const caseAccept = "application/vnd.lites.support-case.v2+json";

function failure(error: unknown, zh: boolean) {
  if (error instanceof ApiError) {
    if (error.status === 401) return zh ? "请先登录后再联系支持团队。" : "Sign in before contacting support.";
    if (error.status === 403) return zh ? "当前组织成员身份无权查看该工单。" : "Your current organization membership cannot access that case.";
    if (error.status === 409) return zh ? "工单已更新，请重新打开后再回复。" : "The case changed. Open it again before replying.";
    return `${error.message}${error.requestID ? ` · request ${error.requestID}` : ""}`;
  }
  return error instanceof Error ? error.message : zh ? "请求未完成，请重试。" : "The request did not complete. Try again.";
}

function StateNote({ state }: { state: State }) {
  if (state.kind === "idle" || state.kind === "working") return null;
  return <p className={state.kind === "done" ? "admin-note success" : "admin-note error"} role={state.kind === "error" ? "alert" : "status"}>{state.kind === "done" ? <CheckCircle2 aria-hidden="true" /> : <AlertTriangle aria-hidden="true" />}{state.message}</p>;
}

export function SupportCenter({ locale }: { locale: Locale }) {
  const zh = locale === "zh-CN";
  const [cases, setCases] = useState<SupportCase[] | null>(null);
  const [selected, setSelected] = useState<SupportCase | null>(null);
  const [listState, setListState] = useState<State>({ kind: "idle" });
  const [createState, setCreateState] = useState<State>({ kind: "idle" });
  const [detailState, setDetailState] = useState<State>({ kind: "idle" });
  const [replyState, setReplyState] = useState<State>({ kind: "idle" });
  const [body, setBody] = useState("");
  const [reply, setReply] = useState("");

  async function loadCases() {
    setListState({ kind: "working" });
    try {
      const result = await apiRequest<{ items: SupportCase[] }>("/v1/support/cases", { accept: "application/vnd.lites.support-case-page.v2+json" });
      setCases(result.items);
      setListState({ kind: "done", message: zh ? `已读取 ${result.items.length} 个工单。` : `Loaded ${result.items.length} cases.` });
    } catch (error) {
      setListState({ kind: "error", message: failure(error, zh) });
    }
  }

  async function createCase(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = event.currentTarget;
    const data = new FormData(form);
    const currentBody = body;
    setBody("");
    setCreateState({ kind: "working" });
    try {
      const requestID = newIdempotencyKey();
      const result = await apiRequest<SupportCase>("/v1/support/cases", { method: "POST", idempotencyKey: requestID, accept: caseAccept, contentType: "application/vnd.lites.support-case-create.v2+json", body: { request_id: requestID, category: String(data.get("category")), priority: String(data.get("priority")), subject: String(data.get("subject") ?? "").trim(), body: currentBody.trim() } });
      form.reset();
      setCases((current) => current ? [result, ...current.filter((item) => item.id !== result.id)] : [result]);
      setSelected(result);
      setCreateState({ kind: "done", message: zh ? `工单 ${result.reference} 已安全提交。` : `Case ${result.reference} was submitted securely.` });
    } catch (error) {
      setBody(currentBody);
      setCreateState({ kind: "error", message: failure(error, zh) });
    }
  }

  async function openCase(item: SupportCase) {
    setDetailState({ kind: "working" });
    try {
      const result = await apiRequest<SupportCase>(`/v1/support/cases/${encodeURIComponent(item.id)}`, { accept: caseAccept });
      setSelected(result);
      setCases((current) => current?.map((entry) => entry.id === result.id ? result : entry) ?? [result]);
      setDetailState({ kind: "done" });
    } catch (error) {
      setDetailState({ kind: "error", message: failure(error, zh) });
    }
  }

  async function replyToCase(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!selected) return;
    const currentReply = reply;
    setReply("");
    setReplyState({ kind: "working" });
    try {
      const requestID = newIdempotencyKey();
      const result = await apiRequest<SupportCase>(`/v1/support/cases/${encodeURIComponent(selected.id)}/messages`, { method: "POST", idempotencyKey: requestID, ifMatch: `"${selected.version}"`, accept: caseAccept, contentType: "application/vnd.lites.support-reply.v2+json", body: { request_id: requestID, body: currentReply.trim(), expected_case_version: selected.version } });
      setSelected(result);
      setCases((current) => current?.map((entry) => entry.id === result.id ? result : entry) ?? [result]);
      setReplyState({ kind: "done", message: zh ? "回复已追加到工单。" : "Reply appended to the case." });
    } catch (error) {
      setReply(currentReply);
      setReplyState({ kind: "error", message: failure(error, zh) });
    }
  }

  return <div className="page-wrap support-center"><header className="page-head"><div><p className="eyebrow">Support Center</p><h1>{zh ? "问题有记录，承诺有时钟。" : "Every issue has a record. Every promise has a clock."}</h1><p>{zh ? "工单内容在进入对象存储前加密；组织管理员只能在当前租户边界内查看，支持等级和 SLA 在创建时冻结。" : "Case content is encrypted before object storage. Organization administrators remain tenant-bound, and the support tier and SLA are frozen when the case is created."}</p></div><button className="button" type="button" onClick={loadCases} disabled={listState.kind === "working"}><RefreshCw aria-hidden="true" />{zh ? "读取我的工单" : "Load my cases"}</button></header><StateNote state={listState} />

    <div className="support-layout"><form className="card form-grid support-create" onSubmit={createCase}><div><p className="section-label"><MessageSquareText aria-hidden="true" /> {zh ? "新工单" : "New case"}</p><h2>{zh ? "告诉我们发生了什么" : "Tell us what happened"}</h2></div><label className="field"><span>{zh ? "类别" : "Category"}</span><select name="category"><option value="product">{zh ? "产品" : "Product"}</option><option value="availability">{zh ? "可用性" : "Availability"}</option><option value="contract">{zh ? "合同" : "Contract"}</option><option value="privacy">{zh ? "隐私" : "Privacy"}</option><option value="security">{zh ? "安全" : "Security"}</option></select></label><label className="field"><span>{zh ? "紧急程度" : "Urgency"}</span><select name="priority"><option value="normal">normal</option><option value="low">low</option><option value="high">high</option><option value="urgent">urgent</option></select></label><label className="field"><span>{zh ? "主题" : "Subject"}</span><input name="subject" required maxLength={200} /></label><label className="field"><span>{zh ? "详细信息" : "Details"}</span><textarea name="body" required maxLength={65536} value={body} onChange={(event) => setBody(event.target.value)} placeholder={zh ? "请包含时间、影响范围、可复现步骤和安全的诊断信息。不要粘贴密码、API key 或 token。" : "Include timing, impact, reproduction steps, and safe diagnostics. Never paste passwords, API keys, or tokens."} /></label><p className="support-safety"><ShieldCheck aria-hidden="true" />{zh ? "请勿提交密码、密钥、token 或完整个人数据。" : "Do not submit passwords, credentials, tokens, or full personal datasets."}</p><button className="button primary" type="submit" disabled={!body.trim() || createState.kind === "working"}>{createState.kind === "working" ? (zh ? "正在提交…" : "Submitting…") : (zh ? "安全提交" : "Submit securely")}</button><StateNote state={createState} /></form>

      <section className="card support-list"><div><p className="section-label">{zh ? "工单" : "Cases"}</p><h2>{zh ? "租户范围内可追溯" : "Traceable within your tenant"}</h2></div>{cases === null ? <p className="muted">{zh ? "工单不会自动读取。点击“读取我的工单”发起一次明确查询。" : "Cases are not read automatically. Use “Load my cases” to make an explicit query."}</p> : cases.length === 0 ? <p className="muted">{zh ? "暂无工单。" : "No cases yet."}</p> : <div className="support-case-list">{cases.map((item) => <button type="button" key={item.id} className={selected?.id === item.id ? "support-case active" : "support-case"} onClick={() => openCase(item)}><span><strong>{item.subject}</strong><small>{item.reference} · {item.category}</small></span><span><span className="chip">{item.status}</span><small>v{item.version}</small></span></button>)}</div>}<StateNote state={detailState} /></section>
    </div>

    {selected && <section className="card support-detail"><header><div><p className="section-label">{selected.reference}</p><h2>{selected.subject}</h2><p className="muted">{selected.category} · {selected.priority} · {selected.support_tier}</p></div><span className="chip">{selected.status}</span></header><div className="support-clocks"><span><Clock3 aria-hidden="true" /><span>{zh ? "首次响应" : "First response"}<strong>{selected.first_responded_at ? new Date(selected.first_responded_at).toLocaleString(locale) : selected.response_due_at ? `${zh ? "应于" : "Due"} ${new Date(selected.response_due_at).toLocaleString(locale)}` : (zh ? "无合同承诺" : "No contractual target")}</strong></span></span><span><Clock3 aria-hidden="true" /><span>{zh ? "解决目标" : "Resolution target"}<strong>{selected.resolved_at ? new Date(selected.resolved_at).toLocaleString(locale) : selected.resolution_due_at ? `${zh ? "应于" : "Due"} ${new Date(selected.resolution_due_at).toLocaleString(locale)}` : (zh ? "无合同承诺" : "No contractual target")}</strong></span></span></div><div className="support-thread">{(selected.messages ?? []).map((message) => <article key={message.id} className={message.author_kind === "support" ? "support-message agent" : "support-message"}><header><strong>{message.author_kind === "support" ? "Lites Support" : message.author_kind === "tenant_admin" ? (zh ? "组织管理员" : "Organization admin") : (zh ? "客户" : "Customer")}</strong><time dateTime={message.created_at}>{new Date(message.created_at).toLocaleString(locale)}</time></header><p>{message.body}</p></article>)}</div>{selected.status !== "closed" && <form className="form-grid support-reply" onSubmit={replyToCase}><label className="field"><span>{zh ? "追加回复" : "Add a reply"}</span><textarea required maxLength={65536} value={reply} onChange={(event) => setReply(event.target.value)} /></label><button className="button primary" disabled={!reply.trim() || replyState.kind === "working"}>{replyState.kind === "working" ? (zh ? "正在追加…" : "Appending…") : (zh ? "追加到工单" : "Append to case")}</button><StateNote state={replyState} /></form>}</section>}
  </div>;
}
