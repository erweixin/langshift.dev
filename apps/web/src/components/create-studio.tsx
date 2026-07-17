"use client";

import { useState } from "react";
import { AlertCircle, ArrowRight, Check, CheckCircle2, Code2, Download, FilePenLine, FlaskConical, Layers3, LoaderCircle, Palette, ShieldCheck, Sparkles } from "lucide-react";
import type { Locale } from "@/i18n/config";
import { apiRequest, newIdempotencyKey } from "@/lib/api/client";

type ProjectKind = "code" | "writing" | "design";
type Phase = "brief" | "plan" | "provisioning" | "workspace" | "evaluating" | "evidence" | "reflection" | "complete";
type ProjectState = { id: string; version: number; workspaceVersion: number; workspaceRevision: string; milestoneID: string; milestoneVersion: number };

const formats: { id: ProjectKind; icon: typeof Code2; title: string; description: string }[] = [
  { id: "code", icon: Code2, title: "Code", description: "Service, tool, automation, or executable system" },
  { id: "writing", icon: FilePenLine, title: "Writing", description: "Essay, guide, case study, or technical argument" },
  { id: "design", icon: Palette, title: "Design", description: "Product flow, system design, or interaction study" },
];
const delay = (milliseconds: number) => new Promise((resolve) => window.setTimeout(resolve, milliseconds));

export function CreateStudio({ locale }: { locale: Locale }) {
  const zh = locale === "zh-CN";
  const demo = process.env.NEXT_PUBLIC_LITES_DEMO_MODE === "true";
  const [selected, setSelected] = useState<ProjectKind>("code");
  const [brief, setBrief] = useState("");
  const [phase, setPhase] = useState<Phase>("brief");
  const [deliverable, setDeliverable] = useState("");
  const [revisionNote, setRevisionNote] = useState("");
  const [reflection, setReflection] = useState("");
  const [error, setError] = useState("");
  const [project, setProject] = useState<ProjectState | null>(null);
  const [exportReady, setExportReady] = useState(false);

  const milestones = selected === "design"
    ? [zh ? "失败旅程与恢复边界" : "Failure journey and recovery boundaries", zh ? "可用性评审与修订" : "Usability review and revision"]
    : selected === "writing"
      ? [zh ? "论点、证据与反例" : "Argument, evidence, and counterexample", zh ? "独立评审与修订" : "Independent review and revision"]
      : [zh ? "生命周期契约与失败边界" : "Lifecycle contract and failure boundaries", zh ? "恢复测试与运行说明" : "Recovery tests and operating notes"];

  async function provision() {
    if (!navigator.onLine) { setError(zh ? "离线时无法创建项目；项目尚未写入服务器。" : "A project cannot be created offline; nothing was written to the server."); return; }
    setError(""); setPhase("provisioning");
    try {
      if (demo) {
        await delay(550);
        setProject({ id: "demo-project", version: 4, workspaceVersion: 1, workspaceRevision: "demo:workspace:initial", milestoneID: "demo-milestone-1", milestoneVersion: 1 });
        setPhase("workspace"); return;
      }
      const missions = await apiRequest<{ items: { id: string; current_route_revision_id: string | null; focused: boolean }[]; focus: { mission_id: string | null } }>("/v1/missions");
      const mission = missions.items.find((item) => item.id === missions.focus.mission_id) ?? missions.items.find((item) => item.focused);
      if (!mission?.current_route_revision_id) throw new Error("accepted_route_required");
      const created = await apiRequest<{ id: string; version: number }>("/v1/projects", {
        method: "POST", idempotencyKey: newIdempotencyKey(), contentType: "application/vnd.lites.project-create.v2+json",
        body: { request_id: newIdempotencyKey(), mission_id: mission.id, accepted_route_revision_id: mission.current_route_revision_id, project_kind: selected, title: brief.trim().slice(0, 120), brief: brief.trim() },
      });
      const workspaceID = crypto.randomUUID(); const baseRevision = `workspace:${workspaceID}:initial`;
      const bound = await apiRequest<{ version: number; workspace: { version: number; head_revision: string } }>(`/v1/projects/${created.id}/workspace`, {
        method: "POST", idempotencyKey: newIdempotencyKey(), ifMatch: `"${created.version}"`, contentType: "application/vnd.lites.project-workspace-bind.v2+json",
        body: { request_id: newIdempotencyKey(), workspace_id: workspaceID, branch_name: `project/${created.id.slice(0, 12)}`, base_revision: baseRevision, expected_project_version: created.version },
      });
      let version = bound.version; let firstMilestone: { milestone_id: string; milestone_version: number } | null = null;
      for (const [index, title] of milestones.entries()) {
        const milestone = await apiRequest<{ version: number; milestone_id: string; milestone_version: number }>(`/v1/projects/${created.id}/milestones`, {
          method: "POST", idempotencyKey: newIdempotencyKey(), ifMatch: `"${version}"`, contentType: "application/vnd.lites.milestone-create.v2+json",
          body: { request_id: newIdempotencyKey(), title, required: true, sequence: index + 1, acceptance_spec: { schema_version: 1, must_pass: true, evidence_required: true }, expected_project_version: version },
        });
        version = milestone.version; if (index === 0) firstMilestone = milestone;
      }
      if (!firstMilestone) throw new Error("milestone_creation_failed");
      setProject({ id: created.id, version, workspaceVersion: bound.workspace.version, workspaceRevision: bound.workspace.head_revision, milestoneID: firstMilestone.milestone_id, milestoneVersion: firstMilestone.milestone_version });
      setPhase("workspace");
    } catch (cause) { setError(cause instanceof Error ? cause.message : "project_creation_failed"); setPhase("plan"); }
  }

  async function requestEvaluation() {
    if (!project || !deliverable.trim()) return;
    if (!navigator.onLine) { setError(zh ? "离线时不能提交评估；Workspace 内容仍可继续编辑。" : "Evaluation cannot be requested offline; the Workspace remains editable."); return; }
    setError(""); setPhase("evaluating");
    try {
      if (demo) { await delay(850); setPhase("evidence"); return; }
      const started = await apiRequest<{ version: number; milestone_version: number }>(`/v1/projects/${project.id}/milestones/${project.milestoneID}`, {
        method: "PATCH", idempotencyKey: newIdempotencyKey(), ifMatch: `"${project.version}"`, contentType: "application/vnd.lites.milestone-transition.v2+json",
        body: { request_id: newIdempotencyKey(), action: "start", expected_project_version: project.version, expected_milestone_version: project.milestoneVersion },
      });
      const submitted = await apiRequest<{ version: number; milestone_version: number }>(`/v1/projects/${project.id}/milestones/${project.milestoneID}`, {
        method: "PATCH", idempotencyKey: newIdempotencyKey(), ifMatch: `"${started.version}"`, contentType: "application/vnd.lites.milestone-transition.v2+json",
        body: { request_id: newIdempotencyKey(), action: "submit", result: deliverable.trim(), expected_project_version: started.version, expected_milestone_version: started.milestone_version },
      });
      await apiRequest(`/v1/projects/${project.id}/test-runs`, {
        method: "POST", idempotencyKey: newIdempotencyKey(), ifMatch: `"${submitted.version}"`, contentType: "application/vnd.lites.project-test-generation.v2+json",
        body: { request_id: newIdempotencyKey(), milestone_id: project.milestoneID, validation_kind: selected === "code" ? "deterministic_test" : "rubric_review", validation_spec: { schema_version: 1, requires_observed_evidence: true, no_self_attestation: true }, workspace_revision: project.workspaceRevision, expected_project_version: submitted.version, expected_milestone_version: submitted.milestone_version, expected_workspace_binding_version: project.workspaceVersion },
      });
      setProject({ ...project, version: submitted.version, milestoneVersion: submitted.milestone_version });
    } catch (cause) { setError(cause instanceof Error ? cause.message : "evaluation_request_failed"); setPhase("workspace"); }
  }

  async function createRevision() {
    if (!revisionNote.trim()) return;
    if (!demo) { setError(zh ? "正在等待可信评估事件；Artifact 只能在扫描与证据绑定完成后生成。" : "Waiting for the trusted evaluation event; an Artifact is created only after scan and evidence binding complete."); return; }
    setError(""); await delay(500); setPhase("reflection");
  }
  async function completeProject() { if (reflection.trim() && demo) { await delay(500); setPhase("complete"); } }
  async function buildExport() { setExportReady(false); await delay(650); setExportReady(true); }

  const steps = [
    { id: "workspace", label: zh ? "项目与 Workspace" : "Project & Workspace", done: !["brief", "plan", "provisioning"].includes(phase) },
    { id: "milestones", label: zh ? "两项必需里程碑" : "Two required milestones", done: !["brief", "plan", "provisioning"].includes(phase) },
    { id: "evaluation", label: zh ? "独立测试 / Rubric" : "Independent test / rubric", done: ["evidence", "reflection", "complete"].includes(phase) },
    { id: "artifact", label: zh ? "证据与 Artifact 修订" : "Evidence & Artifact revision", done: ["reflection", "complete"].includes(phase) },
    { id: "reflection", label: zh ? "反思、完成与导出" : "Reflection, completion & export", done: phase === "complete" },
  ];

  return <div className="page-wrap create-studio">
    <header className="page-head"><div><p className="eyebrow">{zh ? "创造" : "Create"}</p><h1>{zh ? "把能力放进真实作品。" : "Put capability into real work."}</h1><p>{zh ? "每个项目都经过计划、Workspace、独立验证、证据绑定、修订和反思；排队回执不会冒充完成。" : "Every project moves through planning, Workspace, independent validation, evidence binding, revision, and reflection; a queue receipt never masquerades as completion."}</p></div></header>
    {phase !== "brief" && <ol className="create-progress" aria-label={zh ? "项目生命周期" : "Project lifecycle"}>{steps.map((step, index) => <li className={step.done ? "done" : ""} key={step.id}><span>{step.done ? <Check aria-hidden="true" /> : index + 1}</span>{step.label}</li>)}</ol>}

    {phase === "brief" && <div className="create-layout"><section><p className="section-label">01 · {zh ? "选择形式" : "Choose a form"}</p><div className="format-grid">{formats.map(({ id, icon: Icon, title, description }) => <button type="button" aria-pressed={selected === id} className={selected === id ? "format-card selected" : "format-card"} onClick={() => setSelected(id)} key={id}><Icon aria-hidden="true" /><strong>{title}</strong><span>{description}</span></button>)}</div></section><section className="card create-brief"><p className="section-label">02 · {zh ? "定义真实用途" : "Define a real use"}</p><h2>{zh ? "你希望这个作品为谁解决什么？" : "What should this work solve, and for whom?"}</h2><label className="field"><span>{zh ? "项目简述" : "Project brief"}</span><textarea name="project-brief" autoComplete="off" required value={brief} onChange={(event) => setBrief(event.target.value)} placeholder={zh ? "例如：为平台工程团队设计一个可以安全重试的 Agent 运行服务…" : "For example: design a safely retryable agent run service for a platform engineering team…"} /></label><div className="coach-suggestion"><Sparkles aria-hidden="true" /><p><strong>Coach suggestion</strong><br />{zh ? "当前证据缺口是副作用对账。让项目包含一次故障恢复演练，会比增加功能更有价值。" : "Your current evidence gap is effect reconciliation. A failure-recovery exercise would add more value than another feature."}</p></div><button className="button primary" type="button" disabled={!brief.trim()} onClick={() => setPhase("plan")}>{zh ? "生成项目计划" : "Generate project plan"}<ArrowRight aria-hidden="true" /></button></section></div>}

    {(phase === "plan" || phase === "provisioning") && <section className="workspace-created card"><span className="mode-icon"><Layers3 /></span><p className="eyebrow">{zh ? "计划待确认" : "Plan ready for confirmation"}</p><h1>{zh ? "先定义成功，再开始执行。" : "Define success before execution starts."}</h1><div className="workspace-columns"><div><strong>Milestone 1 · Required</strong><p>{milestones[0]}</p></div><div><strong>Milestone 2 · Required</strong><p>{milestones[1]}</p></div><div><strong>{selected === "code" ? "Deterministic test" : "Independent rubric"}</strong><p>{zh ? "验证精确 Workspace revision，并生成可追溯 Evidence。" : "Validates the exact Workspace revision and produces traceable Evidence."}</p></div></div>{error && <p className="error-note" role="alert"><AlertCircle />{error}</p>}<button className="button primary" type="button" onClick={provision} disabled={phase === "provisioning"}>{phase === "provisioning" ? <><LoaderCircle className="spinner-icon" />{zh ? "正在原子创建…" : "Creating atomically…"}</> : <>{zh ? "创建项目、Workspace 与里程碑" : "Create project, Workspace & milestones"}<ArrowRight /></>}</button></section>}

    {phase === "workspace" && <section className="create-workspace"><div className="card workspace-console"><div><p className="section-label">Workspace · {selected}</p><h2>{milestones[0]}</h2><p>{zh ? "提交的是这次精确修订，而不是一段“已经完成”的声明。" : "The exact revision is evaluated—not a prose claim that the work is complete."}</p></div><label className="field"><span>{zh ? "工作成果 / revision 摘要" : "Work output / revision summary"}</span><textarea name="workspace-output" data-testid="workspace-output" autoComplete="off" required value={deliverable} onChange={(event) => setDeliverable(event.target.value)} placeholder={zh ? "记录实现、设计或文章修订，以及验证它所需的可观察结果…" : "Record the implementation, design, or writing revision and the observable result needed to validate it…"} /></label>{error && <p className="error-note" role="alert"><AlertCircle />{error}</p>}<button className="button primary" type="button" disabled={!deliverable.trim()} onClick={requestEvaluation}><FlaskConical />{zh ? "提交精确修订并请求独立评估" : "Submit exact revision for independent evaluation"}</button></div><aside className="card acceptance-panel"><p className="section-label">Acceptance</p><ul><li><Check />{milestones[0]}</li><li><Check />{zh ? "测试或 Rubric 观察实际结果" : "Test or rubric observes the actual result"}</li><li><Check />{zh ? "失败状态不会生成 Evidence" : "A failed result creates no passing Evidence"}</li></ul></aside></section>}

    {phase === "evaluating" && <section className="workspace-created card" aria-live="polite"><LoaderCircle className="spinner-icon create-loader" /><p className="eyebrow">{zh ? "评估已排队" : "Evaluation queued"}</p><h1>{zh ? "等待独立结果，不会提前宣告通过。" : "Waiting for an independent result—no premature pass."}</h1><p className="muted">{demo ? (zh ? "Demo 评估器正在检查精确 revision。" : "The demo evaluator is checking the exact revision.") : (zh ? "服务器已确认请求。结果将由实时事件带回，并绑定测试运行、Workspace revision 与 Evidence。" : "The server confirmed the request. A realtime event will return the result bound to the test run, Workspace revision, and Evidence.")}</p></section>}

    {phase === "evidence" && <section className="create-workspace"><div className="card evidence-verdict"><ShieldCheck /><p className="eyebrow">{zh ? "独立评估通过" : "Independent evaluation passed"}</p><h2>{zh ? "Evidence 已绑定到精确修订" : "Evidence is bound to the exact revision"}</h2><p>{zh ? "依据：观察到的验收结果。等级：已在项目中应用。没有从一次结果推导精确能力百分比。" : "Basis: observed acceptance results. Level: Applied. No exact capability percentage is inferred from one result."}</p><dl><div><dt>Workspace revision</dt><dd>demo:workspace:revision-1</dd></div><div><dt>Test run</dt><dd>demo:test-run:verified</dd></div><div><dt>Evidence</dt><dd>Applied · independently observed</dd></div></dl></div><div className="card form-grid"><p className="section-label">Artifact revision</p><h2>{milestones[1]}</h2><label className="field"><span>{zh ? "依据评审做了什么修订？" : "What changed in response to the review?"}</span><textarea name="artifact-revision" autoComplete="off" required value={revisionNote} onChange={(event) => setRevisionNote(event.target.value)} placeholder={zh ? "说明修改、证据依据和剩余不确定性…" : "Describe the change, evidence basis, and remaining uncertainty…"} /></label>{error && <p className="error-note" role="alert"><AlertCircle />{error}</p>}<button className="button primary" type="button" disabled={!revisionNote.trim()} onClick={createRevision}>{zh ? "创建并扫描 Artifact 修订" : "Create and scan Artifact revision"}<ArrowRight /></button></div></section>}

    {phase === "reflection" && <section className="card reflection-card"><p className="eyebrow">{zh ? "最后一步" : "Final required step"}</p><h1>{zh ? "把做法变成可迁移的判断。" : "Turn the work into a transferable judgment."}</h1><label className="field"><span>{zh ? "项目反思" : "Project reflection"}</span><textarea name="project-reflection" autoComplete="off" required value={reflection} onChange={(event) => setReflection(event.target.value)} placeholder={zh ? "什么判断经受住了验证？什么边界仍不确定？下次你会如何更早验证？" : "Which judgment held up? What remains uncertain? What would you validate earlier next time?"} /></label><button className="button primary" type="button" disabled={!reflection.trim()} onClick={completeProject}><CheckCircle2 />{zh ? "完成项目" : "Complete project"}</button></section>}

    {phase === "complete" && <section className="workspace-created card"><span className="review-seal"><CheckCircle2 /></span><p className="eyebrow">{zh ? "项目已完成" : "Project complete"}</p><h1>{zh ? "作品、证据与反思现在共享同一条版本链。" : "Work, evidence, and reflection now share one version chain."}</h1><div className="workspace-columns"><div><strong>2 / 2 Milestones</strong><p>{zh ? "全部必需条件已验证" : "All required conditions verified"}</p></div><div><strong>Artifact revision 1</strong><p>{zh ? "扫描通过并绑定 Evidence" : "Scan passed and Evidence bound"}</p></div><div><strong>Evidence · Applied</strong><p>{zh ? "基于观察结果，而非自我声明" : "Based on observed results, not self-attestation"}</p></div></div>{exportReady ? <button className="button primary" type="button"><Download />{zh ? "下载已验证项目包" : "Download verified project package"}</button> : <button className="button primary" type="button" onClick={buildExport}><Download />{zh ? "构建精确版本作品集导出" : "Build exact-revision portfolio export"}</button>}{exportReady && <p role="status" className="pending-note">{zh ? "可信收据已验证，下载现已可用。" : "The trusted receipt is verified; download is now available."}</p>}</section>}
  </div>;
}
