import { notFound } from "next/navigation";
import { AuthPortal } from "@/components/auth-portal";
import { isLocale } from "@/i18n/config";

export default async function LoginPage({ params }: { params: Promise<{ locale: string }> }) {
  const { locale } = await params;
  if (!isLocale(locale)) notFound();
  return <AuthPortal locale={locale} initialMode="login" />;
}
