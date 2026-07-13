# Lites 架构文档

这个目录保留 Lites Cloud Agent 的架构内核、一个独立的 UX 原型，以及从产品理念、原型和架构重新推导出的完整 GA 实施计划。前端代码、后端代码、生成出来的 schema、本地脚本和旧实现记录已经移除，目的是让项目从一套更清楚的 Cloud Agent 架构重新开始。

读这组文档时可以先抓住五件事：

- Agent 任务不是一次 HTTP 请求，而是一串可恢复的“事件 + 命令”。
- EventStore、durable queue/stream、runtime、memory、realtime、evaluation 和 observability 都按生产基线设计；组件可以演进，但语义不能变。
- LLM 只提出计划，真正的权限、工具执行、安全审批和状态推进都由平台负责。
- 多 Agent、长期记忆、流式输出和自动工具执行都不是默认魔法，而是建立在状态机、权限、隔离和可观测之上的可选能力。
- [product-implementation-plan.md](./product-implementation-plan.md) 定义完整产品范围和 GA 验收边界，但不表示仓库已经实现到对应状态，也不划分中间 MVP。

## 怎么读

不同读者不需要从头读到尾：

| 你想做什么 | 建议读法 |
| --- | --- |
| 快速理解整套设计 | [architecture.md](./architecture.md) → [end-to-end-flow.md](./end-to-end-flow.md) |
| 实现执行内核 | [state-machines.md](./state-machines.md) → [concurrency-and-durability.md](./concurrency-and-durability.md) → [execution-model.md](./execution-model.md) |
| 做安全或合规评审 | [agent-safety-and-guardrails.md](./agent-safety-and-guardrails.md) → [runtime-and-sandbox.md](./runtime-and-sandbox.md) → [multi-tenancy-and-security.md](./multi-tenancy-and-security.md) |
| 接入工具、模型或记忆 | [tool-system.md](./tool-system.md)、[llm-provider.md](./llm-provider.md)、[memory.md](./memory.md) 按需读 |
| 设计多 Agent 或人机协作 | 先确认单 Run 语义，再读 [orchestration-patterns.md](./orchestration-patterns.md) |
| 准备上线和扩容 | [operations.md](./operations.md) → [capacity-and-scaling.md](./capacity-and-scaling.md) |
| 了解完整产品与实施边界 | 根目录 [README.md](../README.md) → [architecture.md](./architecture.md) → [ux-prototype.html](./ux-prototype.html) → [product-implementation-plan.md](./product-implementation-plan.md) |

## 阅读顺序

建议先读 [architecture.md](./architecture.md)。它是总入口，下面每篇文档都只展开其中一个局部。

| 顺序 | 文档 | 这篇主要回答什么 |
| ---: | --- | --- |
| 0 | [architecture.md](./architecture.md) | 整体模型、核心循环、术语、逻辑架构和生产部署基线 |
| 1 | [end-to-end-flow.md](./end-to-end-flow.md) | 一个任务从用户请求到最终完成，中间经过哪些组件 |
| 2 | [state-machines.md](./state-machines.md) | Run、ToolCall、Command 能怎么变，哪些状态不能乱改 |
| 3 | [concurrency-and-durability.md](./concurrency-and-durability.md) | 事件怎么写入、版本怎么检查、崩溃后怎么恢复 |
| 4 | [execution-model.md](./execution-model.md) | Worker 怎么领取任务、执行慢操作、提交结果和处理并行工具 |
| 5 | [agent-safety-and-guardrails.md](./agent-safety-and-guardrails.md) | Agent 安全边界、工具准入、审批和输出检查 |
| 6 | [tool-system.md](./tool-system.md) | 工具怎么声明、注册、发现、升级和下线 |
| 7 | [memory.md](./memory.md) | Agent 记忆怎么写入、检索、清理和隔离 |
| 8 | [llm-provider.md](./llm-provider.md) | 多个模型 Provider 怎么接入、路由、降级和计费 |
| 9 | [orchestration-patterns.md](./orchestration-patterns.md) | 多 Agent、子任务、流水线、监督和人机协作怎么组织 |
| 10 | [realtime.md](./realtime.md) | 实时推送、断线重连、慢连接和 token 流怎么处理 |
| 11 | [runtime-and-sandbox.md](./runtime-and-sandbox.md) | 代码执行环境、sandbox、secret 和 workspace 写入边界 |
| 12 | [multi-tenancy-and-security.md](./multi-tenancy-and-security.md) | 租户隔离、权限、删除、数据保留和人工修复入口 |
| 13 | [operations.md](./operations.md) | Sweeper、监控、故障演练和不变量测试 |
| 14 | [capacity-and-scaling.md](./capacity-and-scaling.md) | 容量怎么描述，生产部署如何扩缩和验收 |

## UX 原型

[ux-prototype.html](./ux-prototype.html) 是一个独立的产品探索原型，可以直接用浏览器打开，用来讨论用户旅程。它不是前端源码，也不应该单独决定架构；如果原型里出现新的架构要求，需要回写到上面的 Markdown 文档里。

## 产品实施计划

[product-implementation-plan.md](./product-implementation-plan.md) 在产品理念、UX 原型和生产架构之上定义 Lites 的完整 GA 产品范围，包括账号体系、成长闭环、企业与合同能力、Agent 内核、数据与 API、实施顺序和发布门禁。

这份计划是目标状态和实施边界，不是当前实现状态，也不把工作拆成可对外发布的 MVP。真正实现某个模块时，仍须遵守对应架构专题中的状态机、持久化、安全和运维语义。

## 已有内容为什么被移除

被移除的旧文档主要属于具体产品或旧实现切片，例如内容体系、课程、学习者画像、练习 runtime、增长、实现计划、实现状态、前端产品蓝图、生成 schema 和本地开发脚本。

如果这些主题以后重新进入范围，应从这套架构内核重新推导，而不是把旧实现整块恢复回来。
