"use client";

import { FormEvent, useEffect, useRef, useState } from "react";
import { LoaderCircle, Send, Sparkles, X } from "lucide-react";
import type { Locale } from "@/i18n/config";
import type { Dictionary } from "@/i18n/dictionaries";
import { ApiError, apiRequest, newIdempotencyKey } from "@/lib/api/client";
import { demoMode } from "@/lib/demo";

type Message = { id: string; role: "coach" | "user"; text: string };
type Conversation = {
  id: string;
  mission_id: string;
  version: number;
  mode: "coach";
  status: "active" | "archived";
  messages: { id: string; run_id: string; role: "user" | "assistant"; content: string; created_at: string }[];
};

const terminalFailures = new Set(["failed", "cancelled", "expired"]);

function wait(milliseconds: number, signal: AbortSignal) {
  return new Promise<void>((resolve, reject) => {
    const timer = window.setTimeout(resolve, milliseconds);
    signal.addEventListener("abort", () => { window.clearTimeout(timer); reject(new DOMException("Aborted", "AbortError")); }, { once: true });
  });
}

export function CoachDrawer({ dictionary, context, missionID, locale }: { dictionary: Dictionary; context: string; missionID: string; locale: Locale }) {
  const zh = locale === "zh-CN";
  const welcome = zh ? "我在这里。可以询问当前步骤，或说说你最想拆解的部分。" : "I’m here. Ask about the current step, or select something you want to unpack.";
  const [open, setOpen] = useState(false);
  const [messages, setMessages] = useState<Message[]>([{ id: "welcome", role: "coach", text: welcome }]);
  const [draft, setDraft] = useState("");
  const [conversation, setConversation] = useState<{ id: string; version: number } | null>(null);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  const closeRef = useRef<HTMLButtonElement>(null);
  const requestRef = useRef<AbortController | null>(null);

  useEffect(() => {
    const listener = (event: KeyboardEvent) => {
      if (event.key === "Escape") setOpen(false);
    };
    window.addEventListener("keydown", listener);
    return () => window.removeEventListener("keydown", listener);
  }, []);
  useEffect(() => {
    if (open) closeRef.current?.focus();
  }, [open]);
  useEffect(() => {
    requestRef.current?.abort();
    requestRef.current = null;
	queueMicrotask(() => {
	  setConversation(null);
	  setMessages([{ id: "welcome", role: "coach", text: welcome }]);
	  setPending(false);
	  setError("");
	});
  }, [missionID, welcome]);
  useEffect(() => () => requestRef.current?.abort(), []);

  function applyConversation(resource: Conversation) {
    setConversation({ id: resource.id, version: resource.version });
    const projected = resource.messages.map<Message>((message) => ({ id: message.id, role: message.role === "assistant" ? "coach" : "user", text: message.content }));
    setMessages(projected.length > 0 ? projected : [{ id: "welcome", role: "coach", text: welcome }]);
  }

  async function ensureConversation(signal: AbortSignal) {
    if (conversation) return conversation;
    const storageKey = `lites.coach.conversation.${missionID}`;
    const remembered = window.localStorage.getItem(storageKey);
    if (remembered) {
      try {
        const existing = await apiRequest<Conversation>(`/v1/conversations/${encodeURIComponent(remembered)}?limit=50`, { signal });
        if (existing.mission_id === missionID && existing.mode === "coach" && existing.status === "active") {
          applyConversation(existing);
          return { id: existing.id, version: existing.version };
        }
      } catch (caught) {
        if (!(caught instanceof ApiError) || caught.status !== 404) throw caught;
      }
      window.localStorage.removeItem(storageKey);
    }
    const requestID = newIdempotencyKey();
    const created = await apiRequest<{ id: string; version: number }>("/v1/conversations", { method: "POST", idempotencyKey: newIdempotencyKey(), signal, body: { request_id: requestID, mission_id: missionID, title: null, mode: "coach" } });
    window.localStorage.setItem(storageKey, created.id);
    setConversation({ id: created.id, version: created.version });
    return { id: created.id, version: created.version };
  }

  async function ask(text: string) {
    const value = text.trim();
    if (!value || pending || !missionID) return;
    setDraft("");
    setError("");
    if (demoMode) {
      setMessages((current) => [...current, { id: crypto.randomUUID(), role: "user", text: value }, { id: crypto.randomUUID(), role: "coach", text: "I’ll keep this grounded in your current Mission and the evidence attached to this task. What part feels least clear?" }]);
      return;
    }
    setPending(true);
    const controller = new AbortController();
    requestRef.current?.abort();
    requestRef.current = controller;
    try {
      const current = await ensureConversation(controller.signal);
      setMessages((items) => items[0]?.id === "welcome" ? [{ id: crypto.randomUUID(), role: "user", text: value }] : [...items, { id: crypto.randomUUID(), role: "user", text: value }]);
      const requestID = newIdempotencyKey();
      const accepted = await apiRequest<{ run_id: string; status: string; accepted_at: string }>("/v1/messages", {
        method: "POST", idempotencyKey: newIdempotencyKey(), ifMatch: `"${current.version}"`, signal: controller.signal,
        body: { request_id: requestID, conversation_id: current.id, content: value, mode: "enqueue", expected_conversation_version: current.version },
      });
      setConversation({ id: current.id, version: current.version + 1 });
      for (let attempt = 0; attempt < 90; attempt += 1) {
        const resource = await apiRequest<Conversation>(`/v1/conversations/${encodeURIComponent(current.id)}?limit=50`, { signal: controller.signal });
        const answer = resource.messages.find((message) => message.run_id === accepted.run_id && message.role === "assistant");
        if (answer) {
          applyConversation(resource);
          return;
        }
        const run = await apiRequest<{ status: string }>(`/v1/runs/${encodeURIComponent(accepted.run_id)}`, { signal: controller.signal });
        if (terminalFailures.has(run.status)) throw new Error(`run_${run.status}`);
        await wait(1000, controller.signal);
      }
      throw new Error("run_timeout");
    } catch (caught) {
      if (caught instanceof DOMException && caught.name === "AbortError") return;
      setError(zh ? "Coach 暂时无法完成回复。你的消息已安全保留，可以稍后重试。" : "Coach could not complete the response. Your message is safely retained; try again shortly.");
    } finally {
      if (requestRef.current === controller) requestRef.current = null;
      setPending(false);
    }
  }

  const submit = (event: FormEvent) => {
    event.preventDefault();
    void ask(draft);
  };
  return (
    <>
      <button className="coach-fab" type="button" onClick={() => setOpen(true)} aria-label={dictionary.coach.open} aria-haspopup="dialog" aria-expanded={open} data-testid="coach-open">
        <Sparkles size={18} aria-hidden="true" /><span>{dictionary.coach.open}</span>
      </button>
      {open && <button className="drawer-scrim" type="button" aria-label={dictionary.coach.close} onClick={() => setOpen(false)} />}
      {open && <aside className="coach-drawer is-open" role="dialog" aria-modal="true" aria-labelledby="coach-title" aria-busy={pending}>
        <header className="coach-head">
          <div><span className="eyebrow">{context}</span><h2 id="coach-title"><Sparkles size={18} aria-hidden="true" /> {dictionary.coach.title}</h2></div>
          <button ref={closeRef} className="icon-button" type="button" onClick={() => setOpen(false)} aria-label={dictionary.coach.close}><X aria-hidden="true" /></button>
        </header>
        <div className="coach-messages" aria-live="polite">
          {messages.map((message) => <div className={`coach-message ${message.role}`} key={message.id}>{message.text}</div>)}
          {pending && <div className="coach-message coach"><LoaderCircle className="spinner-icon" aria-hidden="true" /> {zh ? "正在结合当前 Mission 与证据思考…" : "Thinking with your current Mission and evidence…"}</div>}
          {error && <p className="error-note" role="alert">{error}</p>}
        </div>
        <div className="coach-prompts">
          {[dictionary.coach.hint, dictionary.coach.why, dictionary.coach.check].map((prompt) => <button type="button" className="chip-button" onClick={() => void ask(prompt)} disabled={pending || !missionID} key={prompt}>{prompt}</button>)}
        </div>
        <form className="coach-form" onSubmit={submit}>
          <label className="sr-only" htmlFor="coach-input">{dictionary.coach.placeholder}</label>
          <input id="coach-input" name="coach-message" value={draft} onChange={(event) => setDraft(event.target.value)} placeholder={missionID ? dictionary.coach.placeholder : (zh ? "请先创建并聚焦一个 Mission" : "Create and focus a Mission first")} autoComplete="off" maxLength={100000} disabled={pending || !missionID} />
          <button className="icon-button primary" type="submit" aria-label="Send" disabled={pending || !missionID || !draft.trim()}>{pending ? <LoaderCircle className="spinner-icon" aria-hidden="true" /> : <Send aria-hidden="true" />}</button>
        </form>
      </aside>}
    </>
  );
}
