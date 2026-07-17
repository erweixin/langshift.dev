"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { useState } from "react";
import { BriefcaseBusiness, Building2, CalendarCheck2, Compass, FileCheck2, FolderKanban, Goal, Settings } from "lucide-react";
import type { Locale } from "@/i18n/config";
import type { Dictionary } from "@/i18n/dictionaries";
import { apiRequest, newIdempotencyKey } from "@/lib/api/client";
import { demoMode } from "@/lib/demo";
import { demoMission } from "@/lib/model";
import { useMissions } from "@/lib/use-product-workspace";
import { Brand } from "./brand";
import { CoachDrawer } from "./coach-drawer";

export function AppShell({ locale, dictionary, children }: { locale: Locale; dictionary: Dictionary; children: React.ReactNode }) {
  const pathname = usePathname();
  const workspace = useMissions(locale, !demoMode);
  const [activeMission, setActiveMission] = useState(demoMode ? "cloud-agent" : "");
  const [switchError, setSwitchError] = useState("");
  const demoMissions = [
    { id: "cloud-agent", name: demoMission.name, stage: demoMission.stage },
    { id: "ai-product", name: "AI Product Strategist", stage: locale === "zh-CN" ? "Stage 2 · 验证产品判断" : "Stage 2 · Validate product judgment" },
  ];
  const missions = demoMode ? demoMissions : workspace.items.map((mission) => ({ id: mission.id, name: workspace.roleNames.get(mission.target_role_profile_id) ?? `${locale === "zh-CN" ? "成长目标" : "Mission"} ${mission.id.slice(0, 8)}`, stage: mission.current_route_revision_id ? (locale === "zh-CN" ? "路线已激活" : "Route active") : mission.status }));
  const selectedMissionID = activeMission || workspace.focus?.id || "";
  const currentMission = missions.find((mission) => mission.id === selectedMissionID) ?? missions[0] ?? { id: "", name: locale === "zh-CN" ? "尚无 Mission" : "No Mission yet", stage: locale === "zh-CN" ? "创建目标后开始" : "Create a goal to begin" };
  async function switchMission(missionID: string) {
    if (demoMode) { setActiveMission(missionID); return; }
    if (missionID === workspace.focus?.id) return;
    setSwitchError("");
    try {
      await apiRequest(`/v1/missions/${missionID}/focus`, { method: "PUT", contentType: "application/vnd.lites.mission-focus.v2+json", idempotencyKey: newIdempotencyKey(), ifMatch: `"${workspace.focusState.version}"`, body: { request_id: newIdempotencyKey(), expected_focus_version: workspace.focusState.version, replacement_mission_id: null } });
      setActiveMission(missionID);
      await workspace.refresh();
    } catch {
      setSwitchError(locale === "zh-CN" ? "切换失败，请刷新后重试。" : "Focus switch failed. Refresh and retry.");
    }
  }
  const nav = [
    { key: "today", label: dictionary.nav.today, icon: CalendarCheck2 },
    { key: "map", label: dictionary.nav.map, icon: Compass },
    { key: "evidence", label: dictionary.nav.evidence, icon: FileCheck2 },
    { key: "goals", label: dictionary.nav.goals, icon: Goal },
    { key: "create", label: dictionary.nav.create, icon: FolderKanban },
    { key: "admin", label: dictionary.nav.admin, icon: Building2 },
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
          {missions.map((mission) => <button type="button" key={mission.id} className={mission.id === selectedMissionID ? "mission-card active" : "mission-card"} aria-pressed={mission.id === selectedMissionID} onClick={() => void switchMission(mission.id)}>
            <span className="mission-icon"><BriefcaseBusiness aria-hidden="true" /></span>
            <span><strong>{mission.name}</strong><small>{mission.stage}</small></span>
            {mission.id === selectedMissionID && <span className="status-dot" title={dictionary.common.current} />}
          </button>)}
          {!demoMode && workspace.loading && <small>{locale === "zh-CN" ? "读取 Missions…" : "Loading Missions…"}</small>}
          {!demoMode && (workspace.error || switchError) && <small className="error-note" role="alert">{switchError || workspace.error}</small>}
          <Link href={`/${locale}/goals`} className="mission-manage">{locale === "zh-CN" ? "管理 Missions" : "Manage Missions"}</Link>
          <Link className="add-mission" href={`/${locale}/onboarding?new=mission`}>+ {dictionary.common.add}</Link>
        </section>
        <footer className="sidebar-footer"><Link href={`/${locale}/support`}>Support</Link><Link href={`/${locale}/legal`}>Legal notice</Link></footer>
      </aside>
      <label className="mobile-mission-switcher"><span>Mission</span><select aria-label={locale === "zh-CN" ? "切换 Mission" : "Switch Mission"} value={selectedMissionID} onChange={(event) => void switchMission(event.target.value)}>{missions.map((mission) => <option value={mission.id} key={mission.id}>{mission.name}</option>)}</select></label>
      <main className="app-main" id="main-content" tabIndex={-1}>{children}</main>
      <CoachDrawer dictionary={dictionary} context={`${currentMission.name} · ${currentMission.stage}`} missionID={currentMission.id} locale={locale} />
    </div>
  );
}
