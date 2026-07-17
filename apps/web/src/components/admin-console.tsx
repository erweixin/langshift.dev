"use client";

import { FormEvent, useState } from "react";
import { AlertTriangle, CheckCircle2, ClipboardCheck, KeyRound, RefreshCw, ShieldCheck, UsersRound } from "lucide-react";
import type { Locale } from "@/i18n/config";
import { ApiError, apiDownload, apiRequest, newIdempotencyKey } from "@/lib/api/client";

type Operation = { status: "idle" | "working" | "done" | "error"; message?: string };
type EnterpriseResource = { id: string; version: number; status: string; updated_at: string; replayed?: boolean };
type ControlResource = EnterpriseResource & { proposal_hash: string; target_id: string; target_version: number; approval_count: number };
type Membership = { id: string; user_id: string; email: string; role: string; status: string; version: number; joined_at: string; updated_at: string; deactivated_at: string | null };
type Usage = { tenant_id: string; granted_units: number; available_units: number; reserved_units: number; settled_units: number; active_seats: number; seat_limit: number; as_of: string };
type AuditRecord = { id: string; kind: string; action: string; actor_user_id: string; resource_kind: string; resource_id: string; reason_hash: string; before_version: number | null; after_version: number | null; occurred_at: string };
type AuditExport = { id: string; version: 1; status: "ready"; format: "jsonl" | "csv"; record_count: number; content_hash: string; byte_size: number; period_start: string; period_end: string; created_at: string; expires_at: string; replayed?: boolean };

const idle: Operation = { status: "idle" };
const enterpriseAccept = "application/vnd.lites.enterprise-resource.v2+json";
const controlAccept = "application/vnd.lites.admin-control-resource.v2+json";

function errorMessage(error: unknown, zh: boolean) {
  if (error instanceof ApiError) {
    const request = error.requestID ? ` · request ${error.requestID}` : "";
    if (error.status === 401 && error.message === "reauthentication_required") return (zh ? "重新认证已过期，请先验证当前密码。" : "Reauthentication expired. Verify your current password first.") + request;
    if (error.status === 403) return (zh ? "当前成员角色无权执行此操作。" : "Your current membership role cannot perform this operation.") + request;
    if (error.status === 409 || error.status === 412) return (zh ? "资源版本或审批范围已变化，请重新核对后提交。" : "The resource version or approval scope changed. Review and submit again.") + request;
    return `${error.message}${request}`;
  }
  return error instanceof Error ? error.message : zh ? "操作未完成，请重试。" : "The operation did not complete. Try again.";
}

function asISO(value: FormDataEntryValue | null) {
  const raw = String(value ?? "").trim();
  return raw ? new Date(raw).toISOString() : null;
}

function requiredString(data: FormData, name: string) {
  return String(data.get(name) ?? "").trim();
}

function requiredNumber(data: FormData, name: string) {
  return Number(data.get(name));
}

function identifierList(data: FormData, name: string) {
  return requiredString(data, name).split(/[\s,]+/).map((value) => value.trim()).filter(Boolean);
}

function parseObject(value: string, zh: boolean) {
  const parsed: unknown = JSON.parse(value);
  if (!parsed || Array.isArray(parsed) || typeof parsed !== "object") throw new Error(zh ? "配置必须是 JSON 对象。" : "Configuration must be a JSON object.");
  return parsed as Record<string, unknown>;
}

function OperationNote({ operation }: { operation: Operation; zh: boolean }) {
  if (operation.status === "idle" || operation.status === "working") return null;
  return <p className={operation.status === "done" ? "admin-note success" : "admin-note error"} role={operation.status === "error" ? "alert" : "status"}>
    {operation.status === "done" ? <CheckCircle2 aria-hidden="true" /> : <AlertTriangle aria-hidden="true" />}{operation.message}
  </p>;
}

function ResourceResult({ resource, zh }: { resource: EnterpriseResource | ControlResource | null; zh: boolean }) {
  if (!resource) return null;
  const control = "proposal_hash" in resource ? resource : null;
  return <dl className="admin-resource" aria-label={zh ? "最新服务端结果" : "Latest server result"}>
    <div><dt>ID</dt><dd><code>{resource.id}</code></dd></div>
    <div><dt>{zh ? "状态" : "Status"}</dt><dd>{resource.status}</dd></div>
    <div><dt>{zh ? "版本" : "Version"}</dt><dd>{resource.version}</dd></div>
    {control && <><div><dt>{zh ? "提案哈希" : "Proposal hash"}</dt><dd><code>{control.proposal_hash}</code></dd></div><div><dt>{zh ? "目标版本" : "Target version"}</dt><dd>{control.target_version}</dd></div><div><dt>{zh ? "审批数" : "Approvals"}</dt><dd>{control.approval_count}</dd></div></>}
  </dl>;
}

export function AdminConsole({ locale }: { locale: Locale }) {
  const zh = locale === "zh-CN";
  const [operations, setOperations] = useState<Record<string, Operation>>({});
  const [password, setPassword] = useState("");
  const [reauthUntil, setReauthUntil] = useState<string | null>(null);
  const [members, setMembers] = useState<Membership[] | null>(null);
  const [contractAction, setContractAction] = useState("create");
  const [resources, setResources] = useState<Record<string, EnterpriseResource | ControlResource | null>>({});
  const [auditReason, setAuditReason] = useState("");
  const [usage, setUsage] = useState<Usage | null>(null);
  const [audit, setAudit] = useState<AuditRecord[]>([]);
  const [nextBefore, setNextBefore] = useState<string | null>(null);
  const [auditExport, setAuditExport] = useState<AuditExport | null>(null);

  const operation = (key: string) => operations[key] ?? idle;
  async function execute(key: string, work: () => Promise<string>) {
    setOperations((current) => ({ ...current, [key]: { status: "working" } }));
    try {
      const message = await work();
      setOperations((current) => ({ ...current, [key]: { status: "done", message } }));
    } catch (error) {
      setOperations((current) => ({ ...current, [key]: { status: "error", message: errorMessage(error, zh) } }));
    }
  }

  function storeResource(key: string, resource: EnterpriseResource | ControlResource) {
    setResources((current) => ({ ...current, [key]: resource }));
  }

  async function reauthenticate(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const currentPassword = password;
    setPassword("");
    await execute("reauth", async () => {
      const requestID = newIdempotencyKey();
      const result = await apiRequest<{ valid_until: string }>("/v1/auth/reauthentication", { method: "POST", idempotencyKey: requestID, body: { request_id: requestID, password: currentPassword } });
      setReauthUntil(result.valid_until);
      return zh ? "当前会话已重新认证，有效期为 5 分钟。" : "This session is reauthenticated for five minutes.";
    });
  }

  async function loadMembers() {
    await execute("members", async () => {
      const result = await apiRequest<{ items: Membership[] }>("/v1/memberships");
      setMembers(result.items);
      return zh ? `已读取 ${result.items.length} 位成员。` : `Loaded ${result.items.length} members.`;
    });
  }

  async function createProgram(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const data = new FormData(event.currentTarget);
    await execute("program", async () => {
      const requestID = newIdempotencyKey();
      const result = await apiRequest<EnterpriseResource>("/v1/admin/programs", { method: "POST", idempotencyKey: requestID, accept: enterpriseAccept, contentType: "application/vnd.lites.program-create.v2+json", body: { request_id: requestID, name: requiredString(data, "name"), settings: parseObject(requiredString(data, "settings"), zh) } });
      storeResource("program", result);
      return zh ? "项目已由服务端创建。" : "Program created by the service.";
    });
  }

  async function createCohort(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const data = new FormData(event.currentTarget);
    await execute("cohort", async () => {
      const requestID = newIdempotencyKey();
      const result = await apiRequest<EnterpriseResource>("/v1/admin/cohorts", { method: "POST", idempotencyKey: requestID, accept: enterpriseAccept, contentType: "application/vnd.lites.cohort-create.v2+json", body: { request_id: requestID, program_id: requiredString(data, "program_id"), name: requiredString(data, "name"), starts_at: asISO(data.get("starts_at")), ends_at: asISO(data.get("ends_at")) } });
      storeResource("cohort", result);
      return zh ? "学习群组已创建。" : "Cohort created.";
    });
  }

  async function publishRolePack(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const data = new FormData(event.currentTarget);
    await execute("role-pack", async () => {
      const requestID = newIdempotencyKey();
      const result = await apiRequest<EnterpriseResource>("/v1/admin/role-packs", { method: "POST", idempotencyKey: requestID, accept: enterpriseAccept, contentType: "application/vnd.lites.role-pack-publish.v2+json", body: { request_id: requestID, program_id: requiredString(data, "program_id"), revision: requiredNumber(data, "revision"), role_profile_ids: identifierList(data, "role_profile_ids"), task_template_ids: identifierList(data, "task_template_ids") } });
      storeResource("role-pack", result);
      return zh ? "角色包已作为不可变 revision 发布。" : "Role pack published as an immutable revision.";
    });
  }

  async function publishTaskPack(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const data = new FormData(event.currentTarget);
    await execute("task-pack", async () => {
      const requestID = newIdempotencyKey();
      const result = await apiRequest<EnterpriseResource>("/v1/admin/task-packs", { method: "POST", idempotencyKey: requestID, accept: enterpriseAccept, contentType: "application/vnd.lites.task-pack-publish.v2+json", body: { request_id: requestID, program_id: requiredString(data, "program_id"), revision: requiredNumber(data, "revision"), name: requiredString(data, "name"), task_template_ids: identifierList(data, "task_template_ids"), assignment: parseObject(requiredString(data, "assignment"), zh) } });
      storeResource("task-pack", result);
      return zh ? "任务包已作为不可变 revision 发布。" : "Task pack published as an immutable revision.";
    });
  }

  async function proposeContract(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const data = new FormData(event.currentTarget);
    await execute("contract-proposal", async () => {
      const requestID = newIdempotencyKey();
      const termsRequired = contractAction === "create" || contractAction === "renew";
      const result = await apiRequest<ControlResource>("/v1/admin/contracts", { method: "POST", idempotencyKey: requestID, accept: controlAccept, contentType: "application/vnd.lites.contract-proposal.v2+json", body: {
        request_id: requestID, action: contractAction,
        target_contract_id: contractAction === "create" ? "" : requiredString(data, "target_contract_id"),
        target_version: contractAction === "create" ? 0 : requiredNumber(data, "target_version"),
        contract_number: termsRequired ? requiredString(data, "contract_number") : "",
        starts_at: termsRequired ? asISO(data.get("starts_at")) : null,
        ends_at: termsRequired ? asISO(data.get("ends_at")) : null,
        seat_limit: termsRequired ? requiredNumber(data, "seat_limit") : 0,
        region: termsRequired ? requiredString(data, "region") : "",
        license_kind: termsRequired ? requiredString(data, "license_kind") : "",
        reason: requiredString(data, "reason"),
      } });
      storeResource("contract-proposal", result);
      return zh ? "合同变更提案已封存，须由另一位授权管理员审批。" : "Contract proposal sealed; another authorized administrator must decide it.";
    });
  }

  async function decideContract(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const data = new FormData(event.currentTarget);
    await execute("contract-decision", async () => {
      const requestID = newIdempotencyKey();
      const proposalID = requiredString(data, "proposal_id");
      const result = await apiRequest<ControlResource>(`/v1/admin/contracts/${encodeURIComponent(proposalID)}/approval-decisions`, { method: "POST", idempotencyKey: requestID, ifMatch: '"1"', accept: controlAccept, contentType: "application/vnd.lites.contract-decision.v2+json", body: { request_id: requestID, decision: requiredString(data, "decision"), proposal_hash: requiredString(data, "proposal_hash"), target_version: requiredNumber(data, "target_version"), expected_proposal_version: 1 } });
      storeResource("contract-decision", result);
      return zh ? "合同审批决定已提交。" : "Contract approval decision submitted.";
    });
  }

  async function proposeEntitlement(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const data = new FormData(event.currentTarget);
    await execute("entitlement-proposal", async () => {
      const requestID = newIdempotencyKey();
      const contractVersion = requiredNumber(data, "contract_version");
      const limit = requiredString(data, "limit_value");
      const result = await apiRequest<ControlResource>("/v1/admin/entitlements", { method: "POST", idempotencyKey: requestID, ifMatch: `"${contractVersion}"`, accept: controlAccept, contentType: "application/vnd.lites.entitlement-proposal.v2+json", body: { request_id: requestID, contract_id: requiredString(data, "contract_id"), expected_contract_version: contractVersion, entitlement_key: requiredString(data, "entitlement_key"), expected_entitlement_version: requiredNumber(data, "entitlement_version"), limit_value: limit === "" ? null : Number(limit), config: parseObject(requiredString(data, "config"), zh), reason: requiredString(data, "reason") } });
      storeResource("entitlement-proposal", result);
      return zh ? "授权变更提案已封存。" : "Entitlement proposal sealed.";
    });
  }

  async function decideEntitlement(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const data = new FormData(event.currentTarget);
    await execute("entitlement-decision", async () => {
      const requestID = newIdempotencyKey();
      const proposalID = requiredString(data, "proposal_id");
      const result = await apiRequest<ControlResource>(`/v1/admin/entitlements/${encodeURIComponent(proposalID)}/approval-decisions`, { method: "POST", idempotencyKey: requestID, ifMatch: '"1"', accept: controlAccept, contentType: "application/vnd.lites.entitlement-decision.v2+json", body: { request_id: requestID, decision: requiredString(data, "decision"), proposal_hash: requiredString(data, "proposal_hash"), target_contract_version: requiredNumber(data, "contract_version"), target_entitlement_version: requiredNumber(data, "entitlement_version"), expected_proposal_version: 1 } });
      storeResource("entitlement-decision", result);
      return zh ? "授权审批决定已提交。" : "Entitlement approval decision submitted.";
    });
  }

  async function loadUsage() {
    await execute("usage", async () => {
      const result = await apiRequest<Usage>("/v1/admin/usage", { accept: "application/vnd.lites.usage-snapshot.v2+json", auditReason: auditReason.trim() });
      setUsage(result);
      return zh ? "已读取最新用量快照。" : "Latest usage snapshot loaded.";
    });
  }

  async function loadAudit(before?: string) {
    await execute("audit", async () => {
      const suffix = before ? `?limit=50&before=${encodeURIComponent(before)}` : "?limit=50";
      const result = await apiRequest<{ items: AuditRecord[]; next_before?: string }>(`/v1/admin/audit${suffix}`, { accept: "application/vnd.lites.admin-audit-page.v2+json", auditReason: auditReason.trim() });
      setAudit((current) => before ? [...current, ...result.items] : result.items);
      setNextBefore(result.next_before ?? null);
      return zh ? `已读取 ${result.items.length} 条审计记录。` : `Loaded ${result.items.length} audit records.`;
    });
  }

  async function requestAuditExport(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const data = new FormData(event.currentTarget);
    await execute("audit-export", async () => {
      const requestID = newIdempotencyKey();
      const result = await apiRequest<AuditExport>("/v1/admin/audit-exports", { method: "POST", idempotencyKey: requestID, accept: "application/vnd.lites.admin-audit-export.v2+json", contentType: "application/vnd.lites.admin-audit-export-request.v2+json", body: { request_id: requestID, period_start: asISO(data.get("period_start")), period_end: asISO(data.get("period_end")), kinds: data.getAll("kinds").map(String), format: requiredString(data, "format"), reason: requiredString(data, "reason") } });
      setAuditExport(result);
      return zh ? `审计导出已生成，共 ${result.record_count} 条记录。` : `Audit export created with ${result.record_count} records.`;
    });
  }

  async function downloadAuditExport() {
    if (!auditExport) return;
    await execute("audit-export-download", async () => {
      const result = await apiDownload(`/v1/admin/audit-exports/${encodeURIComponent(auditExport.id)}`, { auditReason: auditReason.trim(), accept: auditExport.format === "csv" ? "text/csv" : "application/x-ndjson" });
      const url = URL.createObjectURL(result.blob);
      const anchor = document.createElement("a");
      anchor.href = url;
      anchor.download = result.filename;
      anchor.click();
      URL.revokeObjectURL(url);
      return zh ? "已下载并由服务端记录本次敏感访问。" : "Downloaded; the sensitive access was recorded by the service.";
    });
  }

  const termsRequired = contractAction === "create" || contractAction === "renew";
  return <div className="page-wrap admin-console">
    <header className="page-head"><div><p className="eyebrow">{zh ? "组织控制台" : "Organization console"}</p><h1>{zh ? "治理人、权限与商业合同。" : "Govern people, access, and commercial terms."}</h1><p>{zh ? "所有权限都由服务端角色、最近重新认证、版本条件与双人审批强制执行。此界面不会绕过治理边界。" : "Server roles, recent reauthentication, version preconditions, and dual control enforce every privileged action. This interface never bypasses those boundaries."}</p></div></header>

    <nav className="admin-jump" aria-label={zh ? "控制台分区" : "Console sections"}><a href="#admin-access">{zh ? "访问" : "Access"}</a><a href="#admin-people">{zh ? "人员与项目" : "People & programs"}</a><a href="#admin-commercial">{zh ? "合同与授权" : "Contracts & entitlements"}</a><a href="#admin-observability">{zh ? "用量与审计" : "Usage & audit"}</a></nav>

    <section className="card admin-access" id="admin-access"><div><p className="section-label"><KeyRound aria-hidden="true" /> {zh ? "高权限会话" : "Privileged session"}</p><h2>{zh ? "用当前密码开启五分钟授权窗口" : "Open a five-minute authorization window"}</h2><p className="muted">{zh ? "密码仅发送到身份服务用于本次校验；不会写入浏览器存储、响应或审计日志。" : "Your password is sent only to Identity for this check; it is never stored in browser storage, responses, or audit logs."}</p>{reauthUntil && <p className="admin-valid"><ShieldCheck aria-hidden="true" />{zh ? "有效至" : "Valid until"} <time dateTime={reauthUntil}>{new Date(reauthUntil).toLocaleString(locale)}</time></p>}</div><form onSubmit={reauthenticate}><label className="field"><span>{zh ? "当前密码" : "Current password"}</span><input name="current-password" type="password" autoComplete="current-password" required value={password} onChange={(event) => setPassword(event.target.value)} /></label><button className="button primary" type="submit" disabled={!password || operation("reauth").status === "working"}>{operation("reauth").status === "working" ? (zh ? "正在验证…" : "Verifying…") : (zh ? "重新认证" : "Reauthenticate")}</button><OperationNote operation={operation("reauth")} zh={zh} /></form></section>

    <section id="admin-people" className="admin-section"><div className="admin-section-head"><div><p className="section-label"><UsersRound aria-hidden="true" /> {zh ? "人员与项目" : "People & programs"}</p><h2>{zh ? "以租户边界管理组织学习" : "Manage organizational learning within the tenant boundary"}</h2></div><button className="button" type="button" onClick={loadMembers} disabled={operation("members").status === "working"}><RefreshCw aria-hidden="true" />{zh ? "读取成员" : "Load members"}</button></div><OperationNote operation={operation("members")} zh={zh} />
      {members && <div className="admin-table-wrap"><table className="admin-table"><caption className="sr-only">{zh ? "组织成员" : "Organization members"}</caption><thead><tr><th>{zh ? "成员" : "Member"}</th><th>{zh ? "角色" : "Role"}</th><th>{zh ? "状态" : "Status"}</th><th>{zh ? "版本" : "Version"}</th><th>{zh ? "加入时间" : "Joined"}</th></tr></thead><tbody>{members.map((member) => <tr key={member.id}><td><strong>{member.email}</strong><small>{member.id}</small></td><td>{member.role}</td><td><span className="chip">{member.status}</span></td><td>{member.version}</td><td><time dateTime={member.joined_at}>{new Date(member.joined_at).toLocaleDateString(locale)}</time></td></tr>)}</tbody></table>{members.length === 0 && <p className="muted">{zh ? "此租户暂无成员。" : "This tenant has no members."}</p>}</div>}
      <div className="admin-grid"><form className="card form-grid" onSubmit={createProgram}><div><p className="section-label">Program</p><h3>{zh ? "创建组织项目" : "Create an organization program"}</h3></div><label className="field"><span>{zh ? "项目名称" : "Program name"}</span><input name="name" required maxLength={200} /></label><label className="field"><span>{zh ? "设置（JSON 对象）" : "Settings (JSON object)"}</span><textarea name="settings" required defaultValue="{}" spellCheck={false} /></label><button className="button primary" disabled={operation("program").status === "working"}>{zh ? "创建项目" : "Create program"}</button><OperationNote operation={operation("program")} zh={zh} /><ResourceResult resource={resources.program ?? null} zh={zh} /></form>
      <form className="card form-grid" onSubmit={createCohort}><div><p className="section-label">Cohort</p><h3>{zh ? "创建学习群组" : "Create a learning cohort"}</h3></div><label className="field"><span>Program ID</span><input name="program_id" required autoComplete="off" /></label><label className="field"><span>{zh ? "群组名称" : "Cohort name"}</span><input name="name" required maxLength={200} /></label><div className="admin-form-pair"><label className="field"><span>{zh ? "开始" : "Starts"}</span><input name="starts_at" type="datetime-local" /></label><label className="field"><span>{zh ? "结束" : "Ends"}</span><input name="ends_at" type="datetime-local" /></label></div><button className="button primary" disabled={operation("cohort").status === "working"}>{zh ? "创建群组" : "Create cohort"}</button><OperationNote operation={operation("cohort")} zh={zh} /><ResourceResult resource={resources.cohort ?? null} zh={zh} /></form></div>
      <div className="admin-grid"><form className="card form-grid" onSubmit={publishRolePack}><div><p className="section-label">Role pack</p><h3>{zh ? "发布角色路径包" : "Publish a role-path pack"}</h3></div><label className="field"><span>Program ID</span><input name="program_id" required /></label><label className="field"><span>Revision</span><input name="revision" type="number" min="1" required /></label><label className="field"><span>Role profile IDs</span><textarea name="role_profile_ids" required placeholder={zh ? "逗号或换行分隔 UUID" : "Comma or newline separated UUIDs"} /></label><label className="field"><span>Task template IDs</span><textarea name="task_template_ids" required placeholder={zh ? "逗号或换行分隔 UUID" : "Comma or newline separated UUIDs"} /></label><button className="button primary" disabled={operation("role-pack").status === "working"}>{zh ? "发布角色包" : "Publish role pack"}</button><OperationNote operation={operation("role-pack")} zh={zh} /><ResourceResult resource={resources["role-pack"] ?? null} zh={zh} /></form>
      <form className="card form-grid" onSubmit={publishTaskPack}><div><p className="section-label">Task pack</p><h3>{zh ? "发布任务包" : "Publish a task pack"}</h3></div><label className="field"><span>Program ID</span><input name="program_id" required /></label><div className="admin-form-pair"><label className="field"><span>Revision</span><input name="revision" type="number" min="1" required /></label><label className="field"><span>{zh ? "名称" : "Name"}</span><input name="name" required maxLength={200} /></label></div><label className="field"><span>Task template IDs</span><textarea name="task_template_ids" required placeholder={zh ? "逗号或换行分隔 UUID" : "Comma or newline separated UUIDs"} /></label><label className="field"><span>{zh ? "分配策略（JSON 对象）" : "Assignment policy (JSON object)"}</span><textarea name="assignment" required defaultValue='{"required":true}' spellCheck={false} /></label><button className="button primary" disabled={operation("task-pack").status === "working"}>{zh ? "发布任务包" : "Publish task pack"}</button><OperationNote operation={operation("task-pack")} zh={zh} /><ResourceResult resource={resources["task-pack"] ?? null} zh={zh} /></form></div>
    </section>

    <section id="admin-commercial" className="admin-section"><div className="admin-section-head"><div><p className="section-label"><ClipboardCheck aria-hidden="true" /> {zh ? "合同与授权" : "Contracts & entitlements"}</p><h2>{zh ? "变更即提案，执行须双人审批" : "Every change is a proposal; execution requires dual control"}</h2></div></div>
      <div className="admin-grid"><form className="card form-grid" onSubmit={proposeContract}><div><p className="section-label">Contract proposal</p><h3>{zh ? "提出合同变更" : "Propose a contract change"}</h3></div><label className="field"><span>{zh ? "操作" : "Action"}</span><select name="action" value={contractAction} onChange={(event) => setContractAction(event.target.value)}><option value="create">create</option><option value="renew">renew</option><option value="suspend">suspend</option><option value="terminate">terminate</option></select></label>{contractAction !== "create" && <div className="admin-form-pair"><label className="field"><span>Target contract ID</span><input name="target_contract_id" required /></label><label className="field"><span>Target version</span><input name="target_version" type="number" min="1" required /></label></div>}{termsRequired && <><label className="field"><span>Contract number</span><input name="contract_number" required maxLength={200} /></label><div className="admin-form-pair"><label className="field"><span>{zh ? "开始" : "Starts"}</span><input name="starts_at" type="datetime-local" required /></label><label className="field"><span>{zh ? "结束" : "Ends"}</span><input name="ends_at" type="datetime-local" required /></label></div><div className="admin-form-pair"><label className="field"><span>Seat limit</span><input name="seat_limit" type="number" min="1" required /></label><label className="field"><span>Region</span><input name="region" required maxLength={100} /></label></div><label className="field"><span>License kind</span><select name="license_kind"><option value="enterprise_cloud">enterprise_cloud</option><option value="private_cloud">private_cloud</option><option value="commercial_self_hosted">commercial_self_hosted</option></select></label></>}<label className="field"><span>{zh ? "变更理由" : "Change reason"}</span><textarea name="reason" required maxLength={1000} /></label><button className="button primary" disabled={operation("contract-proposal").status === "working"}>{zh ? "封存提案" : "Seal proposal"}</button><OperationNote operation={operation("contract-proposal")} zh={zh} /><ResourceResult resource={resources["contract-proposal"] ?? null} zh={zh} /></form>
      <form className="card form-grid" onSubmit={decideContract}><div><p className="section-label">Contract decision</p><h3>{zh ? "审批合同提案" : "Decide a contract proposal"}</h3><p className="muted">{zh ? "审批者必须不同于提案者；服务端将验证提案哈希和目标版本。" : "The approver must differ from the proposer; the server verifies proposal hash and target version."}</p></div><label className="field"><span>Proposal ID</span><input name="proposal_id" required /></label><label className="field"><span>Proposal hash</span><input name="proposal_hash" required pattern="[0-9a-f]{64}" /></label><div className="admin-form-pair"><label className="field"><span>Target version</span><input name="target_version" type="number" min="0" required /></label><label className="field"><span>{zh ? "决定" : "Decision"}</span><select name="decision"><option value="approve">approve</option><option value="reject">reject</option></select></label></div><button className="button primary" disabled={operation("contract-decision").status === "working"}>{zh ? "提交决定" : "Submit decision"}</button><OperationNote operation={operation("contract-decision")} zh={zh} /><ResourceResult resource={resources["contract-decision"] ?? null} zh={zh} /></form></div>

      <div className="admin-grid"><form className="card form-grid" onSubmit={proposeEntitlement}><div><p className="section-label">Entitlement proposal</p><h3>{zh ? "提出产品授权变更" : "Propose an entitlement change"}</h3></div><label className="field"><span>Contract ID</span><input name="contract_id" required /></label><div className="admin-form-pair"><label className="field"><span>Contract version</span><input name="contract_version" type="number" min="1" required /></label><label className="field"><span>Current entitlement version</span><input name="entitlement_version" type="number" min="0" required defaultValue="0" /></label></div><label className="field"><span>Entitlement</span><select name="entitlement_key"><option value="programs">programs</option><option value="cohorts">cohorts</option><option value="role_packs">role_packs</option><option value="aggregate_analytics">aggregate_analytics</option><option value="audit_export">audit_export</option><option value="private_delivery">private_delivery</option><option value="commercial_license">commercial_license</option><option value="support_tier">support_tier</option></select></label><label className="field"><span>{zh ? "数值上限（可空）" : "Numeric limit (optional)"}</span><input name="limit_value" type="number" min="0" /></label><label className="field"><span>Config (JSON object)</span><textarea name="config" required defaultValue="{}" spellCheck={false} /></label><label className="field"><span>{zh ? "变更理由" : "Change reason"}</span><textarea name="reason" required maxLength={1000} /></label><button className="button primary" disabled={operation("entitlement-proposal").status === "working"}>{zh ? "封存授权提案" : "Seal entitlement proposal"}</button><OperationNote operation={operation("entitlement-proposal")} zh={zh} /><ResourceResult resource={resources["entitlement-proposal"] ?? null} zh={zh} /></form>
      <form className="card form-grid" onSubmit={decideEntitlement}><div><p className="section-label">Entitlement decision</p><h3>{zh ? "审批授权提案" : "Decide an entitlement proposal"}</h3></div><label className="field"><span>Proposal ID</span><input name="proposal_id" required /></label><label className="field"><span>Proposal hash</span><input name="proposal_hash" required pattern="[0-9a-f]{64}" /></label><div className="admin-form-pair"><label className="field"><span>Contract version</span><input name="contract_version" type="number" min="1" required /></label><label className="field"><span>Entitlement version</span><input name="entitlement_version" type="number" min="0" required /></label></div><label className="field"><span>{zh ? "决定" : "Decision"}</span><select name="decision"><option value="approve">approve</option><option value="reject">reject</option></select></label><button className="button primary" disabled={operation("entitlement-decision").status === "working"}>{zh ? "提交决定" : "Submit decision"}</button><OperationNote operation={operation("entitlement-decision")} zh={zh} /><ResourceResult resource={resources["entitlement-decision"] ?? null} zh={zh} /></form></div>
    </section>

    <section id="admin-observability" className="admin-section"><div className="admin-section-head"><div><p className="section-label"><ShieldCheck aria-hidden="true" /> {zh ? "用量与审计" : "Usage & audit"}</p><h2>{zh ? "敏感读取必须说明业务目的" : "Sensitive reads require a business purpose"}</h2><p className="muted">{zh ? "理由会被单向哈希后进入访问审计；界面不展示或持久化理由原文。" : "The reason is one-way hashed into access audit; the interface does not display or persist its plaintext."}</p></div></div><label className="field admin-reason"><span>{zh ? "本次读取理由" : "Reason for this access"}</span><input value={auditReason} onChange={(event) => setAuditReason(event.target.value)} maxLength={500} required placeholder={zh ? "例如：2026 Q3 续约复核" : "For example: 2026 Q3 renewal review"} /></label>
      <div className="admin-observe-actions"><button className="button" type="button" disabled={!auditReason.trim() || operation("usage").status === "working"} onClick={loadUsage}>{zh ? "读取用量" : "Load usage"}</button><button className="button" type="button" disabled={!auditReason.trim() || operation("audit").status === "working"} onClick={() => loadAudit()}>{zh ? "读取审计记录" : "Load audit records"}</button></div><OperationNote operation={operation("usage")} zh={zh} /><OperationNote operation={operation("audit")} zh={zh} />
      {usage && <div className="admin-stats" aria-label={zh ? "用量快照" : "Usage snapshot"}><article><span>{zh ? "可用单元" : "Available units"}</span><strong>{usage.available_units.toLocaleString(locale)}</strong></article><article><span>{zh ? "已结算" : "Settled"}</span><strong>{usage.settled_units.toLocaleString(locale)}</strong></article><article><span>{zh ? "预留" : "Reserved"}</span><strong>{usage.reserved_units.toLocaleString(locale)}</strong></article><article><span>{zh ? "席位" : "Seats"}</span><strong>{usage.active_seats} / {usage.seat_limit}</strong></article></div>}
      {audit.length > 0 && <div className="admin-table-wrap"><table className="admin-table"><caption className="sr-only">{zh ? "管理员审计记录" : "Administrative audit records"}</caption><thead><tr><th>{zh ? "时间" : "Time"}</th><th>{zh ? "操作" : "Action"}</th><th>{zh ? "资源" : "Resource"}</th><th>{zh ? "版本" : "Version"}</th><th>{zh ? "理由哈希" : "Reason hash"}</th></tr></thead><tbody>{audit.map((record) => <tr key={record.id}><td><time dateTime={record.occurred_at}>{new Date(record.occurred_at).toLocaleString(locale)}</time></td><td>{record.kind} · {record.action}</td><td><strong>{record.resource_kind}</strong><small>{record.resource_id}</small></td><td>{record.before_version ?? "–"} → {record.after_version ?? "–"}</td><td><code>{record.reason_hash.slice(0, 12)}…</code></td></tr>)}</tbody></table>{nextBefore && <button className="button" type="button" disabled={operation("audit").status === "working"} onClick={() => loadAudit(nextBefore)}>{zh ? "加载更早记录" : "Load older records"}</button>}</div>}
      <form className="card form-grid admin-export" onSubmit={requestAuditExport}><div><p className="section-label">Audit export</p><h3>{zh ? "生成合规审计导出" : "Create a compliance audit export"}</h3><p className="muted">{zh ? "仅导出管理元数据和理由哈希；需要有效 entitlement 与五分钟内重新认证。" : "Exports management metadata and reason hashes only; an active entitlement and five-minute reauthentication are required."}</p></div><div className="admin-form-pair"><label className="field"><span>{zh ? "开始" : "Period start"}</span><input name="period_start" type="datetime-local" required /></label><label className="field"><span>{zh ? "结束" : "Period end"}</span><input name="period_end" type="datetime-local" required /></label></div><fieldset className="field"><legend>{zh ? "记录类型" : "Record kinds"}</legend><label><input name="kinds" type="checkbox" value="contract_change" defaultChecked /> contract_change</label><label><input name="kinds" type="checkbox" value="accounting_adjustment" defaultChecked /> accounting_adjustment</label><label><input name="kinds" type="checkbox" value="admin_read" defaultChecked /> admin_read</label></fieldset><label className="field"><span>{zh ? "格式" : "Format"}</span><select name="format"><option value="jsonl">JSONL</option><option value="csv">CSV</option></select></label><label className="field"><span>{zh ? "导出理由" : "Export reason"}</span><textarea name="reason" required maxLength={500} /></label><button className="button primary" disabled={operation("audit-export").status === "working"}>{zh ? "生成导出" : "Create export"}</button><OperationNote operation={operation("audit-export")} zh={zh} />{auditExport && <div><dl className="admin-resource"><div><dt>ID</dt><dd><code>{auditExport.id}</code></dd></div><div><dt>{zh ? "记录" : "Records"}</dt><dd>{auditExport.record_count}</dd></div><div><dt>SHA-256</dt><dd><code>{auditExport.content_hash}</code></dd></div></dl><button className="button" type="button" disabled={!auditReason.trim() || operation("audit-export-download").status === "working"} onClick={downloadAuditExport}>{zh ? "下载导出" : "Download export"}</button><OperationNote operation={operation("audit-export-download")} zh={zh} /></div>}</form>
    </section>
  </div>;
}
