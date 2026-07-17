import { notFound } from "next/navigation";
import { AdminConsole } from "@/components/admin-console";
import { isLocale } from "@/i18n/config";

export default async function AdminPage({ params }: { params: Promise<{ locale: string }> }) {
  const { locale } = await params;
  if (!isLocale(locale)) notFound();
  return <AdminConsole locale={locale} />;
}
