"use client";

import { FormEvent, useEffect, useState } from "react";
import { AlertCircle, BellRing, Brain, Check, FileArchive, Globe2, KeyRound, LogOut, MessageSquareText, ShieldCheck, Trash2, UserRound } from "lucide-react";
import type { Locale } from "@/i18n/config";
import { apiDownload, apiRequest, newIdempotencyKey } from "@/lib/api/client";

type SaveState = "idle" | "saving" | "saved" | "error";
type ActionState = "idle" | "working" | "done" | "error";
type AccountExport = { id: string; version: number; status: "requested" | "ready" | "failed"; expires_at: string | null; failure_code: "invalid_command" | "archive_too_large" | "legacy_failure" | null };
const demo = process.env.NEXT_PUBLIC_LITES_DEMO_MODE === "true";
const delay = (milliseconds: number) => new Promise((resolve) => window.setTimeout(resolve, milliseconds));

export function SettingsPanel({ locale }: { locale: Locale }) {
  const zh = locale === "zh-CN";
  const [loadState, setLoadState] = useState<"loading" | "ready" | "error">("loading");
  const [state, setState] = useState<SaveState>("idle");
  const [account, setAccount] = useState<{ version: number; normalized_email: string; display_name: string | null } | null>(null);
  const [workspaceName, setWorkspaceName] = useState("");
  const [profileName, setProfileName] = useState("");
  const [profileState, setProfileState] = useState<SaveState>("idle");
  const [time, setTime] = useState("09:00");
  const [weekdays, setWeekdays] = useState(true);
  const [style, setStyle] = useState("socratic");
  const [timezone, setTimezone] = useState(() => Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC");
  const [preferencesVersion, setPreferencesVersion] = useState(1);
  const [reminder, setReminder] = useState<{ id: string; version: number } | null>(null);
  const [provider, setProvider] = useState("openai");
  const [endpoint, setEndpoint] = useState("");
  const [apiKey, setApiKey] = useState("");
  const [byokPassword, setByokPassword] = useState("");
  const [byokState, setByokState] = useState<ActionState>("idle");
  const [byokLabel, setByokLabel] = useState("");
  const [memoryEnabled, setMemoryEnabled] = useState(true);
  const [retention, setRetention] = useState("365");
  const [memoryVersion, setMemoryVersion] = useState(1);
  const [memoryState, setMemoryState] = useState<ActionState>("idle");
  const [exportState, setExportState] = useState<ActionState>("idle");
  const [accountExport, setAccountExport] = useState<AccountExport | null>(null);
  const [exportPassword, setExportPassword] = useState("");
  const [deletePhrase, setDeletePhrase] = useState("");
  const [deletePassword, setDeletePassword] = useState("");
  const [deleteState, setDeleteState] = useState<ActionState>("idle");
  const [logoutState, setLogoutState] = useState<ActionState>("idle");

  useEffect(() => {
    if (demo) return;
    const controller = new AbortController();
    void Promise.allSettled([
      apiRequest<{ version: number; normalized_email: string; display_name: string | null }>("/v1/account", { signal: controller.signal }),
      apiRequest<{ items: { name: string; active: boolean }[] }>("/v1/tenants", { signal: controller.signal }),
      apiRequest<{ version: number; timezone: string; coach_preferences: { tone: string } }>("/v1/preferences", { signal: controller.signal }),
      apiRequest<{ items: { id: string; version: number; local_time: string; weekdays: number[] }[] }>("/v1/reminder-schedules", { signal: controller.signal }),
      apiRequest<{ version: number; enabled?: boolean; retention_days?: number | null }>("/v1/memory-policy", { signal: controller.signal }),
    ]).then(([accountResult, tenantsResult, preferencesResult, remindersResult, memoryResult]) => {
      if (accountResult.status === "fulfilled") {
        setAccount(accountResult.value);
        setProfileName(accountResult.value.display_name ?? "");
      }
      if (tenantsResult.status === "fulfilled") setWorkspaceName(tenantsResult.value.items.find((tenant) => tenant.active)?.name ?? "");
      if (preferencesResult.status === "fulfilled") {
        setPreferencesVersion(preferencesResult.value.version);
        setTimezone(preferencesResult.value.timezone);
        setStyle(preferencesResult.value.coach_preferences.tone);
      }
      if (remindersResult.status === "fulfilled") {
        const current = remindersResult.value.items[0];
        if (current) {
          setReminder(current);
          setTime(current.local_time);
          setWeekdays(current.weekdays.length === 5);
        }
      }
      if (memoryResult.status === "fulfilled") {
        setMemoryVersion(memoryResult.value.version);
        if (typeof memoryResult.value.enabled === "boolean") setMemoryEnabled(memoryResult.value.enabled);
        if (memoryResult.value.retention_days) setRetention(String(memoryResult.value.retention_days));
      }
      if (!controller.signal.aborted) setLoadState([accountResult, tenantsResult, preferencesResult, remindersResult, memoryResult].some((result) => result.status === "rejected") ? "error" : "ready");
    });
    return () => controller.abort();
  }, []);

  async function saveProfile(event: FormEvent) {
    event.preventDefault(); if (!account) return; setProfileState("saving");
    try {
      const requestID = newIdempotencyKey();
      const updated = await apiRequest<{ version: number }>("/v1/account", { method: "PATCH", idempotencyKey: requestID, ifMatch: `"${account.version}"`, body: { request_id: requestID, changes: { display_name: profileName.trim() || null } } });
      setAccount({ ...account, version: updated.version, display_name: profileName.trim() || null }); setProfileState("saved");
    } catch { setProfileState("error"); }
  }

  async function save(event: FormEvent) {
    event.preventDefault(); setState("saving");
    try {
      if (demo) await delay(450);
      else {
        const preferences = await apiRequest<{ version: number }>("/v1/preferences", {
          method: "PATCH", idempotencyKey: newIdempotencyKey(), ifMatch: `"${preferencesVersion}"`, contentType: "application/vnd.lites.preferences-update.v2+json",
          body: { request_id: newIdempotencyKey(), locale, timezone, coach_preferences: { schema_version: 1, difficulty: "standard", available_minutes: 30, tone: style, explanation_depth: "balanced" }, expected_preferences_version: preferencesVersion },
        });
        setPreferencesVersion(preferences.version);
        const scheduleBody = { request_id: newIdempotencyKey(), timezone, local_time: time, weekdays: weekdays ? [1, 2, 3, 4, 5] : [1, 2, 3, 4, 5, 6, 7], channel: "in_app" };
        if (reminder) {
          const updated = await apiRequest<{ id: string; version: number }>(`/v1/reminder-schedules/${reminder.id}`, {
            method: "PATCH", idempotencyKey: newIdempotencyKey(), ifMatch: `"${reminder.version}"`, contentType: "application/vnd.lites.reminder-update.v2+json",
            body: { ...scheduleBody, action: "replace", expected_schedule_version: reminder.version },
          });
          setReminder(updated);
        } else {
          const created = await apiRequest<{ id: string; version: number }>("/v1/reminder-schedules", { method: "POST", idempotencyKey: newIdempotencyKey(), contentType: "application/vnd.lites.reminder-create.v2+json", body: scheduleBody });
          setReminder(created);
        }
      }
      setState("saved");
    } catch { setState("error"); }
  }

  async function saveBYOK(event: FormEvent) {
    event.preventDefault(); if (!apiKey.trim() || !demo && !byokPassword) return; setByokState("working");
    try {
      if (demo) {
        await delay(450);
        setByokLabel(`${provider} · ••••${apiKey.slice(-4)}`);
      }
      else {
        const reauthID = newIdempotencyKey();
        await apiRequest("/v1/auth/reauthentication", { method: "POST", idempotencyKey: reauthID, body: { request_id: reauthID, password: byokPassword } });
        const requestID = newIdempotencyKey();
        const credential = await apiRequest<{ provider_id: string; secret_hint: string }>("/v1/byok-credentials", { method: "POST", idempotencyKey: requestID, body: { request_id: requestID, provider_id: provider, endpoint: provider === "openai_compatible" ? endpoint.trim() : null, api_key: apiKey } });
        setByokLabel(`${credential.provider_id} · ${credential.secret_hint}`);
      }
      setByokPassword(""); setApiKey(""); setByokState("done");
    } catch { setByokState("error"); }
  }

  async function logout() {
    setLogoutState("working");
    try {
      if (demo) await delay(250);
      else {
        const sessions = await apiRequest<{ items: { id: string; current: boolean }[] }>("/v1/auth/sessions");
        const current = sessions.items.find((session) => session.current);
        if (!current) throw new Error("current session unavailable");
        const requestID = newIdempotencyKey();
        await apiRequest("/v1/auth/logout", { method: "POST", idempotencyKey: newIdempotencyKey(), body: { request_id: requestID, session_id: current.id, all_devices: false } });
      }
      window.location.assign(`/${locale}/login`);
    } catch {
      setLogoutState("error");
    }
  }

  async function saveMemory() {
    setMemoryState("working");
    try {
      if (demo) await delay(400);
      else {
        const result = await apiRequest<{ version: number }>("/v1/memory-policy", {
          method: "PUT", idempotencyKey: newIdempotencyKey(), ifMatch: `"${memoryVersion}"`,
          body: { request_id: newIdempotencyKey(), enabled: memoryEnabled, retention_days: memoryEnabled ? Number(retention) : null, allowed_kinds: memoryEnabled ? ["preference", "goal", "capability_context", "learning_history"] : [], expected_policy_version: memoryVersion },
        });
        setMemoryVersion(result.version);
      }
      setMemoryState("done");
    } catch { setMemoryState("error"); }
  }

  async function requestExport() {
    setExportState("working");
    try {
      if (demo) { await delay(450); setExportState("done"); return; }
      if (!exportPassword) throw new Error("account_export_reauthentication_required");
      const password = exportPassword;
      setExportPassword("");
      const reauthID = newIdempotencyKey();
      await apiRequest("/v1/auth/reauthentication", { method: "POST", idempotencyKey: reauthID, body: { request_id: reauthID, password } });
      const requestID = newIdempotencyKey();
      const created = await apiRequest<{ id: string }>("/v1/account/export-requests", { method: "POST", idempotencyKey: requestID, body: { request_id: requestID, scope: ["account", "missions", "evidence", "projects", "conversations", "memory", "audit"], format: "zip" } });
      for (let attempt = 0; attempt < 120; attempt += 1) {
        const current = await apiRequest<AccountExport>(`/v1/account/export-requests/${created.id}`);
        setAccountExport(current);
        if (current.status === "ready") { setExportState("done"); return; }
        if (current.status === "failed") throw new Error("account_export_failed");
        await delay(1500);
      }
      throw new Error("account_export_timeout");
    } catch { setExportState("error"); }
  }

  async function downloadExport() {
    if (!accountExport || accountExport.status !== "ready") return;
    setExportState("working");
    try {
      if (!demo) {
        if (!exportPassword) throw new Error("account_export_reauthentication_required");
        const password = exportPassword;
        setExportPassword("");
        const reauthID = newIdempotencyKey();
        await apiRequest("/v1/auth/reauthentication", { method: "POST", idempotencyKey: reauthID, body: { request_id: reauthID, password } });
      }
      const result = await apiDownload(`/v1/account/export-requests/${accountExport.id}/download`, { accept: "application/zip" });
      const href = URL.createObjectURL(result.blob);
      const link = document.createElement("a");
      link.href = href; link.download = result.filename; link.click();
      URL.revokeObjectURL(href);
      setExportState("done");
    } catch { setExportState("error"); }
  }

  async function requestDeletion(event: FormEvent) {
    event.preventDefault(); if (deletePhrase !== "DELETE MY ACCOUNT") return; setDeleteState("working");
    try {
      if (demo) await delay(450);
      else {
        if (!deletePassword) throw new Error("account_erasure_reauthentication_required");
        const password = deletePassword;
        setDeletePassword("");
        const reauthID = newIdempotencyKey();
        await apiRequest("/v1/auth/reauthentication", { method: "POST", idempotencyKey: reauthID, body: { request_id: reauthID, password } });
        const requestID = newIdempotencyKey();
        await apiRequest("/v1/account/erasure-requests", { method: "POST", idempotencyKey: requestID, body: { request_id: requestID, confirmation: deletePhrase, reason: null } });
      }
      setDeleteState("done");
    } catch { setDeleteState("error"); }
  }

  const exportDescription = accountExport?.status === "failed" && accountExport.failure_code === "archive_too_large"
    ? (zh ? "归档超过本轮 24 MiB 安全上限；请联系支持获取分批导出。" : "The archive exceeds the 24 MiB safety limit; contact support for a segmented export.")
    : exportState === "error"
      ? (zh ? "导出未完成，请重新输入当前密码后重试。" : "The export did not complete. Enter your current password and retry.")
      : accountExport?.status === "ready"
        ? (zh ? `加密归档已就绪，将于 ${new Date(accountExport.expires_at ?? "").toLocaleString()} 到期。` : `The encrypted archive is ready and expires ${new Date(accountExport.expires_at ?? "").toLocaleString()}.`)
        : exportState === "working"
          ? (zh ? "正在生成可恢复的限时归档…" : "Building a recoverable, expiring archive…")
          : (zh ? "获取 Mission、任务、证据、项目、Memory 治理元数据与审计记录的可移植副本。" : "Get a portable copy of Missions, tasks, evidence, projects, governed Memory metadata, and audit records.");

  const sections = [
    { id: "profile", icon: UserRound, label: zh ? "账号" : "Account" },
    { id: "reminders", icon: BellRing, label: zh ? "提醒与节奏" : "Reminders & pace" },
    { id: "providers", icon: KeyRound, label: "BYOK" },
    { id: "memory", icon: Brain, label: "Memory" },
    { id: "language", icon: Globe2, label: zh ? "语言" : "Language" },
    { id: "privacy", icon: ShieldCheck, label: zh ? "隐私与数据" : "Privacy & data" },
  ];

  return <div className="page-wrap"><header className="page-head"><div><p className="eyebrow">{zh ? "设置" : "Settings"}</p><h1>{zh ? "你的工作区，由你掌控。" : "Your workspace, under your control."}</h1></div></header><div className="settings-layout"><nav className="settings-nav" aria-label={zh ? "设置分区" : "Settings sections"}>{sections.map(({ id, icon: Icon, label }) => <a href={`#${id}`} key={id}><Icon />{label}</a>)}</nav><div className="settings-content">{loadState === "error" && <p className="error-note" role="alert"><AlertCircle />{zh ? "部分设置未能加载。刷新后重试；未成功读取的值不会显示为已保存。" : "Some settings could not be loaded. Refresh to retry; unread values are not presented as saved."}<button className="button" type="button" onClick={() => window.location.reload()}>{zh ? "刷新" : "Refresh"}</button></p>}
	<form className="card form-grid" id="profile" onSubmit={saveProfile}><p className="section-label">{zh ? "账号" : "Account"}</p>{account ? <><div className="profile-row"><span className="avatar">{(account.display_name || account.normalized_email).slice(0, 2).toUpperCase()}</span><div><h2>{account.display_name || (zh ? "未设置显示名称" : "Display name not set")}</h2><p>{account.normalized_email} · {workspaceName || (zh ? "当前工作区" : "Current workspace")}</p></div></div><label className="field"><span>{zh ? "显示名称" : "Display name"}</span><input name="display-name" autoComplete="name" maxLength={200} value={profileName} onChange={(event) => setProfileName(event.target.value)} /></label>{profileState === "error" && <p className="error-note" role="alert">{zh ? "账号资料未保存。" : "Account profile was not saved."}</p>}<button className="button" type="submit" disabled={profileState === "saving"}>{profileState === "saved" ? (zh ? "已保存" : "Saved") : (zh ? "保存账号资料" : "Save account profile")}</button></> : <p aria-live="polite">{zh ? "正在读取账号资料…" : "Loading account profile…"}</p>}</form>

    <form className="card form-grid" id="reminders" onSubmit={save}><div><p className="section-label">{zh ? "提醒与节奏" : "Reminders & pace"}</p><h2>{zh ? "提醒你回来，不催促你刷进度。" : "A reminder to return—not pressure to perform."}</h2></div><label className="field"><span>{zh ? "每日提醒时间" : "Daily reminder time"}</span><input name="reminder-time" type="time" value={time} onChange={(event) => setTime(event.target.value)} /></label><label className="field"><span>{zh ? "时区" : "Timezone"}</span><input name="timezone" autoComplete="off" required value={timezone} onChange={(event) => setTimezone(event.target.value)} /></label><label className="toggle-row"><span><strong>{zh ? "仅工作日" : "Weekdays only"}</strong><small>{zh ? "周末不会发送任务提醒" : "No task reminders on weekends"}</small></span><input name="weekdays-only" type="checkbox" role="switch" checked={weekdays} onChange={(event) => setWeekdays(event.target.checked)} /></label><label className="field"><span>{zh ? "Coach 风格" : "Coach style"}</span><select name="coach-style" value={style} onChange={(event) => setStyle(event.target.value)}><option value="socratic">{zh ? "苏格拉底式：多问问题" : "Socratic: ask before telling"}</option><option value="direct">{zh ? "直接：先给下一步" : "Direct: lead with the next action"}</option><option value="encouraging">{zh ? "鼓励：连接已有进展" : "Encouraging: connect prior progress"}</option></select></label>{state === "error" && <p className="error-note" role="alert"><AlertCircle />{zh ? "设置未保存，请重试。" : "Settings were not saved. Try again."}</p>}<button className="button primary" type="submit" disabled={state === "saving"}>{state === "saved" ? <><Check />{zh ? "已保存" : "Saved"}</> : state === "saving" ? (zh ? "正在确认…" : "Confirming…") : (zh ? "保存设置" : "Save settings")}</button></form>

		<form className="card form-grid" id="providers" onSubmit={saveBYOK}><div><p className="section-label">BYOK · {zh ? "需要最近重新认证" : "Recent reauthentication required"}</p><h2>{zh ? "连接你自己的模型提供方" : "Connect your model provider"}</h2><p className="muted">{zh ? "密钥只会发送到所绑定的 Provider host；浏览器不会持久化明文。" : "The key is sent only to its bound provider host; plaintext is never persisted by the browser."}</p></div>{byokLabel && <p className="credential-chip"><ShieldCheck />{byokLabel}</p>}<label className="field"><span>Provider</span><select name="provider" value={provider} onChange={(event) => setProvider(event.target.value)}><option value="openai">OpenAI</option><option value="anthropic">Anthropic</option><option value="openai_compatible">OpenAI-compatible</option></select></label>{provider === "openai_compatible" && <label className="field"><span>Endpoint</span><input name="provider-endpoint" type="url" autoComplete="url" required value={endpoint} onChange={(event) => setEndpoint(event.target.value)} placeholder="https://api.example.com/v1" /></label>}<label className="field"><span>{zh ? "当前密码（重新认证）" : "Current password (reauthentication)"}</span><input name="byok-current-password" type="password" autoComplete="current-password" required={!demo} value={byokPassword} onChange={(event) => setByokPassword(event.target.value)} /></label><label className="field"><span>API key</span><input name="api-key" type="password" autoComplete="new-password" required value={apiKey} onChange={(event) => setApiKey(event.target.value)} /></label>{byokState === "error" && <p className="error-note" role="alert"><AlertCircle />{zh ? "未保存。请重新认证并检查 Provider 配置。" : "Not saved. Reauthenticate and check the provider configuration."}</p>}<button className="button primary" type="submit" disabled={!apiKey.trim() || !demo && !byokPassword || byokState === "working"}>{byokState === "done" ? <><Check />{zh ? "密钥已封存" : "Credential sealed"}</> : (zh ? "验证并保存密钥" : "Verify and save credential")}</button></form>

    <section className="card form-grid" id="memory"><div><p className="section-label">Memory</p><h2>{zh ? "只有受治理的记忆可以长期保留" : "Only governed memory persists"}</h2><p className="muted">{zh ? "关闭后不会产生新的长期 Memory；历史数据仍按删除与保留策略处理。" : "When disabled, no new long-term Memory is written; existing data remains governed by retention and deletion policy."}</p></div><label className="toggle-row"><span><strong>{zh ? "启用长期 Memory" : "Enable long-term Memory"}</strong><small>{zh ? "偏好、目标、能力上下文和学习历史" : "Preferences, goals, capability context, and learning history"}</small></span><input name="memory-enabled" type="checkbox" role="switch" checked={memoryEnabled} onChange={(event) => setMemoryEnabled(event.target.checked)} /></label><label className="field"><span>{zh ? "保留天数" : "Retention days"}</span><select name="memory-retention" value={retention} disabled={!memoryEnabled} onChange={(event) => setRetention(event.target.value)}><option value="30">30</option><option value="90">90</option><option value="365">365</option></select></label>{memoryState === "error" && <p className="error-note" role="alert"><AlertCircle />{zh ? "Memory 策略未更新。" : "Memory policy was not updated."}</p>}<button className="button primary" type="button" onClick={saveMemory} disabled={memoryState === "working"}>{memoryState === "done" ? <><Check />{zh ? "策略已保存" : "Policy saved"}</> : (zh ? "保存 Memory 策略" : "Save Memory policy")}</button></section>

    <section className="card" id="language"><p className="section-label">{zh ? "语言" : "Language"}</p><div className="setting-row"><Globe2 /><div><strong>{locale === "zh-CN" ? "简体中文" : "English"}</strong><p>{zh ? "界面语言不会改变证据原文。" : "Interface language never rewrites evidence source material."}</p></div><a className="button" href={locale === "zh-CN" ? "/en/settings" : "/zh-CN/settings"}>{locale === "zh-CN" ? "English" : "简体中文"}</a></div></section>

    <section className="card" id="privacy"><p className="section-label">{zh ? "隐私与数据" : "Privacy & data"}</p><div className="setting-row"><MessageSquareText /><div><strong>{zh ? "Support Center" : "Support Center"}</strong><p>{zh ? "创建加密工单，查看支持等级、SLA 时钟与完整回复记录。" : "Create encrypted cases and review support tier, SLA clocks, and the full reply history."}</p></div><a className="button" href={`/${locale}/support`}>{zh ? "打开" : "Open"}</a></div><div className="setting-row"><FileArchive /><div><strong>{zh ? "导出全部数据" : "Export all data"}</strong><p>{exportDescription}</p><label className="field"><span>{zh ? "当前密码（重新认证）" : "Current password (reauthentication)"}</span><input name="export-current-password" type="password" autoComplete="current-password" required={!demo} value={exportPassword} onChange={(event) => setExportPassword(event.target.value)} /></label></div><button className="button" type="button" onClick={accountExport?.status === "ready" ? downloadExport : requestExport} disabled={exportState === "working" || !demo && !exportPassword}>{exportState === "working" ? (zh ? "处理中…" : "Working…") : accountExport?.status === "ready" ? (zh ? "下载归档" : "Download archive") : (zh ? "请求导出" : "Request export")}</button></div><div className="setting-row" id="legal"><ShieldCheck /><div><strong>{zh ? "隐私与法律说明" : "Privacy and legal notices"}</strong><p>AGPL-3.0-only · Source · No warranty · Trademarks · Security</p></div><a className="button" href={`/${locale}/legal`}>{zh ? "查看" : "Review"}</a></div><form className="deletion-form danger-zone" onSubmit={requestDeletion}><Trash2 /><div><strong>{zh ? "删除账户与托管数据" : "Delete account and hosted data"}</strong><p>{deleteState === "done" ? (zh ? "删除请求已创建，宽限期内仍可撤销。" : "Erasure request created; it remains cancelable during the grace period.") : (zh ? "需要最近重新认证。输入 DELETE MY ACCOUNT 创建可撤销的删除请求。" : "Recent reauthentication is required. Enter DELETE MY ACCOUNT to create a cancelable erasure request.")}</p><label className="field"><span>{zh ? "当前密码（重新认证）" : "Current password (reauthentication)"}</span><input name="delete-current-password" type="password" autoComplete="current-password" required={!demo} value={deletePassword} onChange={(event) => setDeletePassword(event.target.value)} /></label><label className="field"><span>{zh ? "确认短语" : "Confirmation phrase"}</span><input name="delete-confirmation" autoComplete="off" value={deletePhrase} onChange={(event) => setDeletePhrase(event.target.value)} /></label></div><button className="button danger" type="submit" disabled={deletePhrase !== "DELETE MY ACCOUNT" || !demo && !deletePassword || deleteState === "working"}>{deleteState === "done" ? (zh ? "已创建请求" : "Request created") : (zh ? "请求删除" : "Request erasure")}</button></form><div className="setting-row danger-zone"><LogOut /><div><strong>{zh ? "退出登录" : "Sign out"}</strong><p>{logoutState === "error" ? (zh ? "退出失败，请重试。" : "Sign out failed. Try again.") : (zh ? "本机未提交草稿不会同步到其他设备。" : "Unsubmitted device drafts do not move to another device.")}</p></div><button className="button danger" type="button" onClick={logout} disabled={logoutState === "working"}>{logoutState === "working" ? (zh ? "正在退出…" : "Signing out…") : (zh ? "退出" : "Sign out")}</button></div></section>
  </div></div></div>;
}
