import type { Locale } from "@/i18n/config";
import { evidenceLevelLabels, type EvidenceLevel } from "@/lib/model";

export function LevelBadge({ level, locale }: { level: EvidenceLevel; locale: Locale }) {
  return <span className={`level level-${level}`}>{evidenceLevelLabels[locale][level]}</span>;
}
