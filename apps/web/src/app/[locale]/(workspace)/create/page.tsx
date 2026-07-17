import { notFound } from "next/navigation";
import { CreateStudio } from "@/components/create-studio";
import { isLocale } from "@/i18n/config";

export default async function CreatePage({ params }: { params: Promise<{ locale: string }> }) { const { locale } = await params; if (!isLocale(locale)) notFound(); return <CreateStudio locale={locale} />; }
