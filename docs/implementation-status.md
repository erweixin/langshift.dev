# 实施看板（v0）

> 这篇是 doc-to-code 差距的可见化：已拍板的决策、准备清单状态、M0/M1/M2 的可勾选任务。每次合并有实质进展的 PR 时更新本文件。范围之外的想法记到本文件末尾的"不做清单"，防止 scope creep。全程阶段计划（阶段 0–7，含各阶段实施细则与验收标准）见 [implementation-plan.md](./implementation-plan.md)：M0 ≈ 阶段 0–1，M1 ≈ 阶段 2–4，M2 ≈ 阶段 5。

## 已拍板决策

| 决策 | 结论 | 理由 / 备注 |
| --- | --- | --- |
| 数据库 | **直接 PostgreSQL，不做 SQLite 过渡**（用户拍板，推翻此前 SQLite 决策） | goose + pgx；自部署形态 = 单二进制 + docker-compose 附带一个 Postgres 容器 |
| Migration 工具 | goose（SQL 迁移，Postgres 方言） | 迁移文件即 schema 文档 |
| CI | GitHub Actions：build + test 两步 | 动工第一天配好 |
| 开源时点 | M1 跑通后再 public | 开源是分发手段，空仓库消耗第一印象 |
| 登录 | 邮箱验证码（无密码）+ 邀请码控内测；自部署版默认单用户免登录 | 引入发信服务依赖（见准备清单）；比 OAuth 少一个回调依赖 |
| 账户删除 | v0 硬删除（append-only 的唯一特权例外，按 user_id 物理删除全部数据）；v1 演进为 per-user 加密 + 销毁密钥 | 规则见 event-catalog；共享的 content_artifacts 因隐私 lint 保证不含用户内容，删除时不清 |
| Run CAS | Run 级乐观锁 M0 起启用（worker 与 sweeper 天然并发）；ToolCall 级随 v1 工具域 | 推翻此前"v0 只写不校验"的取舍 |
| 后端选型 | 标准库 `net/http`（1.22+ ServeMux）· PostgreSQL（`jackc/pgx`）· 手写 SQL repository（无 ORM）· 官方 `anthropic-sdk-go`（vercel-ai 因仅支持 TS 运行时而否决，保留 Go）· `log/slog` · `oklog/ulid` · `yaml.v3` | 单二进制、少依赖 |
| 前端选型 | TypeScript（骨架从 .jsx 切换）· TanStack Query + 自写 SSE hook（`last_seen_seq` 补拉）· react-router · streamdown（流式 Markdown 渲染、内置代码高亮，替代 react-markdown + Shiki）· CodeMirror 6 · **自建设计系统**（不复用原型视觉，见准备清单） | 类型从 `schemas/` 生成（json-schema-to-typescript），契约贯通前后端 |
| 模型分档 | 见 `config/llm.example.yaml`，代码只认档位 | 单价参数化，调价改配置 |
| 课程生成输出 | 必须用 structured outputs（JSON schema 约束） | 消除格式类校验失败（见风险登记 R1） |
| D1 课程兜底 | 第一课人工写定稿（原型内容已具备雏形） | 管线冷启动失败不阻塞用户 |

## 准备清单状态

- [x] 可行性评估（spike 以分析代替，结论见风险登记）
- [x] 14 天任务序列草案 → [curriculum-14d.md](./curriculum-14d.md)（待创始人修订冻结，含 3 个待确认问题）
- [x] Event catalog → [event-catalog.md](./event-catalog.md)
- [x] ContentArtifact / ExerciseResult / ReviewOutput / ReviewCompleted schema → `schemas/`
- [x] 模型分档配置 → `config/llm.example.yaml`
- [x] 原型声明为交互 source of truth → [product-ux-blueprint.md](./product-ux-blueprint.md) 顶部
- [ ] 选发信服务（Resend / SES / 任意 SMTP，内测量级免费档即可）+ 发信域名（创始人操作，M1 前；本地开发用控制台打印验证码绕过）
- [ ] 拍板 M2 托管部署目标：单二进制 + Postgres（托管 Postgres 或同机容器），小 VPS / Fly.io 即可。M2 前定
- [ ] 设计系统初版：色板 / 字号 / 间距 token + 基础组件清单（M1 前端动工前；原型只约束交互流程与信息架构，不约束视觉）
- [ ] Provider 账号：dev/prod key 分开 + 用量告警（创始人操作）
- [ ] 内测用户物色：3~5 名正在转型的前端（创始人操作，现在就开始）
- [ ] 仓库台面：goose 接入、GitHub Actions、`.env.example`（动工第一天）

## M0 · 行走骨架（目标：2 周）

一条真实链路端到端：提交 → Review run → 事件 → 投影 → SSE 推给前端。

- [ ] events 表 + event_cursors（seq 游标）+ idempotency_keys（作用域化请求重放）+ envelope（对齐 event-catalog）+ 投影重建命令
- [ ] jobs 表：入队 / 领取 / ack / 重试 / command_id 去重 / subject_user_id 删除定位 / JobFence(job_id, lease_token) / 成功路径 append+done 同事务
- [ ] Run 状态机（6 态 + 转换表 + `run_version` 乐观锁 + 超时 sweeper goroutine）
- [ ] LLM client：分档配置加载、pre-call pending 记账 → 返回补全 → 崩溃收敛 unknown、structured outputs
- [ ] Review run 真实跑通（读 EvidenceSubmitted → 写 ReviewCompleted）
- [ ] SSE：投影变化推送 + 断线重连补拉
- [ ] 验证：`kill -9` 后重启，进行中 run 恢复或干净失败；ledger 无重复落账、无静默丢账（unknown 行被收敛）

## M1 · loop 闭环（目标：M0 后 3~4 周）

10 个产品动作全部真实化（对照 [product-to-platform.md](./product-to-platform.md) 动作表）：

- [ ] 1 冷启动诊断 + 路线生成（含确认屏数据）
- [ ] 2 每日任务生成（消费 next_task_seed）
- [ ] 3 内容管线：LessonPlan → Draft → 校验（参考解真跑测试）→ 定稿 → content_artifacts
- [ ] 4 选中重写（直连流式 + delta 持久化 + PreferenceRecorded）
- [ ] 5 Drawer 对话（stub 先行 + 直连流式 + 画像摘要前缀 + 最终事件兜底）
- [ ] 6 练习运行时 v0：Web Worker + 看门狗 + ExerciseResult 落账
- [ ] 7 提交页自动带入 + Review 等待态 UI
- [ ] 8 作品化（resume bullet 一种形态即可）
- [ ] 9 断更回归轻任务
- [ ] 10 画像投影 + 分面摘要刷新 + 完成态记忆卡（真数据）
- [ ] 前端：按原型实现 v0 屏清单（见 product-ux-blueprint 顶部）
- [ ] 验证：创始人本人连续跑通 3 天

## M2 · 14 天实验（目标：M1 后即启动）

- [ ] 数据导出（JSON + Markdown）与账户删除（含派生物清除）
- [ ] cost 看板四曲线（unit-economics.md）
- [ ] streak 宽容规则 + 断更回归全流程
- [ ] 3~5 名外部用户跑 14 天；观测口径：D7 / D14 留存、loop 漏斗（开始任务 → 跑通测试 → 提交 → 看完 Review → 次日回访）
- [ ] v0 DoD 全绿（见 [v0-product-slice.md](./v0-product-slice.md)）
- [ ] 复盘：留存与成本实测 → 修订 unit-economics 假设 → 决定开源 public 时点

## 风险登记

| # | 风险 | 评估（代替 spike 的分析结论） | 缓解 |
| --- | --- | --- | --- |
| R1 | 课程生成一次通过率不足 | 预期首过率 70–85%。主要失败模式：① 测试与起始代码不构成"必挂"关系；② 参考解与测试对边界情况理解不一致；③ 输出结构不合规；④ 判断点测试与文本漂移 | ③ 用 structured outputs 基本消除；①②④ 靠管线的"校验失败原因回灌重生成"，预期一轮重试后 >95%；剩余进人审。管线设计不需要改，但 M1 首周要用真实 3 课实测校准这些数字 |
| R2 | token 估算偏差导致成本结论失真 | 6k/5k（课程）与 4k/1.5k（Review）为同量级估计，实测可能在 ±50% 区间；毛利结论对此不敏感（80% 毛利容得下 2 倍偏差） | M0 起每调用落账，M1 首周用真实数据重算 unit-economics 表 |
| R3 | Review 输出结构不稳 | 用 structured outputs 约束后基本不是风险 | 已落地：`schemas/review-output.schema.json`（模型输出）+ `schemas/review-completed.schema.json`（事件 payload，`evidence_id` 由调用方注入） |
| R4 | 14 天内容质量不均 | 管线自动校验管"能跑"，管不了"讲得好" | golden set 人审 + D1 人工兜底 + 重写率信号定位坏小节 |
| R5 | 留存不成立（最大风险，非技术） | 无法预判，只能实验 | M2 就是为它设计的；漏斗口径先定好 |

## 不做清单（v0 期间不讨论）

多租户 RLS、审批流、secret broker、服务端语言沙箱、第二条路线、移动端、团队/B2B 功能、工具市场、多 Agent 编排、支付系统（M2 验证前不收钱）。

技术栈上明确不引入：ORM（GORM/ent）、Redux/Zustand、Tailwind、Monaco、gRPC、微服务框架、Node 后端服务。
