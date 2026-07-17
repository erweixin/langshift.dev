import Link from "next/link";
import { notFound } from "next/navigation";
import { ArrowRight, CalendarDays, CheckCircle2, Clock3, Layers3, Sparkles } from "lucide-react";
import { isLocale } from "@/i18n/config";
import { demoMission, demoTask } from "@/lib/model";

export default async function TodayPage({ params }: { params: Promise<{ locale: string }> }) {
  const { locale } = await params;
  if (!isLocale(locale)) notFound();
  const zh = locale === "zh-CN";
  const today = new Intl.DateTimeFormat(locale, { weekday: "short", month: "short", day: "numeric", timeZone: "Asia/Shanghai" }).format(new Date());
  return <div className="page-wrap today-page"><header className="page-head"><div><p className="eyebrow">{zh ? "今日一步" : "One step today"}</p><h1>{zh ? "早上好。" : "Good morning."}</h1><p>{zh ? "今天只需要完成一项能验证判断的工作。" : "Today needs one piece of work that can test a judgment."}</p></div><div className="date-pill"><CalendarDays /> {today}</div></header>
    <section className="focus-card"><div className="focus-meta"><span><Sparkles /> {zh ? "当前 Focus" : "Current Focus"}</span><span>{demoMission.stage}</span></div><div className="focus-content"><div><p className="section-label">{zh ? "迁移桥任务" : "Bridge task"}</p><h2>{demoTask.title}</h2><p>{demoTask.why}</p><div className="task-facts"><span><Clock3 /> {demoTask.minutes} {zh ? "分钟" : "min"}</span><span><Layers3 /> {zh ? "代码或文字" : "Code or writing"}</span></div></div><Link className="button primary" href={`/${locale}/task`}>{zh ? "开始任务" : "Start task"}<ArrowRight /></Link></div><footer><strong>{zh ? "完成标准" : "Definition of done"}</strong><span>{demoTask.judgment}</span></footer></section>
    <div className="grid-2 today-lower"><section className="card"><div className="stat-line"><p className="section-label">{zh ? "路线位置" : "Route position"}</p><span className="chip">Stage 1 of 4</span></div><h2>{zh ? "建立运行时心智模型" : "Build the runtime mental model"}</h2><div className="progress-track" role="progressbar" aria-label="Stage 1 of 4" aria-valuemin={0} aria-valuemax={4} aria-valuenow={1}><span style={{ width: "25%" }} /></div><p className="muted">{zh ? "下一个里程碑：恢复与副作用对账" : "Next milestone: recovery and effect reconciliation"}</p><Link className="inline-link" href={`/${locale}/map`}>{zh ? "查看迁移地图" : "Open migration map"}<ArrowRight /></Link></section>
      <section className="card soft"><p className="section-label">{zh ? "最近证据" : "Latest evidence"}</p><div className="evidence-mini"><CheckCircle2 /><div><h3>{zh ? "异步编程 · 已展示" : "Asynchronous programming · Demonstrated"}</h3><p>{zh ? "来自流式交互项目；等待评审验证。" : "From your streamed interaction project; awaiting reviewer verification."}</p></div></div><Link className="inline-link" href={`/${locale}/evidence`}>{zh ? "查看依据" : "Inspect basis"}<ArrowRight /></Link></section></div>
    <section className="reflection-strip"><div><p className="section-label">{zh ? "昨日回顾" : "Yesterday’s reflection"}</p><p>{zh ? "“我原来把重试当成网络层细节，现在开始把它看作业务状态的一部分。”" : "“I used to treat retry as a network detail. Now I’m starting to see it as part of business state.”"}</p></div><Link className="button ghost" href={`/${locale}/evidence`}>{zh ? "继续记录" : "Continue reflection"}</Link></section>
  </div>;
}
