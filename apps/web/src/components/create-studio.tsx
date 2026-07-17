"use client";

import { useState } from "react";
import {
  AlertCircle,
  ArrowRight,
  Check,
  CheckCircle2,
  Code2,
  Download,
  FilePenLine,
  FlaskConical,
  Layers3,
  LoaderCircle,
  Palette,
  ShieldCheck,
  Sparkles,
} from "lucide-react";
import type { Locale } from "@/i18n/config";
import { apiDownload, apiRequest, newIdempotencyKey } from "@/lib/api/client";

type ProjectKind = "code" | "writing" | "design";
type Phase =
  | "brief"
  | "plan"
  | "provisioning"
  | "workspace"
  | "evaluating"
  | "evidence"
  | "reflection"
  | "complete";
type ProjectState = {
  id: string;
  version: number;
  workspaceID: string;
  workspaceVersion: number;
  workspaceRevision: string;
  milestones: { id: string; version: number; status: string; title: string }[];
  currentMilestone: number;
  evidenceIDs: string[];
  outputs: string[];
  artifactID?: string;
  artifactVersion?: number;
  artifactRevisionIDs: string[];
};
type Evidence = {
  id: string;
  version: number;
  source_id: string | null;
  content: {
    evaluation?: { result?: string; summary?: string; uncertainty?: string };
  };
};
type ExportState = {
  id: string;
  status: string;
  version: number;
  content_hash: string | null;
  media_type: string | null;
  byte_size: number | null;
  failure_code: string | null;
};

const formats: {
  id: ProjectKind;
  icon: typeof Code2;
  title: string;
  description: string;
}[] = [
  {
    id: "code",
    icon: Code2,
    title: "Code",
    description: "Service, tool, automation, or executable system",
  },
  {
    id: "writing",
    icon: FilePenLine,
    title: "Writing",
    description: "Essay, guide, case study, or technical argument",
  },
  {
    id: "design",
    icon: Palette,
    title: "Design",
    description: "Product flow, system design, or interaction study",
  },
];
const delay = (milliseconds: number) =>
  new Promise((resolve) => window.setTimeout(resolve, milliseconds));
async function inlineRevision(content: string) {
  const digest = await crypto.subtle.digest(
    "SHA-256",
    new TextEncoder().encode(content),
  );
  return `inline:sha256:${Array.from(new Uint8Array(digest), (byte) => byte.toString(16).padStart(2, "0")).join("")}`;
}

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
  const [lastEvidence, setLastEvidence] = useState<Evidence | null>(null);
  const [portfolioExport, setPortfolioExport] = useState<ExportState | null>(
    null,
  );
  const [downloading, setDownloading] = useState(false);

  const milestones =
    selected === "design"
      ? [
          zh ? "失败旅程与恢复边界" : "Failure journey and recovery boundaries",
          zh ? "可用性评审与修订" : "Usability review and revision",
        ]
      : selected === "writing"
        ? [
            zh ? "论点、证据与反例" : "Argument, evidence, and counterexample",
            zh ? "独立评审与修订" : "Independent review and revision",
          ]
        : [
            zh
              ? "生命周期契约与失败边界"
              : "Lifecycle contract and failure boundaries",
            zh ? "恢复测试与运行说明" : "Recovery tests and operating notes",
          ];

  async function provision() {
    if (!navigator.onLine) {
      setError(
        zh
          ? "离线时无法创建项目；项目尚未写入服务器。"
          : "A project cannot be created offline; nothing was written to the server.",
      );
      return;
    }
    setError("");
    setPhase("provisioning");
    try {
      if (demo) {
        await delay(550);
        setProject({
          id: "demo-project",
          version: 4,
          workspaceID: "demo-workspace",
          workspaceVersion: 1,
          workspaceRevision: "demo:workspace:initial",
          milestones: [
            {
              id: "demo-milestone-1",
              version: 1,
              status: "planned",
              title: milestones[0] ?? "Required milestone",
            },
          ],
          currentMilestone: 0,
          evidenceIDs: [],
          outputs: [],
          artifactRevisionIDs: [],
        });
        setPhase("workspace");
        return;
      }
      const missions = await apiRequest<{
        items: {
          id: string;
          current_route_revision_id: string | null;
          focused: boolean;
        }[];
        focus: { mission_id: string | null };
      }>("/v1/missions");
      const mission =
        missions.items.find((item) => item.id === missions.focus.mission_id) ??
        missions.items.find((item) => item.focused);
      if (!mission?.current_route_revision_id)
        throw new Error("accepted_route_required");
      const created = await apiRequest<{ id: string; version: number }>(
        "/v1/projects",
        {
          method: "POST",
          idempotencyKey: newIdempotencyKey(),
          contentType: "application/vnd.lites.project-create.v2+json",
          body: {
            request_id: newIdempotencyKey(),
            mission_id: mission.id,
            accepted_route_revision_id: mission.current_route_revision_id,
            project_kind: selected,
            title: brief.trim().slice(0, 120),
            brief: brief.trim(),
          },
        },
      );
      const workspaceID = crypto.randomUUID();
      const baseRevision = `inline:${workspaceID}:empty`;
      const bound = await apiRequest<{
        version: number;
        workspace: { id: string; version: number; head_revision: string };
      }>(`/v1/projects/${created.id}/workspace`, {
        method: "POST",
        idempotencyKey: newIdempotencyKey(),
        ifMatch: `"${created.version}"`,
        contentType: "application/vnd.lites.project-workspace-bind.v2+json",
        body: {
          request_id: newIdempotencyKey(),
          workspace_id: workspaceID,
          branch_name: `project/${created.id.slice(0, 12)}`,
          base_revision: baseRevision,
          expected_project_version: created.version,
        },
      });
      let version = bound.version;
      const createdMilestones: ProjectState["milestones"] = [];
      for (const [index, title] of milestones.entries()) {
        const milestone = await apiRequest<{
          version: number;
          milestone_id: string;
          milestone_version: number;
        }>(`/v1/projects/${created.id}/milestones`, {
          method: "POST",
          idempotencyKey: newIdempotencyKey(),
          ifMatch: `"${version}"`,
          contentType: "application/vnd.lites.milestone-create.v2+json",
          body: {
            request_id: newIdempotencyKey(),
            title,
            required: true,
            sequence: index + 1,
            acceptance_spec: {
              schema_version: 1,
              must_pass: true,
              evidence_required: true,
            },
            expected_project_version: version,
          },
        });
        version = milestone.version;
        createdMilestones.push({
          id: milestone.milestone_id,
          version: milestone.milestone_version,
          status: "planned",
          title,
        });
      }
      if (createdMilestones.length !== milestones.length)
        throw new Error("milestone_creation_failed");
      setProject({
        id: created.id,
        version,
        workspaceID: bound.workspace.id,
        workspaceVersion: bound.workspace.version,
        workspaceRevision: bound.workspace.head_revision,
        milestones: createdMilestones,
        currentMilestone: 0,
        evidenceIDs: [],
        outputs: [],
        artifactRevisionIDs: [],
      });
      setPhase("workspace");
    } catch (cause) {
      setError(
        cause instanceof Error ? cause.message : "project_creation_failed",
      );
      setPhase("plan");
    }
  }

  async function requestEvaluation() {
    if (!project || !deliverable.trim()) return;
    if (!navigator.onLine) {
      setError(
        zh
          ? "离线时不能提交评估；Workspace 内容仍可继续编辑。"
          : "Evaluation cannot be requested offline; the Workspace remains editable.",
      );
      return;
    }
    setError("");
    setPhase("evaluating");
    try {
      if (demo) {
        await delay(850);
        setPhase("evidence");
        return;
      }
      const milestone = project.milestones[project.currentMilestone];
      if (!milestone) throw new Error("milestone_not_available");
      const nextRevision = await inlineRevision(deliverable.trim());
      if (nextRevision === project.workspaceRevision)
        throw new Error("workspace_revision_unchanged");
      const advanced = await apiRequest<{
        version: number;
        workspace: { id: string; version: number; head_revision: string };
      }>(`/v1/projects/${project.id}/workspace`, {
        method: "PATCH",
        idempotencyKey: newIdempotencyKey(),
        ifMatch: `"${project.version}"`,
        contentType: "application/vnd.lites.project-workspace-advance.v2+json",
        body: {
          request_id: newIdempotencyKey(),
          binding_id: project.workspaceID,
          expected_head_revision: project.workspaceRevision,
          head_revision: nextRevision,
          expected_project_version: project.version,
          expected_binding_version: project.workspaceVersion,
        },
      });
      const started = await apiRequest<{
        version: number;
        milestone_version: number;
        milestone_status: string;
      }>(`/v1/projects/${project.id}/milestones/${milestone.id}`, {
        method: "PATCH",
        idempotencyKey: newIdempotencyKey(),
        ifMatch: `"${advanced.version}"`,
        contentType: "application/vnd.lites.milestone-transition.v2+json",
        body: {
          request_id: newIdempotencyKey(),
          action: "start",
          expected_project_version: advanced.version,
          expected_milestone_version: milestone.version,
        },
      });
      const submitted = await apiRequest<{
        version: number;
        milestone_version: number;
        milestone_status: string;
      }>(`/v1/projects/${project.id}/milestones/${milestone.id}`, {
        method: "PATCH",
        idempotencyKey: newIdempotencyKey(),
        ifMatch: `"${started.version}"`,
        contentType: "application/vnd.lites.milestone-transition.v2+json",
        body: {
          request_id: newIdempotencyKey(),
          action: "submit",
          result: deliverable.trim(),
          expected_project_version: started.version,
          expected_milestone_version: started.milestone_version,
        },
      });
      const generation = await apiRequest<{ run_id: string }>(
        `/v1/projects/${project.id}/test-runs`,
        {
          method: "POST",
          idempotencyKey: newIdempotencyKey(),
          ifMatch: `"${submitted.version}"`,
          contentType: "application/vnd.lites.project-test-generation.v2+json",
          body: {
            request_id: newIdempotencyKey(),
            milestone_id: milestone.id,
            validation_kind: "rubric_review",
            validation_spec: {
              schema_version: 1,
              requires_observed_evidence: true,
              no_self_attestation: true,
              exact_inline_revision: nextRevision,
            },
            workspace_revision: nextRevision,
            expected_project_version: submitted.version,
            expected_milestone_version: submitted.milestone_version,
            expected_workspace_binding_version: advanced.workspace.version,
          },
        },
      );
      let evidence: Evidence | undefined;
      let reconciledVersion = submitted.version;
      for (let attempt = 0; attempt < 120; attempt += 1) {
        const [evidencePage, projectPage, run] = await Promise.all([
          apiRequest<{ items: Evidence[] }>("/v1/capability-evidence", {
            accept: "application/vnd.lites.evidence-list.v2+json",
          }),
          apiRequest<{ items: { id: string; version: number }[] }>(
            "/v1/projects",
            { accept: "application/vnd.lites.projects.v2+json" },
          ),
          apiRequest<{ status: string }>(`/v1/runs/${generation.run_id}`),
        ]);
        evidence = evidencePage.items.find(
          (item) => item.source_id === generation.run_id,
        );
        reconciledVersion =
          projectPage.items.find((item) => item.id === project.id)?.version ??
          submitted.version;
        if (evidence && reconciledVersion > submitted.version) break;
        if (["failed", "cancelled", "expired"].includes(run.status))
          throw new Error(`evaluation_${run.status}`);
        await delay(1000);
      }
      if (!evidence || reconciledVersion <= submitted.version)
        throw new Error("evaluation_timeout");
      setLastEvidence(evidence);
      if (evidence.content.evaluation?.result !== "passed") {
        const rework = await apiRequest<{
          version: number;
          milestone_version: number;
        }>(`/v1/projects/${project.id}/milestones/${milestone.id}`, {
          method: "PATCH",
          idempotencyKey: newIdempotencyKey(),
          ifMatch: `"${reconciledVersion}"`,
          contentType: "application/vnd.lites.milestone-transition.v2+json",
          body: {
            request_id: newIdempotencyKey(),
            action: "request_rework",
            expected_project_version: reconciledVersion,
            expected_milestone_version: submitted.milestone_version,
          },
        });
        const updatedMilestones = project.milestones.map((item, index) =>
          index === project.currentMilestone
            ? { ...item, version: rework.milestone_version, status: "rework" }
            : item,
        );
        setProject({
          ...project,
          version: rework.version,
          workspaceVersion: advanced.workspace.version,
          workspaceRevision: nextRevision,
          milestones: updatedMilestones,
        });
        setError(
          evidence.content.evaluation?.summary ??
            (zh
              ? "独立评估要求修订后重试。"
              : "The independent evaluation requires a revision."),
        );
        setPhase("workspace");
        return;
      }
      const verified = await apiRequest<{
        version: number;
        milestone_version: number;
      }>(`/v1/projects/${project.id}/milestones/${milestone.id}`, {
        method: "PATCH",
        idempotencyKey: newIdempotencyKey(),
        ifMatch: `"${reconciledVersion}"`,
        contentType: "application/vnd.lites.milestone-transition.v2+json",
        body: {
          request_id: newIdempotencyKey(),
          action: "verify",
          expected_project_version: reconciledVersion,
          expected_milestone_version: submitted.milestone_version,
        },
      });
      const completed = await apiRequest<{
        version: number;
        milestone_version: number;
      }>(`/v1/projects/${project.id}/milestones/${milestone.id}`, {
        method: "PATCH",
        idempotencyKey: newIdempotencyKey(),
        ifMatch: `"${verified.version}"`,
        contentType: "application/vnd.lites.milestone-transition.v2+json",
        body: {
          request_id: newIdempotencyKey(),
          action: "complete",
          expected_project_version: verified.version,
          expected_milestone_version: verified.milestone_version,
        },
      });
      const updatedMilestones = project.milestones.map((item, index) =>
        index === project.currentMilestone
          ? {
              ...item,
              version: completed.milestone_version,
              status: "completed",
            }
          : item,
      );
      const updated: ProjectState = {
        ...project,
        version: completed.version,
        workspaceVersion: advanced.workspace.version,
        workspaceRevision: nextRevision,
        milestones: updatedMilestones,
        evidenceIDs: [...project.evidenceIDs, evidence.id],
        outputs: [...project.outputs, deliverable.trim()],
      };
      if (project.currentMilestone + 1 < project.milestones.length) {
        setProject({
          ...updated,
          currentMilestone: project.currentMilestone + 1,
        });
        setDeliverable("");
        setPhase("workspace");
      } else {
        setProject(updated);
        setPhase("evidence");
      }
    } catch (cause) {
      setError(
        cause instanceof Error ? cause.message : "evaluation_request_failed",
      );
      setPhase("workspace");
    }
  }

  async function createRevision() {
    if (!revisionNote.trim()) return;
    if (!project) return;
    setError("");
    try {
      if (demo) {
        await delay(500);
        setPhase("reflection");
        return;
      }
      const artifact = await apiRequest<{ id: string; version: number }>(
        "/v1/artifacts",
        {
          method: "POST",
          idempotencyKey: newIdempotencyKey(),
          contentType: "application/vnd.lites.artifact-create.v2+json",
          body: {
            request_id: newIdempotencyKey(),
            project_id: project.id,
            artifact_kind: selected,
            title: brief.trim().slice(0, 200),
          },
        },
      );
      const content = [
        `# ${brief.trim()}`,
        ...project.outputs.map(
          (output, index) => `## ${milestones[index]}\n\n${output}`,
        ),
        `## Revision response\n\n${revisionNote.trim()}`,
      ].join("\n\n");
      const revision = await apiRequest<{
        id: string;
        artifact_version: number;
      }>(`/v1/artifacts/${artifact.id}/revisions`, {
        method: "POST",
        idempotencyKey: newIdempotencyKey(),
        ifMatch: `"${artifact.version}"`,
        contentType: "application/vnd.lites.artifact-revision-create.v2+json",
        body: {
          request_id: newIdempotencyKey(),
          content,
          media_type: "text/markdown",
          workspace_revision: project.workspaceRevision,
          evidence_ids: project.evidenceIDs,
          expected_artifact_version: artifact.version,
        },
      });
      setProject({
        ...project,
        artifactID: artifact.id,
        artifactVersion: revision.artifact_version,
        artifactRevisionIDs: [revision.id],
      });
      setPhase("reflection");
    } catch (cause) {
      setError(
        cause instanceof Error ? cause.message : "artifact_revision_failed",
      );
    }
  }
  async function completeProject() {
    if (!reflection.trim() || !project) return;
    setError("");
    try {
      if (demo) {
        await delay(500);
        setPhase("complete");
        return;
      }
      const completed = await apiRequest<{ version: number }>(
        `/v1/projects/${project.id}/completion`,
        {
          method: "POST",
          idempotencyKey: newIdempotencyKey(),
          ifMatch: `"${project.version}"`,
          contentType: "application/vnd.lites.project-complete.v2+json",
          body: {
            request_id: newIdempotencyKey(),
            reflection: reflection.trim(),
            workspace_revision: project.workspaceRevision,
            expected_project_version: project.version,
          },
        },
      );
      setProject({ ...project, version: completed.version });
      setPhase("complete");
    } catch (cause) {
      setError(
        cause instanceof Error ? cause.message : "project_completion_failed",
      );
    }
  }
  async function buildExport() {
    if (!project) return;
    setPortfolioExport(null);
    setError("");
    try {
      if (demo) {
        await delay(650);
        setPortfolioExport({
          id: "demo-export",
          status: "ready",
          version: 3,
          content_hash: "demo",
          media_type: "application/zip",
          byte_size: 1024,
          failure_code: null,
        });
        return;
      }
      let current = await apiRequest<ExportState>("/v1/portfolio-exports", {
        method: "POST",
        idempotencyKey: newIdempotencyKey(),
        ifMatch: `"${project.version}"`,
        contentType: "application/vnd.lites.portfolio-export.v2+json",
        body: {
          request_id: newIdempotencyKey(),
          project_id: project.id,
          expected_project_version: project.version,
          expected_workspace_binding_version: project.workspaceVersion,
          workspace_revision: project.workspaceRevision,
          artifact_revision_ids: project.artifactRevisionIDs,
          format: "zip",
        },
      });
      setPortfolioExport(current);
      for (
        let attempt = 0;
        attempt < 180 && current.status !== "ready";
        attempt += 1
      ) {
        if (["failed", "expired"].includes(current.status))
          throw new Error(current.failure_code ?? `export_${current.status}`);
        await delay(1000);
        current = await apiRequest<ExportState>(
          `/v1/portfolio-exports/${current.id}`,
          { accept: "application/vnd.lites.portfolio-export.v2+json" },
        );
        setPortfolioExport(current);
      }
      if (current.status !== "ready") throw new Error("export_timeout");
    } catch (cause) {
      setError(
        cause instanceof Error ? cause.message : "portfolio_export_failed",
      );
    }
  }

  async function downloadExport() {
    if (!portfolioExport || portfolioExport.status !== "ready") return;
    setDownloading(true);
    setError("");
    try {
      if (demo) {
        await delay(150);
        return;
      }
      const file = await apiDownload(
        `/v1/portfolio-exports/${portfolioExport.id}/download`,
        { accept: portfolioExport.media_type ?? "application/octet-stream" },
      );
      const url = URL.createObjectURL(file.blob);
      const anchor = document.createElement("a");
      anchor.href = url;
      anchor.download = file.filename;
      anchor.rel = "noopener";
      anchor.click();
      window.setTimeout(() => URL.revokeObjectURL(url), 0);
    } catch (cause) {
      setError(
        cause instanceof Error ? cause.message : "portfolio_download_failed",
      );
    } finally {
      setDownloading(false);
    }
  }

  const steps = [
    {
      id: "workspace",
      label: zh ? "项目与 Workspace" : "Project & Workspace",
      done: !["brief", "plan", "provisioning"].includes(phase),
    },
    {
      id: "milestones",
      label: zh ? "两项必需里程碑" : "Two required milestones",
      done: !["brief", "plan", "provisioning"].includes(phase),
    },
    {
      id: "evaluation",
      label: zh ? "独立测试 / Rubric" : "Independent test / rubric",
      done: ["evidence", "reflection", "complete"].includes(phase),
    },
    {
      id: "artifact",
      label: zh ? "证据与 Artifact 修订" : "Evidence & Artifact revision",
      done: ["reflection", "complete"].includes(phase),
    },
    {
      id: "reflection",
      label: zh ? "反思、完成与导出" : "Reflection, completion & export",
      done: phase === "complete",
    },
  ];

  return (
    <div className="page-wrap create-studio">
      <header className="page-head">
        <div>
          <p className="eyebrow">{zh ? "创造" : "Create"}</p>
          <h1>
            {zh ? "把能力放进真实作品。" : "Put capability into real work."}
          </h1>
          <p>
            {zh
              ? "每个项目都经过计划、Workspace、独立验证、证据绑定、修订和反思；排队回执不会冒充完成。"
              : "Every project moves through planning, Workspace, independent validation, evidence binding, revision, and reflection; a queue receipt never masquerades as completion."}
          </p>
        </div>
      </header>
      {phase !== "brief" && (
        <ol
          className="create-progress"
          aria-label={zh ? "项目生命周期" : "Project lifecycle"}
        >
          {steps.map((step, index) => (
            <li className={step.done ? "done" : ""} key={step.id}>
              <span>
                {step.done ? <Check aria-hidden="true" /> : index + 1}
              </span>
              {step.label}
            </li>
          ))}
        </ol>
      )}

      {phase === "brief" && (
        <div className="create-layout">
          <section>
            <p className="section-label">
              01 · {zh ? "选择形式" : "Choose a form"}
            </p>
            <div className="format-grid">
              {formats.map(({ id, icon: Icon, title, description }) => (
                <button
                  type="button"
                  aria-pressed={selected === id}
                  className={
                    selected === id ? "format-card selected" : "format-card"
                  }
                  onClick={() => setSelected(id)}
                  key={id}
                >
                  <Icon aria-hidden="true" />
                  <strong>{title}</strong>
                  <span>{description}</span>
                </button>
              ))}
            </div>
          </section>
          <section className="card create-brief">
            <p className="section-label">
              02 · {zh ? "定义真实用途" : "Define a real use"}
            </p>
            <h2>
              {zh
                ? "你希望这个作品为谁解决什么？"
                : "What should this work solve, and for whom?"}
            </h2>
            <label className="field">
              <span>{zh ? "项目简述" : "Project brief"}</span>
              <textarea
                name="project-brief"
                autoComplete="off"
                required
                value={brief}
                onChange={(event) => setBrief(event.target.value)}
                placeholder={
                  zh
                    ? "例如：为平台工程团队设计一个可以安全重试的 Agent 运行服务…"
                    : "For example: design a safely retryable agent run service for a platform engineering team…"
                }
              />
            </label>
            <div className="coach-suggestion">
              <Sparkles aria-hidden="true" />
              <p>
                <strong>Coach suggestion</strong>
                <br />
                {zh
                  ? "当前证据缺口是副作用对账。让项目包含一次故障恢复演练，会比增加功能更有价值。"
                  : "Your current evidence gap is effect reconciliation. A failure-recovery exercise would add more value than another feature."}
              </p>
            </div>
            <button
              className="button primary"
              type="button"
              disabled={!brief.trim()}
              onClick={() => setPhase("plan")}
            >
              {zh ? "生成项目计划" : "Generate project plan"}
              <ArrowRight aria-hidden="true" />
            </button>
          </section>
        </div>
      )}

      {(phase === "plan" || phase === "provisioning") && (
        <section className="workspace-created card">
          <span className="mode-icon">
            <Layers3 />
          </span>
          <p className="eyebrow">
            {zh ? "计划待确认" : "Plan ready for confirmation"}
          </p>
          <h1>
            {zh
              ? "先定义成功，再开始执行。"
              : "Define success before execution starts."}
          </h1>
          <div className="workspace-columns">
            <div>
              <strong>Milestone 1 · Required</strong>
              <p>{milestones[0]}</p>
            </div>
            <div>
              <strong>Milestone 2 · Required</strong>
              <p>{milestones[1]}</p>
            </div>
            <div>
              <strong>
                {zh ? "独立证据评估" : "Independent evidence review"}
              </strong>
              <p>
                {zh
                  ? "验证精确 Workspace revision，并生成可追溯 Evidence。"
                  : "Validates the exact Workspace revision and produces traceable Evidence."}
              </p>
            </div>
          </div>
          {error && (
            <p className="error-note" role="alert">
              <AlertCircle />
              {error}
            </p>
          )}
          <button
            className="button primary"
            type="button"
            onClick={provision}
            disabled={phase === "provisioning"}
          >
            {phase === "provisioning" ? (
              <>
                <LoaderCircle className="spinner-icon" />
                {zh ? "正在原子创建…" : "Creating atomically…"}
              </>
            ) : (
              <>
                {zh
                  ? "创建项目、Workspace 与里程碑"
                  : "Create project, Workspace & milestones"}
                <ArrowRight />
              </>
            )}
          </button>
        </section>
      )}

      {phase === "workspace" && (
        <section className="create-workspace">
          <div className="card workspace-console">
            <div>
              <p className="section-label">
                Workspace · {selected} · {(project?.currentMilestone ?? 0) + 1}
                /2
              </p>
              <h2>
                {project?.milestones[project.currentMilestone]?.title ??
                  milestones[0]}
              </h2>
              <p>
                {zh
                  ? "内容先生成 SHA-256 revision，再与提交、评估和 Evidence 绑定。"
                  : "The content becomes a SHA-256 revision before submission, evaluation, and Evidence binding."}
              </p>
            </div>
            <label className="field">
              <span>
                {zh
                  ? "工作成果 / revision 内容"
                  : "Work output / revision content"}
              </span>
              <textarea
                name="workspace-output"
                data-testid="workspace-output"
                autoComplete="off"
                required
                value={deliverable}
                onChange={(event) => setDeliverable(event.target.value)}
                placeholder={
                  zh
                    ? "记录实现、设计或文章内容，以及验证它所需的可观察结果…"
                    : "Record the implementation, design, or writing content and the observable result needed to validate it…"
                }
              />
            </label>
            {error && (
              <p className="error-note" role="alert">
                <AlertCircle />
                {error}
              </p>
            )}
            <button
              className="button primary"
              type="button"
              disabled={!deliverable.trim()}
              onClick={requestEvaluation}
            >
              <FlaskConical />
              {zh
                ? "提交精确修订并请求独立评估"
                : "Submit exact revision for independent evaluation"}
            </button>
          </div>
          <aside className="card acceptance-panel">
            <p className="section-label">Acceptance</p>
            <ul>
              <li>
                <Check />
                {project?.milestones[project.currentMilestone]?.title ??
                  milestones[0]}
              </li>
              <li>
                <Check />
                {zh
                  ? "Evaluator 检查精确内容与验收条件"
                  : "The evaluator checks exact content and acceptance conditions"}
              </li>
              <li>
                <Check />
                {zh
                  ? "失败会进入 Rework，不会被计作通过"
                  : "Failure enters Rework and is never counted as a pass"}
              </li>
            </ul>
          </aside>
        </section>
      )}

      {phase === "evaluating" && (
        <section className="workspace-created card" aria-live="polite">
          <LoaderCircle className="spinner-icon create-loader" />
          <p className="eyebrow">{zh ? "评估已排队" : "Evaluation queued"}</p>
          <h1>
            {zh
              ? "等待独立结果，不会提前宣告通过。"
              : "Waiting for an independent result—no premature pass."}
          </h1>
          <p className="muted">
            {demo
              ? zh
                ? "Demo 评估器正在检查精确 revision。"
                : "The demo evaluator is checking the exact revision."
              : zh
                ? "服务器已确认请求。结果将由实时事件带回，并绑定测试运行、Workspace revision 与 Evidence。"
                : "The server confirmed the request. A realtime event will return the result bound to the test run, Workspace revision, and Evidence."}
          </p>
        </section>
      )}

      {phase === "evidence" && (
        <section className="create-workspace">
          <div className="card evidence-verdict">
            <ShieldCheck />
            <p className="eyebrow">
              {zh ? "两项独立评估通过" : "Both independent evaluations passed"}
            </p>
            <h2>
              {zh
                ? "Evidence 已绑定到精确修订"
                : "Evidence is bound to the exact revision"}
            </h2>
            <p>
              {lastEvidence?.content.evaluation?.summary ??
                (zh
                  ? "依据来自评估器观察到的验收结果；不会从一次结果推导精确能力百分比。"
                  : "The basis is evaluator-observed acceptance evidence; no exact capability percentage is inferred.")}
            </p>
            <dl>
              <div>
                <dt>Workspace revision</dt>
                <dd>
                  {project?.workspaceRevision ?? "demo:workspace:revision-1"}
                </dd>
              </div>
              <div>
                <dt>Evaluator run</dt>
                <dd>{lastEvidence?.source_id ?? "demo:test-run:verified"}</dd>
              </div>
              <div>
                <dt>Evidence</dt>
                <dd>
                  {project?.evidenceIDs.length ?? 2} · independently observed
                </dd>
              </div>
            </dl>
          </div>
          <div className="card form-grid">
            <p className="section-label">Artifact revision</p>
            <h2>
              {zh
                ? "封装作品、修订与证据"
                : "Package work, revisions, and evidence"}
            </h2>
            <label className="field">
              <span>
                {zh
                  ? "依据评审做了什么修订？"
                  : "What changed in response to the review?"}
              </span>
              <textarea
                name="artifact-revision"
                autoComplete="off"
                required
                value={revisionNote}
                onChange={(event) => setRevisionNote(event.target.value)}
                placeholder={
                  zh
                    ? "说明修改、证据依据和剩余不确定性…"
                    : "Describe the change, evidence basis, and remaining uncertainty…"
                }
              />
            </label>
            {error && (
              <p className="error-note" role="alert">
                <AlertCircle />
                {error}
              </p>
            )}
            <button
              className="button primary"
              type="button"
              disabled={!revisionNote.trim()}
              onClick={createRevision}
            >
              {zh
                ? "创建并扫描 Artifact 修订"
                : "Create and scan Artifact revision"}
              <ArrowRight />
            </button>
          </div>
        </section>
      )}

      {phase === "reflection" && (
        <section className="card reflection-card">
          <p className="eyebrow">{zh ? "最后一步" : "Final required step"}</p>
          <h1>
            {zh
              ? "把做法变成可迁移的判断。"
              : "Turn the work into a transferable judgment."}
          </h1>
          <label className="field">
            <span>{zh ? "项目反思" : "Project reflection"}</span>
            <textarea
              name="project-reflection"
              autoComplete="off"
              required
              value={reflection}
              onChange={(event) => setReflection(event.target.value)}
              placeholder={
                zh
                  ? "什么判断经受住了验证？什么边界仍不确定？下次你会如何更早验证？"
                  : "Which judgment held up? What remains uncertain? What would you validate earlier next time?"
              }
            />
          </label>
          {error && (
            <p className="error-note" role="alert">
              <AlertCircle />
              {error}
            </p>
          )}
          <button
            className="button primary"
            type="button"
            disabled={!reflection.trim()}
            onClick={completeProject}
          >
            <CheckCircle2 />
            {zh ? "完成项目" : "Complete project"}
          </button>
        </section>
      )}

      {phase === "complete" && (
        <section className="workspace-created card">
          <span className="review-seal">
            <CheckCircle2 />
          </span>
          <p className="eyebrow">{zh ? "项目已完成" : "Project complete"}</p>
          <span className="chip">{zh ? "Evidence · 已应用" : "Evidence · Applied"}</span>
          <h1>
            {zh
              ? "作品、证据与反思现在共享同一条版本链。"
              : "Work, evidence, and reflection now share one version chain."}
          </h1>
          <div className="workspace-columns">
            <div>
              <strong>2 / 2 Milestones</strong>
              <p>
                {zh ? "全部必需条件已验证" : "All required conditions verified"}
              </p>
            </div>
            <div>
              <strong>Artifact revision 1</strong>
              <p>
                {zh
                  ? "扫描通过并绑定 Evidence"
                  : "Scan passed and Evidence bound"}
              </p>
            </div>
            <div>
              <strong>
                {project?.evidenceIDs.length ?? 2} Evidence records
              </strong>
              <p>
                {zh
                  ? "基于独立评估，而非自我声明"
                  : "Based on independent evaluation, not self-attestation"}
              </p>
            </div>
          </div>
          {portfolioExport?.status === "ready" ? (
            <button
              className="button primary"
              type="button"
              onClick={downloadExport}
              disabled={downloading}
            >
              {downloading ? (
                <LoaderCircle className="spinner-icon" />
              ) : (
                <Download />
              )}
              {downloading
                ? zh
                  ? "正在校验下载…"
                  : "Verifying download…"
                : zh
                  ? "下载已验证项目包"
                  : "Download verified project package"}
            </button>
          ) : (
            <button
              className="button primary"
              type="button"
              onClick={buildExport}
              disabled={
                portfolioExport != null &&
                !["failed", "expired"].includes(portfolioExport.status)
              }
            >
              {portfolioExport ? (
                <LoaderCircle className="spinner-icon" />
              ) : (
                <Download />
              )}
              {portfolioExport
                ? zh
                  ? "正在构建并核验…"
                  : "Building and verifying…"
                : zh
                  ? "构建精确版本作品集导出"
                  : "Build exact-revision portfolio export"}
            </button>
          )}
          {portfolioExport?.status === "ready" && (
            <p role="status" className="pending-note">
              {zh
                ? `可信收据已验证 · ${portfolioExport.content_hash?.slice(0, 16)}… · ${portfolioExport.byte_size ?? 0} bytes`
                : `Trusted receipt verified · ${portfolioExport.content_hash?.slice(0, 16)}… · ${portfolioExport.byte_size ?? 0} bytes`}
            </p>
          )}
          {error && (
            <p className="error-note" role="alert">
              <AlertCircle />
              {error}
            </p>
          )}
        </section>
      )}
    </div>
  );
}
