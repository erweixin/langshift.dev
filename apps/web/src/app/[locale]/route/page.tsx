import { notFound } from "next/navigation";
import { RouteReview } from "@/components/route-review";
import { isLocale } from "@/i18n/config";

export default async function RoutePage({ params, searchParams }: { params: Promise<{ locale: string }>; searchParams: Promise<{ from?: string; to?: string }> }) {
  const [{ locale }, query] = await Promise.all([params, searchParams]);
  if (!isLocale(locale)) notFound();
  return <RouteReview locale={locale} from={query.from || "Frontend engineering"} to={query.to || "Cloud agent engineering"} />;
}
