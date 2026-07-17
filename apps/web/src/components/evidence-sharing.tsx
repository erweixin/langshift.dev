"use client";

import { FormEvent, useState } from "react";
import { AlertTriangle, CheckCircle2, Share2, ShieldCheck } from "lucide-react";
import type { Locale } from "@/i18n/config";
import { ApiError, apiRequest, newIdempotencyKey } from "@/lib/api/client";

type Grant = { id: string; version: number; status: string; updated_at: string; replayed?: boolean };
type State = { kind: "idle" | "working" | "done" | "error"; message?: string };

function messageFor(error: unknown, zh: boolean) {
  if (error instanceof ApiError) return `${error.message}${error.requestID ? ` · request ${error.requestID}` : ""}`;
  return error instanceof Error ? error.message : zh ? "共享操作未完成。" : "The sharing operation did not complete.";
}

function Note({ state }: { state: State }) {
  if (state.kind === "idle" || state.kind === "working") return null;
  return <p className={state.kind === "done" ? "admin-note success" : "admin-note error"} role={state.kind === "error" ? "alert" : "status"}>
    {state.kind === "done" ? <CheckCircle2 aria-hidden="true" /> : <AlertTriangle aria-hidden="true" />}{state.message}
  </p>;
}

export function EvidenceSharing({ locale }: { locale: Locale }) {
  const zh = locale === "zh-CN";
  const [grant, setGrant] = useState<Grant | null>(null);
  const [grantID, setGrantID] = useState("");
  const [grantVersion, setGrantVersion] = useState("");
  const [createState, setCreateState] = useState<State>({ kind: "idle" });
  const [revokeState, setRevokeState] = useState<State>({ kind: "idle" });

  async function createGrant(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const data = new FormData(event.currentTarget);
    setCreateState({ kind: "working" });
    try {
      const requestID = newIdempotencyKey();
      const expiry = String(data.get("expires_at") ?? "").trim();
      const result = await apiRequest<Grant>("/v1/share-grants", { method: "POST", idempotencyKey: requestID, accept: "application/vnd.lites.share-grant.v2+json", contentType: "application/vnd.lites.share-grant-create.v2+json", body: {
        request_id: requestID,
        grantee_user_id: String(data.get("grantee_user_id") ?? "").trim(),
        resource_kind: String(data.get("resource_kind") ?? ""),
        resource_id: String(data.get("resource_id") ?? "").trim(),
        resource_revision: String(data.get("resource_revision") ?? "").trim(),
        scope: data.getAll("scope").map(String),
        expires_at: expiry ? new Date(expiry).toISOString() : null,
      } });
      setGrant(result);
      setGrantID(result.id);
      setGrantVersion(String(result.version));
      setCreateState({ kind: "done", message: zh ? "显式共享已创建，只允许所选资源、修订与权限。" : "Explicit share created for only the selected resource, revision, and scopes." });
    } catch (error) {
      setCreateState({ kind: "error", message: messageFor(error, zh) });
    }
  }

  async function revokeGrant(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const data = new FormData(event.currentTarget);
    setRevokeState({ kind: "working" });
    try {
      const requestID = newIdempotencyKey();
      const version = Number(grantVersion);
      const result = await apiRequest<Grant>(`/v1/share-grants/${encodeURIComponent(grantID.trim())}`, { method: "DELETE", idempotencyKey: requestID, ifMatch: `"${version}"`, accept: "application/vnd.lites.share-grant.v2+json", contentType: "application/vnd.lites.share-grant-revoke.v2+json", body: { request_id: requestID, reason: String(data.get("reason") ?? "").trim(), expected_grant_version: version } });
      setGrant(result);
      setGrantVersion(String(result.version));
      setRevokeState({ kind: "done", message: zh ? "共享已撤销；服务端立即拒绝后续新读取。" : "Share revoked; the service immediately rejects subsequent new reads." });
    } catch (error) {
      setRevokeState({ kind: "error", message: messageFor(error, zh) });
    }
  }

  return <section className="admin-section evidence-sharing" aria-labelledby="sharing-title">
    <div className="admin-section-head"><div><p className="section-label"><ShieldCheck aria-hidden="true" /> {zh ? "显式共享" : "Explicit sharing"}</p><h2 id="sharing-title">{zh ? "私密证据只有在你授权后才能被读取" : "Private evidence can be read only after you grant access"}</h2><p className="muted">{zh ? "授权绑定接收人、资源、精确修订和权限范围。管理员身份本身不能读取你的内容。" : "A grant binds the recipient, resource, exact revision, and scopes. Administrator status alone cannot read your content."}</p></div></div>
    <div className="admin-grid"><form className="card form-grid" onSubmit={createGrant}><div><p className="section-label"><Share2 aria-hidden="true" /> Share grant</p><h3>{zh ? "创建限界共享" : "Create a bounded share"}</h3></div><label className="field"><span>{zh ? "接收人 User ID" : "Grantee user ID"}</span><input name="grantee_user_id" required /></label><div className="admin-form-pair"><label className="field"><span>{zh ? "资源类型" : "Resource kind"}</span><select name="resource_kind" defaultValue="evidence"><option value="evidence">evidence</option><option value="artifact">artifact</option><option value="project">project</option><option value="workspace">workspace</option></select></label><label className="field"><span>{zh ? "失效时间（可选）" : "Expires at (optional)"}</span><input name="expires_at" type="datetime-local" /></label></div><label className="field"><span>Resource ID</span><input name="resource_id" required /></label><label className="field"><span>{zh ? "精确资源修订" : "Exact resource revision"}</span><input name="resource_revision" required maxLength={500} placeholder="sha256:…" /></label><fieldset className="field"><legend>{zh ? "允许范围" : "Allowed scopes"}</legend><label><input name="scope" type="checkbox" value="read" defaultChecked /> read</label><label><input name="scope" type="checkbox" value="review" /> review</label></fieldset><button className="button primary" disabled={createState.kind === "working"}>{zh ? "创建显式共享" : "Create explicit share"}</button><Note state={createState} /></form>
    <form className="card form-grid" onSubmit={revokeGrant}><div><p className="section-label">Revocation</p><h3>{zh ? "撤销共享" : "Revoke a share"}</h3><p className="muted">{zh ? "创建成功后 ID 与版本会自动带入；也可输入现有授权的精确版本。" : "A newly created grant fills these fields automatically; you may also enter an existing grant and exact version."}</p></div><label className="field"><span>Share grant ID</span><input name="grant_id" required value={grantID} onChange={(event) => setGrantID(event.target.value)} /></label><label className="field"><span>{zh ? "当前授权版本" : "Current grant version"}</span><input name="grant_version" type="number" min="1" required value={grantVersion} onChange={(event) => setGrantVersion(event.target.value)} /></label><label className="field"><span>{zh ? "撤销理由" : "Revocation reason"}</span><textarea name="reason" required maxLength={1000} /></label><button className="button danger" disabled={revokeState.kind === "working"}>{zh ? "撤销并立即阻止新读取" : "Revoke and block new reads"}</button><Note state={revokeState} />{grant && <dl className="admin-resource"><div><dt>ID</dt><dd><code>{grant.id}</code></dd></div><div><dt>{zh ? "状态" : "Status"}</dt><dd>{grant.status}</dd></div><div><dt>{zh ? "版本" : "Version"}</dt><dd>{grant.version}</dd></div></dl>}</form></div>
  </section>;
}
