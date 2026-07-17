"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import type { Locale } from "@/i18n/config";
import { apiRequest } from "@/lib/api/client";

export type MissionResource = {
  id: string;
  version: number;
  status: "draft" | "active" | "paused" | "completed" | "archived";
  source_role_profile_id: string | null;
  target_role_profile_id: string;
  current_route_revision_id: string | null;
  focused: boolean;
  created_at: string;
  updated_at: string;
};

export type MissionList = { items: MissionResource[]; focus: { mission_id: string | null; version: number }; next_cursor: string | null };
export type RoleCatalogItem = { id: string; slug: string; revision: number; status: "active"; name: string };
export type RubricCatalogItem = { id: string; slug: string; revision: number; status: "active"; practice_kind: "code" | "writing" | "design" };
export type RouteDocument = {
  schema_version: 1;
  summary: string;
  transferable_experience: Array<{ statement: string; capability_ids: string[]; evidence_ids: string[]; confidence: "inferred" | "supported" | "verified" }>;
  gaps: Array<{ statement: string; capability_ids: string[]; evidence_ids: string[]; confidence: "inferred" | "supported" | "verified" }>;
  bridge: Array<{ id: string; title: string; rationale: string; from_capability_ids: string[]; to_capability_ids: string[] }>;
  stages: Array<{ id: string; title: string; outcome: string; capability_ids: string[]; evidence_required: string[] }>;
  first_task: { title: string; objective: string; estimated_minutes: number; difficulty: string; capability_ids: string[]; success_criteria: string[] };
};
export type RouteRevision = { id: string; version: number; mission_id: string; route_version: number; status: string; route: RouteDocument | null; accepted_at: string | null; updated_at: string };
export type DailyTaskDocument = { schema_version: 1; title: string; objective: string; why_this_task: string; key_judgment: string; estimated_minutes: number; difficulty: string; practice_kind: "code" | "writing" | "design"; explanation: Array<{ title: string; content: string }>; example: string; practice: { instructions: string; starter_content: string; deterministic_checks: string[]; success_criteria: string[] }; capability_ids: string[]; evidence_targets: string[]; next_task_hint: string };
export type DailyTask = { id: string; mission_id: string; route_revision_id: string; version: number; status: string; practice_kind: "code" | "writing" | "design"; task: DailyTaskDocument | null; estimated_minutes: number; difficulty: string; focus_version: number; scheduled_for: string; current_submission_id?: string | null; current_review_id?: string | null; updated_at: string };
export type Evidence = { id: string; version: number; mission_id: string; evidence_type: string; status: string; source_kind: string; source_id: string | null; content: unknown; recorded_at: string };
export type Project = { id: string; mission_id: string; accepted_route_revision_id: string; version: number; status: string; project_kind: string; title: string; created_at: string; updated_at: string; completed_at: string | null };

export function useMissions(locale: Locale, enabled = true) {
  const [data, setData] = useState<MissionList>({ items: [], focus: { mission_id: null, version: 0 }, next_cursor: null });
  const [roles, setRoles] = useState<RoleCatalogItem[]>([]);
  const [rubrics, setRubrics] = useState<RubricCatalogItem[]>([]);
  const [loading, setLoading] = useState(enabled);
  const [error, setError] = useState("");
  const refresh = useCallback(async (signal?: AbortSignal) => {
    if (!enabled) return;
    try {
      const [missions, catalog] = await Promise.all([
        apiRequest<MissionList>("/v1/missions", { accept: "application/vnd.lites.missions.v2+json", signal }),
        apiRequest<{ items: RoleCatalogItem[]; rubrics: RubricCatalogItem[] }>(`/v1/catalog/roles?locale=${encodeURIComponent(locale)}`, { accept: "application/vnd.lites.role-catalog.v1+json", signal }),
      ]);
      setData(missions);
      setRoles(catalog.items);
      setRubrics(catalog.rubrics);
      setError("");
    } catch (caught) {
      if (caught instanceof DOMException && caught.name === "AbortError") return;
      setError(locale === "zh-CN" ? "无法读取工作区，请重新登录或稍后重试。" : "The workspace could not be loaded. Sign in again or retry shortly.");
    } finally {
      setLoading(false);
    }
  }, [enabled, locale]);
  useEffect(() => {
    const controller = new AbortController();
    queueMicrotask(() => void refresh(controller.signal));
    return () => controller.abort();
  }, [refresh]);
  const roleNames = useMemo(() => new Map(roles.map((role) => [role.id, role.name])), [roles]);
  const focus = data.items.find((mission) => mission.id === data.focus.mission_id) ?? data.items.find((mission) => mission.focused) ?? null;
  return { ...data, focusState: data.focus, roles, rubrics, roleNames, focus, loading, error, refresh };
}

export function useProductWorkspace(locale: Locale, enabled = true) {
  const missions = useMissions(locale, enabled);
  const [routes, setRoutes] = useState<RouteRevision[]>([]);
  const [tasks, setTasks] = useState<DailyTask[]>([]);
  const [evidence, setEvidence] = useState<Evidence[]>([]);
  const [projects, setProjects] = useState<Project[]>([]);
  const [loading, setLoading] = useState(enabled);
  const [error, setError] = useState("");
  useEffect(() => {
    if (!enabled || missions.loading) return;
    const controller = new AbortController();
    async function load() {
      try {
        const [taskResult, evidenceResult, projectResult, routeResult] = await Promise.all([
          apiRequest<{ items: DailyTask[] }>("/v1/daily-tasks", { accept: "application/vnd.lites.daily-tasks.v2+json", signal: controller.signal }),
          apiRequest<{ items: Evidence[] }>("/v1/capability-evidence", { accept: "application/vnd.lites.evidence-list.v2+json", signal: controller.signal }),
          apiRequest<{ items: Project[] }>("/v1/projects", { accept: "application/vnd.lites.projects.v2+json", signal: controller.signal }),
          missions.focus ? apiRequest<{ items: RouteRevision[] }>(`/v1/route-revisions?mission_id=${encodeURIComponent(missions.focus.id)}`, { accept: "application/vnd.lites.route-revisions.v2+json", signal: controller.signal }) : Promise.resolve({ items: [] }),
        ]);
        setTasks(taskResult.items);
        setEvidence(evidenceResult.items);
        setProjects(projectResult.items);
        setRoutes(routeResult.items);
        setError("");
      } catch (caught) {
        if (caught instanceof DOMException && caught.name === "AbortError") return;
        setError(locale === "zh-CN" ? "部分工作区数据暂时不可用。" : "Some workspace data is temporarily unavailable.");
      } finally {
        setLoading(false);
      }
    }
    queueMicrotask(() => void load());
    return () => controller.abort();
  }, [enabled, locale, missions.focus, missions.loading]);
  const route = routes.find((item) => item.id === missions.focus?.current_route_revision_id) ?? routes.find((item) => item.status === "accepted") ?? routes[0] ?? null;
  const focusTasks = tasks.filter((item) => item.mission_id === missions.focus?.id);
  const currentTask = focusTasks.find((item) => item.status === "scheduled" || item.status === "in_progress") ?? focusTasks[0] ?? null;
  return { missions, routes, route, tasks, focusTasks, currentTask, evidence, projects, loading: missions.loading || loading, error: missions.error || error };
}
