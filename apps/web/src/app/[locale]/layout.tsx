import type { Metadata, Viewport } from "next";
import { notFound } from "next/navigation";
import { Providers } from "@/components/providers";
import { dictionary } from "@/i18n/dictionaries";
import { isLocale, locales } from "@/i18n/config";
import "../globals.css";

export const metadata: Metadata = {
  title: { default: "Lites — Skill migration, proven", template: "%s · Lites" },
  description: "Turn what you already know into evidence for what comes next.",
  applicationName: "Lites",
  manifest: "/manifest.webmanifest",
  appleWebApp: { capable: true, title: "Lites", statusBarStyle: "default" },
};

export const viewport: Viewport = { themeColor: "#f5f1e8", colorScheme: "light" };

export function generateStaticParams() {
  return locales.map((locale) => ({ locale }));
}

export default async function LocaleLayout({ children, params }: Readonly<{ children: React.ReactNode; params: Promise<{ locale: string }> }>) {
  const { locale } = await params;
  if (!isLocale(locale)) notFound();
  return (
    <html lang={locale} data-scroll-behavior="smooth">
      <body>
        <a className="skip-link" href="#main-content">Skip to main content</a>
        <Providers dictionary={dictionary(locale)}>{children}</Providers>
      </body>
    </html>
  );
}
