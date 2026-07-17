"use client";

import Link from "next/link";
import { useEffect, useRef, useState } from "react";
import { AlertCircle, CheckCircle2, LoaderCircle, MailCheck } from "lucide-react";
import type { Locale } from "@/i18n/config";
import { ApiError, apiRequest, newIdempotencyKey } from "@/lib/api/client";
import { Brand } from "./brand";

type State = "verifying" | "verified" | "error";

export function EmailVerification({ locale, token }: { locale: Locale; token: string }) {
  const zh = locale === "zh-CN";
  const started = useRef(false);
  const [state, setState] = useState<State>(token ? "verifying" : "error");
  const [message, setMessage] = useState(token ? "" : (zh ? "验证链接缺少 token。" : "The verification link is missing its token."));

  useEffect(() => {
    if (!token || started.current) return;
    started.current = true;
    const requestID = newIdempotencyKey();
    apiRequest("/v1/auth/verify-email", { method: "POST", idempotencyKey: newIdempotencyKey(), body: { request_id: requestID, token } })
      .then(() => setState("verified"))
      .catch((error: unknown) => {
        const code = error instanceof ApiError ? error.message : "request_failed";
        setMessage(code === "state_conflict" ? (zh ? "此验证链接已使用或已失效。" : "This verification link has already been used or is no longer valid.") : (zh ? "无法验证邮箱。请重新发送验证邮件或联系支持。" : "Email verification failed. Request a new verification email or contact support."));
        setState("error");
      });
  }, [token, zh]);

  return <main className="auth-shell" id="main-content">
    <section className="auth-aside"><Brand locale={locale} /><div><p className="eyebrow">{zh ? "账号验证" : "Account verification"}</p><h1>{zh ? "先确认邮箱，再把路线安全地归还给你。" : "Confirm your email before your route is safely attached."}</h1></div></section>
    <section className="auth-form card" aria-live="polite">
      {state === "verifying" && <><LoaderCircle className="auth-icon spin" /><p className="eyebrow">{zh ? "正在验证" : "Verifying"}</p><h2>{zh ? "请稍候" : "One moment"}</h2><p>{zh ? "正在验证单次链接。请勿关闭此页面。" : "The one-time link is being verified. Keep this page open."}</p></>}
      {state === "verified" && <><CheckCircle2 className="auth-icon" /><p className="eyebrow">{zh ? "验证完成" : "Verification complete"}</p><h2>{zh ? "邮箱已验证" : "Your email is verified"}</h2><p>{zh ? "现在登录即可继续；若此前创建了匿名路线，登录后会进行幂等归属。" : "Sign in to continue. If you created an anonymous route, it will be attached idempotently after login."}</p><Link className="button primary" href={`/${locale}/login`}>{zh ? "安全登录" : "Sign in securely"}</Link></>}
      {state === "error" && <><MailCheck className="auth-icon" /><p className="eyebrow">{zh ? "无法完成验证" : "Verification unavailable"}</p><h2>{zh ? "链接需要处理" : "This link needs attention"}</h2><p className="error-note" role="alert"><AlertCircle />{message}</p><Link className="button ghost" href={`/${locale}/login`}>{zh ? "返回登录" : "Return to sign in"}</Link></>}
    </section>
  </main>;
}
