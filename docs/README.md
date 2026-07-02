# Cloud Agent 架构文档入口

这组文档描述 Lites Cloud Agent 的端到端架构。目标不是追求“大厂全家桶”，而是先把一个能落地、能恢复、能审计、能逐步扩展的 v1 讲清楚。

产品层面，Lites 面向 AI 时代的个人成长与职业跃迁：以职业转型为核心入口，但不局限于转行；底层方法论是帮助用户把已有知识和经验迁移、重组、扩展成目标能力。产品设计见 [product-growth-os.md](./product-growth-os.md)，UI/UE、状态草图和学习验证闭环见 [product-ux-blueprint.md](./product-ux-blueprint.md)。

Cloud Agent 和普通 Web 服务最大的区别是：一次用户请求会跨越 API、队列、模型、工具、sandbox、审批、实时通知和后台恢复。任何一步都可能超时、重复、崩溃或被取消。所以文档的主线是：

```text
用户意图
→ 持久化事实 Event
→ 派发下一步 Command
→ Worker 执行 LLM / Tool / Runtime
→ 再写回 Event
→ 状态机决定继续、等待、失败或完成
```

## 先读哪几篇

如果你第一次接触 Cloud Agent，建议按下面顺序读：

| 顺序 | 文档 | 先解决的问题 |
| ---: | --- | --- |
| 0 | [product-growth-os.md](./product-growth-os.md) | 产品面向谁、解决什么成长问题、Mission 和知识迁移方法论是什么 |
| 0.5 | [product-ux-blueprint.md](./product-ux-blueprint.md) | 用户状态、UI/UE 草图、Today 主动作、Evidence、Lite Challenge 和学习验证闭环怎么设计 |
| 0.7 | [product-to-platform.md](./product-to-platform.md) | 把产品语言翻译成工程语言：Mission、Task、Review 分别落到哪些 run / event / tool |
| 1 | [architecture.md](./architecture.md) | 为什么要用 EventStore、状态机和 Worker 循环，而不是一次 HTTP 请求跑到底 |
| 2 | [end-to-end-flow.md](./end-to-end-flow.md) | 一个任务从用户提交到完成，中间经过哪些组件、数据怎么流动、失败怎么恢复 |
| 2.5 | [v0-product-slice.md](./v0-product-slice.md) | 第一个真正要交付的版本：让一个真实用户连续跑通 14 天，而不是先把平台做满 |
| 3 | [mvp-scope.md](./mvp-scope.md) | 平台 v1 的完整边界：v0 被验证后，再补哪些平台能力 |
| 4 | [state-machines.md](./state-machines.md) | Run、ToolCall、Command 各自有哪些合法状态 |
| 5 | [concurrency-and-durability.md](./concurrency-and-durability.md) | 如何用事务、CAS、outbox/inbox 和 effect ledger 抵抗重复投递和崩溃 |
| 6 | [execution-model.md](./execution-model.md) | Worker 如何领取任务、调用 LLM/工具、处理副作用和并行 join |
| 7 | [agent-safety-and-guardrails.md](./agent-safety-and-guardrails.md) | prompt injection、工具越权、审批和输出检查如何落到工程边界 |
| 8 | [operations.md](./operations.md) | 系统怎么被观测、怎么扫尾、怎么做故障测试 |
| 9 | [capacity-and-scaling.md](./capacity-and-scaling.md) 与 [roadmap.md](./roadmap.md) | 怎么部署、怎么压测、什么时候替换基础设施 |

其他专题文档按需要阅读：

- 内容生成管线（怎么低成本、稳定地产出课程和练习）： [content-pipeline.md](./content-pipeline.md)
- 学习者画像（系统到底“记住了你什么”，以及用户如何纠正）： [learner-profile.md](./learner-profile.md)
- 练习运行时（课程里的代码练习怎么安全、快速地跑起来）： [exercise-runtime.md](./exercise-runtime.md)
- 单位经济（一个活跃用户每天大概花多少钱，订阅价能不能覆盖）： [unit-economics.md](./unit-economics.md)
- 工具： [tool-system.md](./tool-system.md)
- 模型接入： [llm-provider.md](./llm-provider.md)
- Memory / RAG： [memory.md](./memory.md)
- 多 Agent 编排： [orchestration-patterns.md](./orchestration-patterns.md)
- 实时通道： [realtime.md](./realtime.md)
- Runtime / Sandbox： [runtime-and-sandbox.md](./runtime-and-sandbox.md)
- 多租户与安全： [multi-tenancy-and-security.md](./multi-tenancy-and-security.md)

## 文档怎么分工

| 问题 | 入口 | 读完应该知道什么 |
| --- | --- | --- |
| 产品是什么 | [product-growth-os.md](./product-growth-os.md)、[product-ux-blueprint.md](./product-ux-blueprint.md) | 为什么以职业跃迁为入口，如何用知识迁移、行动、反馈、Lite Challenge 和 Evidence 构建 Personal Growth OS |
| 为什么要这样设计 | [architecture.md](./architecture.md) | Agent 任务为什么需要可恢复工作流、事件事实源、状态机和短事务 Worker |
| 系统是什么 | [end-to-end-flow.md](./end-to-end-flow.md) | 每个组件的职责边界，以及业务数据、命令、artifact、memory、secret 和 telemetry 怎么流动 |
| 怎么运行 | [execution-model.md](./execution-model.md) | API 如何受理，Worker 如何执行，LLM 和工具如何交接 |
| 怎么失败 | [state-machines.md](./state-machines.md)、[operations.md](./operations.md) | 重复请求、Worker 崩溃、工具未知结果、取消竞态、实时断线和恢复代次怎么处理 |
| 怎么保证安全 | [agent-safety-and-guardrails.md](./agent-safety-and-guardrails.md)、[runtime-and-sandbox.md](./runtime-and-sandbox.md)、[multi-tenancy-and-security.md](./multi-tenancy-and-security.md) | 哪些边界靠代码和策略保证，哪些不能只靠 prompt |
| 怎么部署 | [capacity-and-scaling.md](./capacity-and-scaling.md) | Lite v1 用哪些进程和基础设施，什么时候替换队列、runtime 或存储 |
| 怎么演进 | [mvp-scope.md](./mvp-scope.md)、[roadmap.md](./roadmap.md) | v1 交付边界、暂不做的内容、阶段化演进顺序 |

## 核心取舍

| 取舍 | v1 选择 | 原因 |
| --- | --- | --- |
| 形态 | 模块化单体 + 少量 Worker 进程 | 保留清晰边界，减少分布式系统复杂度 |
| 持久化 | PostgreSQL 作为 EventStore、job queue 和投影存储 | 事务语义清楚，便于先把正确性做实 |
| 队列语义 | 至少一次投递 + inbox 去重 | 不追求传输层 exactly-once，把幂等放在业务边界 |
| 状态推进 | Run / ToolCall / Command 状态机 + CAS | 防止旧 Worker、重复 command 和取消竞态改写终态 |
| 外部副作用 | 能幂等就用 `effect_key`，不能幂等就对账 | 避免工具超时后盲目重做 |
| 安全 | 平台授权和 guardrail 决策独立于模型输出 | prompt 不是安全边界 |
| 运维 | 先做少量高信号指标、trace、审计和故障注入 | 观测系统也要简单可维护 |

## 参考基线

这些文档吸收了社区里的稳定实践，但不会把任何框架原样搬进 v1：

- [OpenAI Agents SDK Guardrails](https://openai.github.io/openai-agents-python/guardrails/)：输入、输出和工具调用都需要检查；危险工具不能只靠 Agent 自觉。
- [OWASP Top 10 for LLM Applications / GenAI Security Project](https://owasp.org/www-project-top-10-for-large-language-model-applications/)：LLM 应用需要把 prompt injection、敏感信息、工具调用和供应链风险当成工程问题处理。
- [OpenTelemetry Signals](https://opentelemetry.io/docs/concepts/signals/)：日志、指标和 trace 是互补信号；Cloud Agent 需要把 run、command、attempt、tool call 和 LLM attempt 关联起来。
- [Google SRE: Monitoring Distributed Systems](https://sre.google/sre-book/monitoring-distributed-systems/)：优先关注延迟、流量、错误和饱和度；告警要可行动、低噪声。
- [Temporal Durable Execution](https://docs.temporal.io/temporal)：长任务需要可恢复执行和明确历史；本文采用相同思想，但 v1 不引入 Temporal 作为依赖。
- [NIST AI Risk Management Framework](https://www.nist.gov/itl/ai-risk-management-framework)：AI 风险治理需要可记录、可测量、可管理；本文把这些要求落到事件、审计、权限和运维流程。

## 阅读时的心智模型

Cloud Agent 不是“模型加几个工具”。它更像一个带 LLM 决策能力的异步工作流系统：

- LLM 负责提出下一步计划，不负责最终授权。
- EventStore 负责保存事实，不负责执行外部副作用。
- Worker 负责执行一小步，不保存长期状态。
- Tool 和 Runtime 负责接触外部世界，所以必须有权限、幂等、隔离和审计。
- Realtime 只负责让用户更快看到变化，不是可靠存储。
- Sweeper 和 Repair API 负责让异常状态收敛，而不是让运维直接改库。
