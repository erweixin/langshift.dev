import { notFound } from "next/navigation";
import { ArrowRight, CheckCircle2, Circle, FolderKanban, MoreHorizontal, Plus } from "lucide-react";
import { PortfolioExport } from "@/components/portfolio-export";
import { isLocale } from "@/i18n/config";
import { demoMission, demoProjects } from "@/lib/model";

export default async function GoalsPage({ params }: { params: Promise<{ locale: string }> }) {
  const { locale } = await params; if (!isLocale(locale)) notFound(); const zh = locale === "zh-CN";
  return <div className="page-wrap"><header className="page-head"><div><p className="eyebrow">{zh ? "成长目标" : "Goals & projects"}</p><h1>{demoMission.name}</h1><p>{demoMission.path} · {demoMission.stage}</p></div><button className="button primary"><Plus />{zh ? "新建 Mission" : "New Mission"}</button></header>
    <section className="mission-overview card"><div><p className="section-label">{zh ? "当前 Mission" : "Current Mission"}</p><h2>{zh ? "用生产级项目验证 Cloud Agent 能力" : "Prove cloud-agent capability through production-grade projects"}</h2><p>{zh ? "成功不是完成课程，而是产出可运行、可评审、可追溯的工作。" : "Success is not finishing content. It is producing runnable, reviewable, traceable work."}</p></div><div className="mission-stage"><span>Stage 1 / 4</span><div className="progress-track"><span style={{ width: "25%" }} /></div><small>{zh ? "Focus 每周自动校准，也可手动修正" : "Focus recalibrates weekly and can be corrected manually"}</small></div></section>
    <div className="section-title"><div><p className="section-label">Workspace</p><h2>{zh ? "项目与里程碑" : "Projects and milestones"}</h2></div><button className="button"><Plus />{zh ? "创建项目" : "Create project"}</button></div>
    <section className="project-grid">{demoProjects.map((project) => <article className="project-card card" key={project.id}><header><span className="project-kind"><FolderKanban />{project.kind}</span><button className="icon-button" aria-label={`${zh ? "项目菜单" : "Project menu"} ${project.title}`}><MoreHorizontal /></button></header><h3>{project.title}</h3><div className="milestone-list">{project.milestones.map((milestone) => <div key={milestone.name}>{milestone.status === "verified" ? <CheckCircle2 /> : <Circle />}<span>{milestone.name}</span><small>{milestone.status.replace("_", " ")}</small></div>)}</div><button className="inline-link">{zh ? "打开 Workspace" : "Open Workspace"}<ArrowRight /></button></article>)}</section>
    <PortfolioExport locale={locale} />
  </div>;
}
