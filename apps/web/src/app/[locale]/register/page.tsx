import Link from "next/link";
import { notFound } from "next/navigation";
import { ArrowRight, CheckCircle2, LockKeyhole } from "lucide-react";
import { Brand } from "@/components/brand";
import { isLocale } from "@/i18n/config";

export default async function RegisterPage({ params, searchParams }: { params: Promise<{ locale: string }>; searchParams: Promise<{ claim?: string }> }) {
  const [{ locale }, query] = await Promise.all([params, searchParams]);
  if (!isLocale(locale)) notFound();
  const zh = locale === "zh-CN";
  return <main className="auth-shell" id="main-content"><section className="auth-aside"><Brand locale={locale} /><div><p className="eyebrow">{query.claim ? (zh ? "保存你的路线" : "Keep your route") : (zh ? "欢迎" : "Welcome")}</p><h1>{zh ? "你的成长记录，应该由你带走。" : "Your growth record should travel with you."}</h1><ul><li><CheckCircle2 />{zh ? "路线和草稿默认私密" : "Routes and drafts are private by default"}</li><li><CheckCircle2 />{zh ? "导出证据与作品集" : "Export your evidence and portfolio"}</li><li><CheckCircle2 />{zh ? "所有能力判断都有依据" : "Every capability judgment has a basis"}</li></ul></div></section><section className="auth-form card"><LockKeyhole className="auth-icon" /><p className="eyebrow">{zh ? "创建工作区" : "Create workspace"}</p><h2>{zh ? "继续你的迁移路线" : "Continue your migration route"}</h2><form className="form-grid"><label className="field"><span>{zh ? "邮箱" : "Email"}</span><input name="email" type="email" autoComplete="email" spellCheck={false} required placeholder="you@example.com…" /></label><label className="field"><span>{zh ? "密码" : "Password"}</span><input name="password" type="password" minLength={12} autoComplete="new-password" required placeholder={zh ? "至少 12 个字符…" : "At least 12 characters…"} /></label><label className="consent"><input name="terms-accepted" type="checkbox" required /><span>{zh ? "我同意服务条款和隐私说明。" : "I agree to the Terms and Privacy Notice."}</span></label><Link className="button primary" href={`/${locale}/today`}>{zh ? "创建并进入今日一步" : "Create and open Today"}<ArrowRight /></Link></form><p className="auth-switch">{zh ? "已有账号？" : "Already have an account?"} <Link href={`/${locale}/today`}>{zh ? "登录" : "Sign in"}</Link></p></section></main>;
}
