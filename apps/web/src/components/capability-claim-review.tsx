"use client";

import { useEffect, useMemo, useState } from "react";
import { AlertCircle, Check, LoaderCircle, Pencil, ShieldQuestion } from "lucide-react";
import type { Locale } from "@/i18n/config";
import { ApiError, apiRequest, newIdempotencyKey } from "@/lib/api/client";
import type { RouteDocument, RouteRevision } from "@/lib/use-product-workspace";

type Assessment = RouteDocument["transferable_experience"][number];
type ReviewAction = "confirm" | "correct" | "dispute";

type CapabilityClaim = {
  id: string;
  version: number;
  mission_id: string;
  capability_id: string;
  status: string;
  statement: string;
};

type ClaimMutation = {
  id: string;
  version: number;
  status: string;
  claim_set_hash: string;
};

export function CapabilityClaimReview({ locale, missionID, routeRevision, assessments, onChanged }: {
  locale: Locale;
  missionID: string;
  routeRevision: RouteRevision;
  assessments: Assessment[];
  onChanged: () => Promise<void>;
}) {
  const zh = locale === "zh-CN";
  const [claims, setClaims] = useState<CapabilityClaim[]>([]);
  const [selected, setSelected] = useState<{ key: string; action: ReviewAction } | null>(null);
  const [draft, setDraft] = useState("");
  const [pending, setPending] = useState("");
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");

  useEffect(() => {
    const controller = new AbortController();
    apiRequest<{ items: CapabilityClaim[] }>("/v1/capability-claims", { signal: controller.signal })
      .then((result) => setClaims(result.items.filter((item) => item.mission_id === missionID)))
      .catch((caught) => {
        if (caught instanceof DOMException && caught.name === "AbortError") return;
        setError(zh ? "暂时无法读取能力声明。" : "Capability claims could not be loaded.");
      });
    return () => controller.abort();
  }, [missionID, zh]);

  useEffect(() => {
    if (routeRevision.status !== "generating") return;
    let active = true;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const poll = async () => {
      await onChanged();
      if (active) timer = setTimeout(() => void poll(), 2_000);
    };
    timer = setTimeout(() => void poll(), 1_500);
    return () => {
      active = false;
      if (timer) clearTimeout(timer);
    };
  }, [onChanged, routeRevision.status]);

  const claimByAssessment = useMemo(() => new Map(assessments.map((assessment, index) => {
    const key = assessmentKey(assessment, index);
    const match = claims.find((claim) => claim.mission_id === missionID && claim.capability_id === assessment.capability_ids[0] && claim.statement === assessment.statement)
      ?? claims.find((claim) => claim.mission_id === missionID && claim.capability_id === assessment.capability_ids[0]);
    return [key, match] as const;
  })), [assessments, claims, missionID]);

  async function review(assessment: Assessment, index: number, action: ReviewAction) {
    const key = assessmentKey(assessment, index);
    const capabilityID = assessment.capability_ids[0];
    if (!capabilityID || pending) return;
    if ((action === "correct" || action === "dispute") && selected?.key !== key) {
      setSelected({ key, action });
      setDraft(action === "correct" ? assessment.statement : "");
      return;
    }
    if ((action === "correct" || action === "dispute") && !draft.trim()) return;
    setPending(key);
    setError("");
    setNotice("");
    try {
      let claim = claimByAssessment.get(key);
      if (!claim) {
        const created = await apiRequest<ClaimMutation>("/v1/capability-claims", {
          method: "POST",
          idempotencyKey: newIdempotencyKey(),
          body: {
            request_id: newIdempotencyKey(),
            mission_id: missionID,
            capability_id: capabilityID,
            origin: "inferred",
            statement: assessment.statement,
            evidence_ids: assessment.evidence_ids,
          },
        });
        claim = { id: created.id, version: created.version, mission_id: missionID, capability_id: capabilityID, status: created.status, statement: assessment.statement };
      }
      const reason = action === "confirm"
        ? "User explicitly confirmed the route assessment"
        : action === "correct"
          ? "User corrected the route assessment"
          : draft.trim();
      const mutation = await apiRequest<ClaimMutation>(`/v1/capability-claims/${claim.id}/revisions`, {
        method: "POST",
        idempotencyKey: newIdempotencyKey(),
        ifMatch: `"${claim.version}"`,
        body: {
          request_id: newIdempotencyKey(),
          action,
          reason,
          evidence_ids: action === "confirm" ? assessment.evidence_ids : [],
          expected_claim_version: claim.version,
          ...(action === "correct" ? { capability_id: capabilityID, statement: draft.trim() } : {}),
        },
      });
      await apiRequest("/v1/route-revisions", {
        method: "POST",
        idempotencyKey: newIdempotencyKey(),
        ifMatch: `"${routeRevision.route_version}"`,
        contentType: "application/vnd.lites.route-generate.v2+json",
        accept: "application/vnd.lites.route-generation.v2+json",
        body: {
          request_id: newIdempotencyKey(),
          mission_id: missionID,
          expected_route_version: routeRevision.route_version,
          expected_claim_set_hash: mutation.claim_set_hash,
        },
      });
      setClaims((items) => [...items.filter((item) => item.id !== mutation.id), { ...claim, version: mutation.version, status: mutation.status, statement: action === "correct" ? draft.trim() : claim.statement }]);
      setSelected(null);
      setDraft("");
      setNotice(zh ? "能力声明已持久化；新的路线修订正在生成。" : "The claim is durable and a new route revision is generating.");
      await onChanged();
    } catch (caught) {
      const code = caught instanceof ApiError ? caught.message : "request_failed";
      setError(zh ? `未保存更改（${code}）。请刷新后重试。` : `The change was not saved (${code}). Refresh and retry.`);
    } finally {
      setPending("");
    }
  }

  async function acceptProposedRoute() {
    if (pending || routeRevision.status !== "proposed") return;
    setPending("accept-route");
    setError("");
    try {
      await apiRequest(`/v1/route-revisions/${routeRevision.id}/accept`, {
        method: "POST",
        idempotencyKey: newIdempotencyKey(),
        ifMatch: `"${routeRevision.version}"`,
        contentType: "application/vnd.lites.route-accept.v2+json",
        accept: "application/vnd.lites.route-acceptance.v2+json",
        body: {
          request_id: newIdempotencyKey(),
          expected_revision_version: routeRevision.version,
          expected_route_version: routeRevision.base_route_version,
          expected_claim_set_hash: routeRevision.claim_set_hash,
        },
      });
      setNotice(zh ? "新的路线修订已接受。" : "The new route revision is accepted.");
      await onChanged();
    } catch (caught) {
      const code = caught instanceof ApiError ? caught.message : "request_failed";
      setError(zh ? `路线未接受（${code}）。请刷新后重试。` : `The route was not accepted (${code}). Refresh and retry.`);
    } finally {
      setPending("");
    }
  }

  if (routeRevision.status === "generating") {
    return <section className="card" aria-live="polite"><LoaderCircle className="spin" /><h2>{zh ? "新的路线修订正在生成" : "A new route revision is generating"}</h2><p role="status">{notice || (zh ? "当前 claim revision 已保存。Planner 完成后可在此接受新路线。" : "The claim revision is saved. You can accept the route here when Planner finishes.")}</p>{error && <p className="error-note" role="alert"><AlertCircle />{error}</p>}</section>;
  }

  return <section className="card">
    <p className="section-label">{zh ? "持久化能力校准" : "Durable capability calibration"}</p>
    <h2>{routeRevision.status === "proposed" ? (zh ? "检查并接受新路线" : "Review and accept the new route") : (zh ? "确认、纠正或反驳判断" : "Confirm, correct, or dispute judgments")}</h2>
    <p>{zh ? "每次操作都写入不可变 capability claim revision，使旧路线失效，并以当前 claim set 生成新路线。" : "Every action writes an immutable capability claim revision, stales the old route, and generates a route from the current claim set."}</p>
    {error && <p className="error-note" role="alert"><AlertCircle />{error}</p>}
    {notice && <p className="success-note" role="status"><Check />{notice}</p>}
    {routeRevision.status === "proposed" && <button className="button primary" type="button" disabled={Boolean(pending)} onClick={acceptProposedRoute}>{pending === "accept-route" && <LoaderCircle className="spin" />}{zh ? "接受此路线修订" : "Accept this route revision"}</button>}
    {routeRevision.status === "accepted" && <div className="claim-review-list">{assessments.map((assessment, index) => {
      const key = assessmentKey(assessment, index);
      const editor = selected?.key === key ? selected.action : null;
      return <article className="map-capability" key={key}><div><strong>{assessment.statement}</strong><p>{claimByAssessment.get(key) ? (zh ? "已有可修订声明" : "Durable claim available") : (zh ? "Planner 推断，尚待用户声明" : "Planner inference awaiting user claim")}</p>{editor && <label className="field"><span>{editor === "correct" ? (zh ? "正确的能力判断" : "Correct assessment") : (zh ? "反驳理由" : "Reason for dispute")}</span><textarea value={draft} onChange={(event) => setDraft(event.target.value)} /></label>}<div className="flow-actions"><button className="button ghost" type="button" disabled={Boolean(pending)} onClick={() => void review(assessment, index, "confirm")}><Check />{zh ? "确认" : "Confirm"}</button><button className="button ghost" type="button" disabled={Boolean(pending)} onClick={() => void review(assessment, index, "correct")}><Pencil />{editor === "correct" ? (zh ? "保存纠正" : "Save correction") : (zh ? "纠正" : "Correct")}</button><button className="button ghost" type="button" disabled={Boolean(pending)} onClick={() => void review(assessment, index, "dispute")}><ShieldQuestion />{editor === "dispute" ? (zh ? "提交反驳" : "Submit dispute") : (zh ? "反驳" : "Dispute")}</button>{pending === key && <LoaderCircle className="spin" />}</div></div></article>;
    })}</div>}
  </section>;
}

function assessmentKey(assessment: Assessment, index: number) {
  return `${index}:${assessment.capability_ids.join(",")}:${assessment.statement}`;
}
