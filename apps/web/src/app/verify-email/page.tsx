import { EmailVerification } from "@/components/email-verification";
import { isLocale } from "@/i18n/config";

export default async function VerifyEmailEntryPage({ searchParams }: { searchParams: Promise<{ token?: string; locale?: string }> }) {
  const query = await searchParams;
  const requestedLocale = query.locale ?? "";
  const locale = isLocale(requestedLocale) ? requestedLocale : "en";
  return <EmailVerification locale={locale} token={query.token ?? ""} />;
}
