import { notFound } from "next/navigation";
import { SettingsPanel } from "@/components/settings-panel";
import { isLocale } from "@/i18n/config";

export default async function SettingsPage({ params }: { params: Promise<{ locale: string }> }) { const { locale } = await params; if (!isLocale(locale)) notFound(); return <SettingsPanel locale={locale} />; }
