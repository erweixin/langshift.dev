"use client";

import { FormEvent, useEffect, useRef, useState } from "react";
import { Send, Sparkles, X } from "lucide-react";
import type { Dictionary } from "@/i18n/dictionaries";

type Message = { id: string; role: "coach" | "user"; text: string };

export function CoachDrawer({ dictionary, context }: { dictionary: Dictionary; context: string }) {
  const [open, setOpen] = useState(false);
  const [messages, setMessages] = useState<Message[]>([{ id: "welcome", role: "coach", text: "I’m here. Ask about the current step, or select something you want to unpack." }]);
  const [draft, setDraft] = useState("");
  const closeRef = useRef<HTMLButtonElement>(null);
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
  const ask = (text: string) => {
    const value = text.trim();
    if (!value) return;
    setMessages((current) => [...current, { id: crypto.randomUUID(), role: "user", text: value }, { id: crypto.randomUUID(), role: "coach", text: "I’ll keep this grounded in your current Mission and the evidence attached to this task. What part feels least clear?" }]);
    setDraft("");
  };
  const submit = (event: FormEvent) => {
    event.preventDefault();
    ask(draft);
  };
  return (
    <>
      <button className="coach-fab" type="button" onClick={() => setOpen(true)} aria-label={dictionary.coach.open} aria-haspopup="dialog" aria-expanded={open} data-testid="coach-open">
        <Sparkles size={18} aria-hidden="true" /><span>{dictionary.coach.open}</span>
      </button>
      {open && <button className="drawer-scrim" type="button" aria-label={dictionary.coach.close} onClick={() => setOpen(false)} />}
      {open && <aside className="coach-drawer is-open" role="dialog" aria-modal="true" aria-labelledby="coach-title">
        <header className="coach-head">
          <div><span className="eyebrow">{context}</span><h2 id="coach-title"><Sparkles size={18} aria-hidden="true" /> {dictionary.coach.title}</h2></div>
          <button ref={closeRef} className="icon-button" type="button" onClick={() => setOpen(false)} aria-label={dictionary.coach.close}><X aria-hidden="true" /></button>
        </header>
        <div className="coach-messages" aria-live="polite">
          {messages.map((message) => <div className={`coach-message ${message.role}`} key={message.id}>{message.text}</div>)}
        </div>
        <div className="coach-prompts">
          {[dictionary.coach.hint, dictionary.coach.why, dictionary.coach.check].map((prompt) => <button type="button" className="chip-button" onClick={() => ask(prompt)} key={prompt}>{prompt}</button>)}
        </div>
        <form className="coach-form" onSubmit={submit}>
          <label className="sr-only" htmlFor="coach-input">{dictionary.coach.placeholder}</label>
          <input id="coach-input" name="coach-message" value={draft} onChange={(event) => setDraft(event.target.value)} placeholder={dictionary.coach.placeholder} autoComplete="off" />
          <button className="icon-button primary" type="submit" aria-label="Send"><Send aria-hidden="true" /></button>
        </form>
      </aside>}
    </>
  );
}
