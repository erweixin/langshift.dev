import { notFound } from "next/navigation";
import { PublicShell } from "@/components/public-shell";
import { TrustCenter } from "@/components/trust-center";
import { isLocale } from "@/i18n/config";

export default async function TrustPage({ params }: { params: Promise<{ locale: string }> }) {
  const { locale } = await params;
  if (!isLocale(locale)) notFound();
  return <PublicShell locale={locale}><TrustCenter locale={locale} /></PublicShell>;
}
