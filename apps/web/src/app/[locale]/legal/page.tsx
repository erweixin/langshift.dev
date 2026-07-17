import { notFound } from "next/navigation";
import { LegalNotice } from "@/components/legal-notice";
import { PublicShell } from "@/components/public-shell";
import { isLocale } from "@/i18n/config";

export default async function LegalPage({ params }: { params: Promise<{ locale: string }> }) {
  const { locale } = await params;
  if (!isLocale(locale)) notFound();
  return <PublicShell locale={locale}><LegalNotice locale={locale} /></PublicShell>;
}
