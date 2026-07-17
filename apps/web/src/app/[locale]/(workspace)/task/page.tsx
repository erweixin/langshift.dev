import { notFound } from "next/navigation";
import { TaskWorkspace } from "@/components/task-workspace";
import { isLocale } from "@/i18n/config";

export default async function TaskPage({ params }: { params: Promise<{ locale: string }> }) {
  const { locale } = await params;
  if (!isLocale(locale)) notFound();
  return <TaskWorkspace locale={locale} />;
}
