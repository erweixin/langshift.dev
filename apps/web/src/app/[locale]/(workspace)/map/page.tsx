import { notFound } from "next/navigation";
import { ArrowDown, Check, CircleDashed, LockKeyhole, MoveRight, Sparkles } from "lucide-react";
import { LevelBadge } from "@/components/level-badge";
import { isLocale } from "@/i18n/config";
import { demoCapabilities, demoMission } from "@/lib/model";

export default async function MapPage({ params }: { params: Promise<{ locale: string }> }) {
  const { locale } = await params; if (!isLocale(locale)) notFound(); const zh = locale === "zh-CN";
  const stages = [
    { name: zh ? "建立运行时心智模型" : "Build the runtime mental model", state: "active", detail: zh ? "状态转换、持久化与恢复" : "Transitions, persistence, and recovery" },
    { name: zh ? "处理副作用与对账" : "Handle effects and reconciliation", state: "next", detail: zh ? "幂等、收据与补偿" : "Idempotency, receipts, and compensation" },
    { name: zh ? "隔离与调度" : "Isolation and scheduling", state: "locked", detail: zh ? "租户边界、容量与公平性" : "Tenant boundaries, capacity, and fairness" },
    { name: zh ? "生产运营" : "Production operations", state: "locked", detail: zh ? "可观测性、SLO 与事件响应" : "Observability, SLOs, and incidents" },
  ];
  return <div className="page-wrap"><header className="page-head"><div><p className="eyebrow">{zh ? "迁移地图" : "Migration map"}</p><h1>{demoMission.path}</h1><p>{zh ? "地图是随证据更新的假设，不是固定课表。你可以修正任何判断。" : "The map is a hypothesis that updates with evidence, not a fixed syllabus. You can correct any judgment."}</p></div><button className="button"><Sparkles />{zh ? "与 Coach 校准" : "Calibrate with Coach"}</button></header>
    <section className="bridge-banner"><div><span>{zh ? "已有能力" : "What you have"}</span><strong>UI state & async flows</strong></div><MoveRight /><div><span>{zh ? "正在迁移" : "What is moving"}</span><strong>Durable agent lifecycle</strong></div></section>
    <div className="map-layout"><section className="stage-path" aria-label={zh ? "四阶段路线" : "Four-stage route"}>{stages.map((stage, index) => <div key={stage.name} className={`stage-node ${stage.state}`}><span className="stage-index">{stage.state === "active" ? <CircleDashed /> : stage.state === "locked" ? <LockKeyhole /> : <Check />}</span><div><p>Stage {index + 1}</p><h2>{stage.name}</h2><span>{stage.detail}</span>{stage.state === "active" && <div className="progress-track"><span style={{ width: "42%" }} /></div>}</div>{index < stages.length - 1 && <ArrowDown className="stage-arrow" />}</div>)}</section>
      <aside className="capability-panel card"><p className="section-label">{zh ? "能力依据" : "Capability basis"}</p><h2>{zh ? "当前判断" : "Current judgments"}</h2>{demoCapabilities.map((item) => <div className="map-capability" key={item.id}><div><strong>{item.name}</strong><p>{item.basis}</p></div><LevelBadge locale={locale} level={item.level} /></div>)}<button className="button ghost">{zh ? "提出修正" : "Propose a correction"}</button></aside></div>
  </div>;
}
