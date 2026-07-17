import { notFound } from "next/navigation";
import { RouteReview } from "@/components/route-review";
import { isLocale } from "@/i18n/config";

export default async function RoutePage({ params, searchParams }: { params: Promise<{ locale: string }>; searchParams: Promise<{ from?: string; to?: string; onboarding_session?: string; onboarding_version?: string; source_role_profile?: string; target_role_profile?: string }> }) {
  const [{ locale }, query] = await Promise.all([params, searchParams]);
  if (!isLocale(locale)) notFound();
  const version = Number(query.onboarding_version);
  return <RouteReview locale={locale} from={query.from || "Frontend engineering"} to={query.to || "AI Application Engineer"} onboardingSessionID={query.onboarding_session} onboardingVersion={Number.isInteger(version) && version > 0 ? version : undefined} sourceRoleProfileID={query.source_role_profile} targetRoleProfileID={query.target_role_profile} />;
}
