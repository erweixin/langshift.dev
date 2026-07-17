import { notFound } from "next/navigation";
import { SupportCenter } from "@/components/support-center";
import { isLocale } from "@/i18n/config";

export default async function SupportPage({ params }: { params: Promise<{ locale: string }> }) {
  const { locale } = await params;
  if (!isLocale(locale)) notFound();
  return <SupportCenter locale={locale} />;
}
