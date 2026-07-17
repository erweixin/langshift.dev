import Link from "next/link";
import type { Locale } from "@/i18n/config";
import { Brand } from "./brand";

export function PublicShell({ locale, children }: { locale: Locale; children: React.ReactNode }) {
  const zh = locale === "zh-CN";
  return <div className="public-frame"><header className="public-header"><Brand locale={locale} /><nav aria-label={zh ? "公共导航" : "Public navigation"}><Link href={`/${locale}/trust`}>Trust Center</Link><Link href={`/${locale}/status`}>{zh ? "状态" : "Status"}</Link><Link href={`/${locale}/onboarding`}>{zh ? "进入产品" : "Open Lites"}</Link></nav></header><main id="main-content" className="public-main">{children}</main><footer className="public-footer"><span>© 2026 Lites · AGPL-3.0 + Commercial</span><nav aria-label={zh ? "法律与支持" : "Legal and support"}><Link href={`/${locale}/trust`}>{zh ? "安全与隐私" : "Security & privacy"}</Link><Link href={`/${locale}/status`}>{zh ? "服务状态" : "Service status"}</Link><Link href={`/${locale}/support`}>Support</Link></nav></footer></div>;
}
