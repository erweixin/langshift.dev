import { notFound } from "next/navigation";
import { EvidenceWorkspace } from "@/components/evidence-workspace";
import { isLocale } from "@/i18n/config";

export default async function EvidencePage({ params }: { params: Promise<{ locale: string }> }) {
  const { locale } = await params;
  if (!isLocale(locale)) notFound();
  return <EvidenceWorkspace locale={locale} />;
}
