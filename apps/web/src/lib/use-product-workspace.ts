"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { Locale } from "@/i18n/config";
import { ApiError, apiRequest } from "@/lib/api/client";
import type { components } from "@/lib/api/schema";

export type MissionResource = components["schemas"]["MissionResourceV2"];
export type MissionList = components["schemas"]["MissionListResponseV2"];
type RoleCatalog = components["schemas"]["CatalogRolesListResponse"];
export type RoleCatalogItem = RoleCatalog["items"][number];
export type RubricCatalogItem = RoleCatalog["rubrics"][number];
export type RouteDocument = {
  schema_version: 1;
  summary: string;
  transferable_experience: Array<{ statement: string; capability_ids: string[]; evidence_ids: string[]; confidence: "inferred" | "supported" | "verified" }>;
  gaps: Array<{ statement: string; capability_ids: string[]; evidence_ids: string[]; confidence: "inferred" | "supported" | "verified" }>;
  bridge: Array<{ id: string; title: string; rationale: string; from_capability_ids: string[]; to_capability_ids: string[] }>;
  stages: Array<{ id: string; title: string; outcome: string; capability_ids: string[]; evidence_required: string[] }>;
  first_task: { title: string; objective: string; estimated_minutes: number; difficulty: string; capability_ids: string[]; success_criteria: string[] };
};
type GeneratedRouteRevision = components["schemas"]["RouteRevisionResourceV2"];
export type RouteRevision = Omit<GeneratedRouteRevision, "route"> & { route: RouteDocument | null };
export type DailyTaskDocument = { schema_version: 1; title: string; objective: string; why_this_task: string; key_judgment: string; estimated_minutes: number; difficulty: string; practice_kind: "code" | "writing" | "design"; explanation: Array<{ title: string; content: string }>; example: string; practice: { instructions: string; starter_content: string; deterministic_checks: string[]; success_criteria: string[] }; capability_ids: string[]; evidence_targets: string[]; next_task_hint: string };
type GeneratedDailyTask = components["schemas"]["DailyTaskResourceV2"];
export type DailyTask = Omit<GeneratedDailyTask, "task"> & { task: DailyTaskDocument | null };
type GeneratedEvidence = components["schemas"]["EvidenceResourceV2"];
export type Evidence = Omit<GeneratedEvidence, "content"> & { content: unknown };
export type Project = components["schemas"]["ProjectResourceV2"];

function loadError(caught: unknown, locale: Locale, partial = false) {
  const zh = locale === "zh-CN";
  if (typeof navigator !== "undefined" && !navigator.onLine) return zh ? "当前离线；已保留页面状态，请联网后重试。" : "You are offline. Page state is preserved; retry when connected.";
  if (caught instanceof ApiError && caught.status === 401) return zh ? "登录已过期，请重新登录后重试。" : "Your session expired. Sign in again and retry.";
  if (caught instanceof ApiError && caught.status === 403) return zh ? "当前账户无权读取此工作区。" : "Your account does not have permission to read this workspace.";
  if (caught instanceof ApiError && caught.status === 429) return zh ? "请求过于频繁，请稍后重试。" : "Too many requests. Retry shortly.";
  if (partial) return zh ? "部分工作区数据暂时不可用；可安全重试。" : "Some workspace data is temporarily unavailable; it is safe to retry.";
  return zh ? "无法读取工作区，请稍后重试。" : "The workspace could not be loaded. Retry shortly.";
}

export function useMissions(locale: Locale, enabled = true, pollUntilReady = false) {
  const [data, setData] = useState<MissionList>({ items: [], focus: { mission_id: null, version: 0 }, next_cursor: null });
  const [roles, setRoles] = useState<RoleCatalogItem[]>([]);
  const [rubrics, setRubrics] = useState<RubricCatalogItem[]>([]);
  const [loading, setLoading] = useState(enabled);
  const [error, setError] = useState("");
  const readinessPolls = useRef(0);
  const refresh = useCallback(async (signal?: AbortSignal, background = false) => {
    if (!enabled) return;
    if (!background) setLoading(true);
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
      setError(loadError(caught, locale));
    } finally {
      if (!background) setLoading(false);
    }
  }, [enabled, locale]);
  useEffect(() => {
    const controller = new AbortController();
    queueMicrotask(() => void refresh(controller.signal));
    return () => controller.abort();
  }, [refresh]);
  const roleNames = useMemo(() => new Map(roles.map((role) => [role.id, role.name])), [roles]);
  const focus = data.items.find((mission) => mission.id === data.focus.mission_id) ?? data.items.find((mission) => mission.focused) ?? null;
  useEffect(() => {
    if (!enabled || !pollUntilReady || loading || focus || readinessPolls.current >= 90) return;
    const timer = window.setTimeout(() => {
      readinessPolls.current += 1;
      void refresh(undefined, true);
    }, 2_000);
    return () => window.clearTimeout(timer);
  }, [enabled, focus, loading, pollUntilReady, refresh]);
  return { ...data, focusState: data.focus, roles, rubrics, roleNames, focus, loading, error, refresh };
}

export function useProductWorkspace(locale: Locale, enabled = true, pollUntilReady = false) {
  const missions = useMissions(locale, enabled, pollUntilReady);
  const [routes, setRoutes] = useState<RouteRevision[]>([]);
  const [tasks, setTasks] = useState<DailyTask[]>([]);
  const [evidence, setEvidence] = useState<Evidence[]>([]);
  const [projects, setProjects] = useState<Project[]>([]);
  const [loading, setLoading] = useState(enabled);
  const [error, setError] = useState("");
  const readinessPolls = useRef(0);
  const refresh = useCallback(async (signal?: AbortSignal, background = false) => {
    if (!enabled || missions.loading) return;
    if (!background) setLoading(true);
    try {
      const [taskResult, evidenceResult, projectResult, routeResult] = await Promise.all([
        apiRequest<{ items: DailyTask[] }>("/v1/daily-tasks", { accept: "application/vnd.lites.daily-tasks.v2+json", signal }),
        apiRequest<{ items: Evidence[] }>("/v1/capability-evidence", { accept: "application/vnd.lites.evidence-list.v2+json", signal }),
        apiRequest<{ items: Project[] }>("/v1/projects", { accept: "application/vnd.lites.projects.v2+json", signal }),
        missions.focus ? apiRequest<{ items: RouteRevision[] }>(`/v1/route-revisions?mission_id=${encodeURIComponent(missions.focus.id)}`, { accept: "application/vnd.lites.route-revisions.v2+json", signal }) : Promise.resolve({ items: [] }),
      ]);
      setTasks(taskResult.items);
      setEvidence(evidenceResult.items);
      setProjects(projectResult.items);
      setRoutes(routeResult.items);
      setError("");
    } catch (caught) {
      if (caught instanceof DOMException && caught.name === "AbortError") return;
      setError(loadError(caught, locale, true));
    } finally {
      if (!background) setLoading(false);
    }
  }, [enabled, locale, missions.focus, missions.loading]);
  useEffect(() => {
    if (!enabled || missions.loading) return;
    const controller = new AbortController();
    queueMicrotask(() => void refresh(controller.signal));
    return () => controller.abort();
  }, [enabled, missions.loading, refresh]);
  const route = routes.find((item) => item.id === missions.focus?.current_route_revision_id) ?? routes.find((item) => item.status === "accepted") ?? routes[0] ?? null;
  const focusTasks = tasks.filter((item) => item.mission_id === missions.focus?.id);
  const currentRouteID = missions.focus?.current_route_revision_id ?? null;
  const currentRouteTasks = currentRouteID ? focusTasks.filter((item) => item.route_revision_id === currentRouteID) : [];
  const currentTask = currentRouteTasks.find((item) => item.status === "scheduled" || item.status === "in_progress")
    ?? currentRouteTasks.find((item) => item.status === "submitted" || item.status === "reviewing")
    ?? currentRouteTasks.find((item) => item.status === "completed")
    ?? null;
  const refreshMissions = missions.refresh;
  const retry = useCallback(async () => {
    await refreshMissions();
    await refresh();
  }, [refreshMissions, refresh]);
  useEffect(() => {
    if (!enabled || !pollUntilReady || loading || !missions.focus || currentTask || readinessPolls.current >= 90) return;
    const timer = window.setTimeout(() => {
      readinessPolls.current += 1;
      void refresh(undefined, true);
    }, 2_000);
    return () => window.clearTimeout(timer);
  }, [currentTask, enabled, loading, missions.focus, pollUntilReady, refresh]);
  return { missions, routes, route, tasks, focusTasks, currentRouteTasks, currentTask, evidence, projects, loading: missions.loading || loading, error: missions.error || error, refresh, retry };
}
