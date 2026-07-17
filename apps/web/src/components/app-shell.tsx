"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { useState } from "react";
import { BriefcaseBusiness, CalendarCheck2, Compass, FileCheck2, FolderKanban, Goal, Settings } from "lucide-react";
import type { Locale } from "@/i18n/config";
import type { Dictionary } from "@/i18n/dictionaries";
import { demoMission } from "@/lib/model";
import { Brand } from "./brand";
import { CoachDrawer } from "./coach-drawer";

export function AppShell({ locale, dictionary, children }: { locale: Locale; dictionary: Dictionary; children: React.ReactNode }) {
  const pathname = usePathname();
  const [activeMission, setActiveMission] = useState("cloud-agent");
  const missions = [
    { id: "cloud-agent", name: demoMission.name, stage: demoMission.stage },
    { id: "ai-product", name: "AI Product Strategist", stage: locale === "zh-CN" ? "Stage 2 · 验证产品判断" : "Stage 2 · Validate product judgment" },
  ];
  const currentMission = missions.find((mission) => mission.id === activeMission) ?? { id: "cloud-agent", name: demoMission.name, stage: demoMission.stage };
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
          {missions.map((mission) => <button type="button" key={mission.id} className={mission.id === activeMission ? "mission-card active" : "mission-card"} aria-pressed={mission.id === activeMission} onClick={() => setActiveMission(mission.id)}>
            <span className="mission-icon"><BriefcaseBusiness aria-hidden="true" /></span>
            <span><strong>{mission.name}</strong><small>{mission.stage}</small></span>
            {mission.id === activeMission && <span className="status-dot" title={dictionary.common.current} />}
          </button>)}
          <Link href={`/${locale}/goals`} className="mission-manage">{locale === "zh-CN" ? "管理 Missions" : "Manage Missions"}</Link>
          <Link className="add-mission" href={`/${locale}/onboarding?new=mission`}>+ {dictionary.common.add}</Link>
        </section>
        <footer className="sidebar-footer"><span>AGPL-3.0</span><Link href={`/${locale}/settings#legal`}>Legal notice</Link></footer>
      </aside>
      <label className="mobile-mission-switcher"><span>Mission</span><select aria-label={locale === "zh-CN" ? "切换 Mission" : "Switch Mission"} value={activeMission} onChange={(event) => setActiveMission(event.target.value)}>{missions.map((mission) => <option value={mission.id} key={mission.id}>{mission.name}</option>)}</select></label>
      <main className="app-main" id="main-content" tabIndex={-1}>{children}</main>
      <CoachDrawer dictionary={dictionary} context={`${currentMission.name} · ${currentMission.stage}`} />
    </div>
  );
}
