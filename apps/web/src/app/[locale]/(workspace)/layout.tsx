import { notFound } from "next/navigation";
import { AppShell } from "@/components/app-shell";
import { isLocale } from "@/i18n/config";
import { dictionary } from "@/i18n/dictionaries";

export default async function WorkspaceLayout({ children, params }: { children: React.ReactNode; params: Promise<{ locale: string }> }) {
  const { locale } = await params;
  if (!isLocale(locale)) notFound();
  return <AppShell locale={locale} dictionary={dictionary(locale)}>{children}</AppShell>;
}
