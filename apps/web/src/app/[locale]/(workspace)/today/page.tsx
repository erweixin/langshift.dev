import { notFound } from "next/navigation";
import { TodayWorkspace } from "@/components/today-workspace";
import { isLocale } from "@/i18n/config";

export default async function TodayPage({ params }: { params: Promise<{ locale: string }> }) {
  const { locale } = await params;
  if (!isLocale(locale)) notFound();
  return <TodayWorkspace locale={locale} />;
}
