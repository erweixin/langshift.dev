import { notFound } from "next/navigation";
import { AuthPortal } from "@/components/auth-portal";
import { isLocale } from "@/i18n/config";

export default async function RegisterPage({ params, searchParams }: { params: Promise<{ locale: string }>; searchParams: Promise<{ onboarding_session?: string; onboarding_version?: string }> }) {
  const [{ locale }, query] = await Promise.all([params, searchParams]);
  if (!isLocale(locale)) notFound();
  const version = Number(query.onboarding_version);
  const claim = query.onboarding_session && Number.isInteger(version) && version > 0 ? { onboardingSessionID: query.onboarding_session, version } : undefined;
  return <AuthPortal locale={locale} initialMode="register" claim={claim} />;
}
