import Link from "next/link";
import type { Locale } from "@/i18n/config";

export function Brand({ locale, compact = false }: { locale: Locale; compact?: boolean }) {
  return (
    <Link className="brand" href={`/${locale}/today`} aria-label="Lites home">
      <span className="brand-mark" aria-hidden="true">◆</span>
      {!compact && <span>Lites</span>}
    </Link>
  );
}
