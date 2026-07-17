"use client";

import Link from "next/link";
import { FormEvent, useEffect, useState } from "react";
import { useRouter } from "next/navigation";
import { AlertCircle, ArrowRight, CheckCircle2, LoaderCircle, LockKeyhole, MailCheck } from "lucide-react";
import type { Locale } from "@/i18n/config";
import { ApiError, apiRequest, newIdempotencyKey } from "@/lib/api/client";
import { Brand } from "./brand";

type Mode = "register" | "login";
type ClaimContext = { onboardingSessionID: string; version: number };
type SessionList = { items: Array<{ id: string; active_tenant_id: string; current: boolean }>; next_cursor: string | null };

const pendingClaimKey = "lites:pending-onboarding-claim";

function validClaim(value: unknown): value is ClaimContext {
  if (!value || typeof value !== "object") return false;
  const candidate = value as Partial<ClaimContext>;
  return typeof candidate.onboardingSessionID === "string" && candidate.onboardingSessionID.length > 0 && Number.isInteger(candidate.version) && Number(candidate.version) > 0;
}

function errorMessage(error: unknown, zh: boolean) {
  const code = error instanceof ApiError ? error.message : "request_failed";
  const messages: Record<string, [string, string]> = {
    authentication_required: ["Email or password is incorrect, or the email is not verified.", "邮箱或密码不正确，或邮箱尚未验证。"],
    validation_failed: ["Check the fields and try again.", "请检查填写内容后重试。"],
    rate_limited: ["Too many attempts. Please wait before trying again.", "尝试次数过多，请稍后再试。"],
    claim_manual_review: ["Your route is safe, but needs support review before it can be attached.", "你的路线数据是安全的，但需要支持人员复核后才能归属。"],
    state_conflict: ["This request conflicts with the current account state.", "请求与当前账号状态冲突。"],
  };
  return (messages[code] ?? ["The request could not be completed. Try again or contact support.", "请求未能完成，请重试或联系支持。"])[zh ? 1 : 0];
}

export function AuthPortal({ locale, initialMode, claim }: { locale: Locale; initialMode: Mode; claim?: ClaimContext }) {
  const zh = locale === "zh-CN";
  const router = useRouter();
  const [mode, setMode] = useState<Mode>(initialMode);
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [termsAccepted, setTermsAccepted] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [verificationRequired, setVerificationRequired] = useState(false);

  useEffect(() => {
    if (claim && validClaim(claim)) localStorage.setItem(pendingClaimKey, JSON.stringify(claim));
  }, [claim]);

  function pendingClaim(): ClaimContext | undefined {
    const source = claim ?? (() => {
      try {
        return JSON.parse(localStorage.getItem(pendingClaimKey) ?? "null") as unknown;
      } catch {
        localStorage.removeItem(pendingClaimKey);
        return undefined;
      }
    })();
    return validClaim(source) ? source : undefined;
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (busy || (mode === "register" && !termsAccepted)) return;
    setBusy(true);
    setError("");
    try {
      if (mode === "register") {
        const requestID = newIdempotencyKey();
        await apiRequest("/v1/auth/register", { method: "POST", idempotencyKey: newIdempotencyKey(), body: { request_id: requestID, email, password, locale } });
        setPassword("");
        setVerificationRequired(true);
        return;
      }
      const requestID = newIdempotencyKey();
      const login = await apiRequest<{ session_id: string }>("/v1/auth/login", { method: "POST", idempotencyKey: newIdempotencyKey(), body: { request_id: requestID, email, password } });
      const savedClaim = pendingClaim();
      if (savedClaim) {
        const sessions = await apiRequest<SessionList>("/v1/auth/sessions");
        const current = sessions.items.find((item) => item.id === login.session_id && item.current) ?? sessions.items.find((item) => item.current);
        if (!current?.active_tenant_id) throw new Error("active tenant missing from current session");
        const claimRequestID = newIdempotencyKey();
        await apiRequest(`/v1/onboarding-sessions/${encodeURIComponent(savedClaim.onboardingSessionID)}/claim`, {
          method: "POST",
          idempotencyKey: newIdempotencyKey(),
          ifMatch: `"${savedClaim.version}"`,
          body: { request_id: claimRequestID, target_tenant_id: current.active_tenant_id, expected_claim_version: savedClaim.version },
        });
        localStorage.removeItem(pendingClaimKey);
      }
      router.replace(`/${locale}/today`);
      router.refresh();
    } catch (caught) {
      setError(errorMessage(caught, zh));
    } finally {
      setBusy(false);
    }
  }

  if (verificationRequired) return <main className="auth-shell" id="main-content">
    <section className="auth-aside"><Brand locale={locale} /><div><p className="eyebrow">{zh ? "检查邮箱" : "Check your inbox"}</p><h1>{zh ? "验证后，这段成长记录才真正属于你。" : "Verify before this growth record becomes yours."}</h1></div></section>
    <section className="auth-form card" aria-live="polite"><MailCheck className="auth-icon" /><p className="eyebrow">{zh ? "验证邮件已发送" : "Verification sent"}</p><h2>{zh ? "请验证你的邮箱" : "Verify your email"}</h2><p>{zh ? `我们已向 ${email} 发送单次验证链接。验证后返回这里登录，匿名路线仍会保留。` : `We sent a one-time verification link to ${email}. Return here to sign in; your anonymous route remains available.`}</p><button className="button primary" type="button" onClick={() => { setMode("login"); setVerificationRequired(false); }}>{zh ? "邮箱已验证，去登录" : "Verified — sign in"}<ArrowRight /></button></section>
  </main>;

  return <main className="auth-shell" id="main-content">
    <section className="auth-aside"><Brand locale={locale} /><div><p className="eyebrow">{claim ? (zh ? "保存你的路线" : "Keep your route") : (zh ? "欢迎" : "Welcome")}</p><h1>{zh ? "你的成长记录，应该由你带走。" : "Your growth record should travel with you."}</h1><ul><li><CheckCircle2 />{zh ? "路线和草稿默认私密" : "Routes and drafts are private by default"}</li><li><CheckCircle2 />{zh ? "导出证据与作品集" : "Export your evidence and portfolio"}</li><li><CheckCircle2 />{zh ? "所有能力判断都有依据" : "Every capability judgment has a basis"}</li></ul></div></section>
    <section className="auth-form card"><LockKeyhole className="auth-icon" /><p className="eyebrow">{mode === "register" ? (zh ? "创建工作区" : "Create workspace") : (zh ? "安全登录" : "Secure sign in")}</p><h2>{mode === "register" ? (zh ? "继续你的迁移路线" : "Continue your migration route") : (zh ? "返回你的成长空间" : "Return to your growth space")}</h2>
      <form className="form-grid" onSubmit={submit}>
        <label className="field"><span>{zh ? "邮箱" : "Email"}</span><input name="email" type="email" autoComplete="email" spellCheck={false} required value={email} onChange={(event) => setEmail(event.target.value)} placeholder="you@example.com…" /></label>
        <label className="field"><span>{zh ? "密码" : "Password"}</span><input name="password" type="password" minLength={mode === "register" ? 15 : 1} maxLength={128} autoComplete={mode === "register" ? "new-password" : "current-password"} required value={password} onChange={(event) => setPassword(event.target.value)} placeholder={mode === "register" ? (zh ? "至少 15 个字符…" : "At least 15 characters…") : (zh ? "输入密码…" : "Enter password…")} /></label>
        {mode === "register" && <label className="consent"><input name="terms-accepted" type="checkbox" required checked={termsAccepted} onChange={(event) => setTermsAccepted(event.target.checked)} /><span>{zh ? "我同意服务条款和隐私说明。" : "I agree to the Terms and Privacy Notice."}</span></label>}
        {error && <p className="error-note" role="alert"><AlertCircle />{error}</p>}
        <button className="button primary" type="submit" disabled={busy || (mode === "register" && !termsAccepted)}>{busy ? <LoaderCircle className="spin" /> : null}{mode === "register" ? (zh ? "创建并发送验证邮件" : "Create and send verification") : (zh ? "登录并继续" : "Sign in and continue")}<ArrowRight /></button>
      </form>
      <p className="auth-switch">{mode === "register" ? (zh ? "已有账号？" : "Already have an account?") : (zh ? "还没有账号？" : "New to Lites?")} <button className="text-button" type="button" onClick={() => { setMode(mode === "register" ? "login" : "register"); setError(""); }}>{mode === "register" ? (zh ? "登录" : "Sign in") : (zh ? "创建账号" : "Create account")}</button></p>
      {mode === "login" && <p className="auth-switch"><Link href={`/${locale}/onboarding`}>{zh ? "先匿名规划路线" : "Plan a route anonymously first"}</Link></p>}
    </section>
  </main>;
}
