import { notFound } from "next/navigation";
import { PublicShell } from "@/components/public-shell";
import { StatusPage } from "@/components/status-page";
import { isLocale } from "@/i18n/config";

export default async function StatusRoute({ params }: { params: Promise<{ locale: string }> }) {
  const { locale } = await params;
  if (!isLocale(locale)) notFound();
  return <PublicShell locale={locale}><StatusPage locale={locale} /></PublicShell>;
}
