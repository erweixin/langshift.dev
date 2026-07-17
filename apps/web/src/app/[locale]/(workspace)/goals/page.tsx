import { notFound } from "next/navigation";
import { GoalsWorkspace } from "@/components/goals-workspace";
import { isLocale } from "@/i18n/config";

export default async function GoalsPage({ params }: { params: Promise<{ locale: string }> }) {
  const { locale } = await params;
  if (!isLocale(locale)) notFound();
  return <GoalsWorkspace locale={locale} />;
}
