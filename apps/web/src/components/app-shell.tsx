"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { BriefcaseBusiness, CalendarCheck2, Compass, FileCheck2, FolderKanban, Goal, Settings } from "lucide-react";
import type { Locale } from "@/i18n/config";
import type { Dictionary } from "@/i18n/dictionaries";
import { demoMission } from "@/lib/model";
import { Brand } from "./brand";
import { CoachDrawer } from "./coach-drawer";

export function AppShell({ locale, dictionary, children }: { locale: Locale; dictionary: Dictionary; children: React.ReactNode }) {
  const pathname = usePathname();
  const nav = [
    { key: "today", label: dictionary.nav.today, icon: CalendarCheck2 },
    { key: "map", label: dictionary.nav.map, icon: Compass },
    { key: "evidence", label: dictionary.nav.evidence, icon: FileCheck2 },
    { key: "goals", label: dictionary.nav.goals, icon: Goal },
    { key: "create", label: dictionary.nav.create, icon: FolderKanban },
    { key: "settings", label: dictionary.nav.settings, icon: Settings },
  ];
  return (
    <div className="app-frame">
      <aside className="sidebar">
        <Brand locale={locale} />
        <nav className="primary-nav" aria-label="Primary navigation">
          {nav.map(({ key, label, icon: Icon }) => {
            const href = `/${locale}/${key}`;
            const active = pathname === href || pathname.startsWith(`${href}/`);
            return <Link href={href} key={key} className={active ? "active" : ""} aria-current={active ? "page" : undefined}><Icon aria-hidden="true" /><span>{label}</span></Link>;
          })}
        </nav>
        <section className="mission-switcher" aria-labelledby="mission-switcher-title">
          <div className="section-label" id="mission-switcher-title">Mission</div>
          <Link href={`/${locale}/goals`} className="mission-card" aria-current="true">
            <span className="mission-icon"><BriefcaseBusiness aria-hidden="true" /></span>
            <span><strong>{demoMission.name}</strong><small>{demoMission.stage}</small></span>
            <span className="status-dot" title={dictionary.common.current} />
          </Link>
          <Link className="add-mission" href={`/${locale}/onboarding?new=mission`}>+ {dictionary.common.add}</Link>
        </section>
        <footer className="sidebar-footer"><span>AGPL-3.0</span><Link href={`/${locale}/settings#legal`}>Legal notice</Link></footer>
      </aside>
      <main className="app-main" id="main-content" tabIndex={-1}>{children}</main>
      <CoachDrawer dictionary={dictionary} context={`${demoMission.name} · ${demoMission.stage}`} />
    </div>
  );
}
