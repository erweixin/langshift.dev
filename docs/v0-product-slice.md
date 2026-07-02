# v0 产品切片：一个用户跑通 14 天

> 这篇定义第一个真正要交付的版本。平台 v1 追求“平台语义正确”；v0 先追求一件更朴素的事：一个真实用户能连续 14 天完成 daily loop，并且愿意回来。产品动作和平台原语的对应见 [product-to-platform.md](./product-to-platform.md)。

这里的 **daily loop** 指一天里的完整学习闭环：拿到今日任务 → 看课程 → 跑练习 → 提交产出 → 收到 Review → 生成明日任务。v0 不是把平台做满，而是证明这个闭环真的有人愿意连续用。

## 问题、决策与风险

**问题**：平台 v1 的“必须做”清单很长：guardrails、审批流、secret broker、RLS、Sweeper、Repair API、全链路可观测。如果当前团队先把这些全部做完，几个月内都验证不到用户价值。这个产品最大的未知不是平台能不能设计正确，而是**第 14 天用户还来不来**。

**决策**：v0 只做支撑单用户、之后放宽到不超过 50 个内测用户的最小系统。必须保留以后很难补的东西：event 事实源、run 状态机、幂等键。多租户、安全纵深、规模化能力先推迟。

**忽略后果**：要么继续文档很完整、产品没人用；要么反过来为了快乱写，用户数据散在临时表里，没有事件语义，等 v1 时全部重来。

## v0 Definition of Done

一个真实用户先做到，随后用 3-5 名外部内测者验证：

- [ ] 完成 onboarding（三种入口至少一种真实可用）→ 得到 Mission + Roadmap + 首个任务。
- [ ] 连续 14 天：任务 → 课程页（真实生成的内容 + 可运行练习）→ 提交（课内产出自动带入）→ Review（真实 LLM 评审）→ 完成态（记忆卡有真实沉淀）→ 明日任务。
- [ ] 中断 1 次后回归，得到轻重启任务，streak 不清零。
- [ ] 至少 1 次「选中重写」且偏好在后续课程中生效；至少 3 次 Drawer 对话且带上下文。
- [ ] 至少 1 份 Evidence 完成作品化（简历 bullet）。
- [ ] 用户导出全部数据（JSON），删除账户后数据与派生物真实消失。
- [ ] 进程崩溃 / 重启后，进行中的 run 恢复或干净失败；LLM 花费账本诚实——无重复落账、无静默丢账，结局未知的调用被显式标记（provider 无幂等键，不承诺绝不重复调用）。

## v0 架构

```text
单二进制 Go 服务（API + worker goroutines + SSE）
├── PostgreSQL（托管与自部署统一；自部署以 docker-compose 附带）
│   ├── events（append-only，单表）
│   ├── jobs（at-least-once + fence + 原子 append/done，SKIP LOCKED / 单进程队列）
│   ├── 操作事实：idempotency_keys / llm_ledger / content_cache（artifact 本体，replay 不清）
│   └── 投影：missions / tasks / profile / evidence …（可由 events 重建）
├── LLM：Anthropic API ×1（强/中/小三档模型别名，见 unit-economics.md）
├── 练习运行时：浏览器 JS（Web Worker），无服务端沙箱
└── 前端：React SPA（现有 frontend/ 骨架）+ SSE
```

先不要 Redis、Docker 沙箱、独立 worker 进程、对象存储。artifact 先进 DB 或本地盘。v0 的目标是把 loop 跑通，不是把未来所有基础设施一次买齐。

## 语义取舍：保留 / 简化 / 推迟

| 平台 v1 项 | v0 处置 | 理由 |
| --- | --- | --- |
| EventStore 事实源 | **保留**（单表 append-only + 投影可重建） | 以后再补会最痛 |
| Run 状态机 | **保留**（6 态精简版：accepted / queued / executing + 3 终态，状态名对齐 state-machines.md） | 产品第一课就在教这个，自己也要用起来 |
| 提交幂等（idempotency key） | **保留**（写端点统一 `Idempotency-Key` 约定，按 user + endpoint scope 映射到服务端 command_id） | 防止重复提交；LLM 花费由 ledger 的 pre-call 规则独立诚实记账（无重复落账） |
| inbox 去重 | **简化**：jobs 表带唯一服务端 command_id；worker 成功路径 append 事件与 job done 同事务 | 单进程下够用，语义不变 |
| 双级 CAS（run/tool_call version） | **Run 级保留（M0 起校验）**：状态转换走 `run_version` 乐观锁——worker 与 sweeper 天然并发；ToolCall 级随 v1 工具域引入 | Run 级 CAS 只是一个带 WHERE 的 UPDATE，成本极低，不值得推迟 |
| outbox | **推迟**：同库同事务，发布即查询 | 没有跨系统投递 |
| Guardrails / 审批流 | **推迟**：v0 无危险工具（Review 只读输入，无外部副作用） | 威胁面不存在 |
| Secret broker / 沙箱纵深 | **推迟**：无服务端代码执行 | 见 exercise-runtime.md 的 v1 触发条件 |
| 多租户 RLS | **推迟**：user_id 外键 + 应用层校验 | ≤50 内测用户 |
| Sweeper | **简化**：一个定时 goroutine 收敛超时 run | 保留"无新事件也能收敛"的原则 |
| Repair API | **推迟**：内测期允许手工 SQL + 记录 | 换取速度，v1 前补 |
| 可观测性 | **简化**：结构化日志 + 每 LLM 调用的 token/cost 记录 | cost 记录不可省（见 unit-economics.md） |
| `store_epoch` / 备份恢复演练 | **推迟** | 内测数据可承受重建 |

## 开源 / 自部署切分

v0 的单二进制**就是自部署版**（AGPL 承诺的兑现），这不是额外工作而是同一产物：

| | 自部署版（开源） | 托管版（商业） |
| --- | --- | --- |
| 运行 | 单二进制 + PostgreSQL（docker-compose 一键起）+ 自带 API key（BYO key） | 多用户 + 平台 key + 托管 Postgres |
| 功能 | 完整 daily loop、画像、课程、练习 | 同左 + 跨设备记忆同步、服务端语言沙箱（v1）、作品化高级模板、免配置 |
| 成本 | 用户自付 token | 订阅覆盖，见 [unit-economics.md](./unit-economics.md) |

切分原则：**daily loop 的机制开源，记忆托管和增值能力收费**。代码层面用同一仓库 + build tag / 配置开关，不维护两个分支。

## 里程碑

| 里程碑 | 内容 | 验证 |
| --- | --- | --- |
| M0 · 行走骨架 | 单二进制起服务：一条真实链路（提交 → Review run → 事件 → 投影 → SSE 推给前端） | 崩溃重启后 run 恢复 |
| M1 · loop 闭环 | 10 个产品动作全部真实化（对照 product-to-platform.md 的表） | 创始人本人跑通 3 天 |
| M2 · 14 天实验 | 3~5 名外部用户 + 数据导出/删除 + cost 面板 | v0 DoD 全绿；拿到留存与成本的真实数据 |

## 升级到平台 v1 的信号

| 信号 | 补什么 |
| --- | --- |
| 内测用户 >50 或出现付费用户 | RLS、真实多租户、Repair API |
| 需要服务端语言沙箱（Go 练习） | runtime-and-sandbox 的隔离基线 + exercise runtime v1 |
| 出现并行 worker 需求（Review 排队变慢） | 双级 CAS 启用、独立 worker 进程、outbox |
| 任何真实外部副作用工具接入 | guardrails + effect ledger + 审批 |
| 数据不可承受丢失 | 备份恢复演练 + `store_epoch` |

v0 的核心标准：**慢一点可以，界面糙一点也可以；但事件语义不能欠债，LLM 账本不能重复落账、结局未知的调用必须显式标记，用户数据必须随时可导出、可删除。**
