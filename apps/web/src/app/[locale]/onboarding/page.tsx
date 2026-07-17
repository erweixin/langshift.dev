import { notFound } from "next/navigation";
import { OnboardingFlow } from "@/components/onboarding-flow";
import { isLocale } from "@/i18n/config";

export default async function OnboardingPage({ params }: { params: Promise<{ locale: string }> }) {
  const { locale } = await params;
  if (!isLocale(locale)) notFound();
  return <OnboardingFlow locale={locale} />;
}
