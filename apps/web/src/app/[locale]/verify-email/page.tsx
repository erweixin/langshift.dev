import { notFound } from "next/navigation";
import { EmailVerification } from "@/components/email-verification";
import { isLocale } from "@/i18n/config";

export default async function VerifyEmailPage({ params, searchParams }: { params: Promise<{ locale: string }>; searchParams: Promise<{ token?: string }> }) {
  const [{ locale }, query] = await Promise.all([params, searchParams]);
  if (!isLocale(locale)) notFound();
  return <EmailVerification locale={locale} token={query.token ?? ""} />;
}
