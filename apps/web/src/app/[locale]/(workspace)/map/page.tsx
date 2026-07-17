import { notFound } from "next/navigation";
import { MapWorkspace } from "@/components/map-workspace";
import { isLocale } from "@/i18n/config";

export default async function MapPage({ params }: { params: Promise<{ locale: string }> }) {
  const { locale } = await params;
  if (!isLocale(locale)) notFound();
  return <MapWorkspace locale={locale} />;
}
