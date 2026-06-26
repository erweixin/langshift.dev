# 实施路线图

> 本文档是 [architecture.md](./architecture.md) 的子文档，定义架构落地的分阶段计划。

本节定义上述设计的落地顺序，分三个阶段，每个阶段都交付一个语义自洽、可独立验证的系统。

## 阶段一：正确性闭环（首版即必须具备）

这一阶段全部是 schema + 纪律，几乎不引入额外基础设施，但乐观并发与状态机无法事后补，因此在写第一行业务逻辑之前就定义到位。

- Run、ToolCall、Command 三个正式状态机及其不变量。（→ [state-machines.md](./state-machines.md)）
- 两级乐观并发：Run 状态转换以 `expected_run_version` CAS，ToolCall 状态转换以 `expected_tool_call_version` CAS，均配合原子 fence 校验；`seq` 由服务端分配。（→ [concurrency-and-durability.md](./concurrency-and-durability.md)）
- outbox 发布状态与 job 执行状态分离。（→ [concurrency-and-durability.md](./concurrency-and-durability.md)）
- inbox、idempotency response store 与 tool effect ledger 落表（去重与 reconciliation 可先定 schema、按需逐步实现）。（→ [execution-model.md](./execution-model.md)）
- 实时发布 after-commit 与无竞态重连协议。（→ [realtime.md](./realtime.md)）
- Admin 写路径全部经 Repair Command API，移除对 EventDB/DLQ 的直接写。（→ [multi-tenancy-and-security.md](./multi-tenancy-and-security.md)）
- cancel、deadline、`max_steps`/`max_cost`、outcome-unknown 与 reconciliation 的判定路径。（→ [state-machines.md](./state-machines.md)）
- **Timer / Sweeper 后台巡检**：deadline / 挂起超时、outcome_unknown 复核、quota 回收、lease/orphan GC、snapshot 触发——这是上述转换能否闭环的执行体，不能留到后期。（→ [operations.md](./operations.md)）
- 取消向在飞执行体 / sandbox 的传播（协作式中止 + `RuntimeTerminationRequested`）。（→ [state-machines.md](./state-machines.md)）
- 并行 join 的归属与 `any`/`quorum` 残余工具处理（去中心化竞争 + 唯一续跑约束）。（→ [execution-model.md](./execution-model.md)）
- Run 内上下文预算与压缩策略（决策进入 `context_manifest`）。（→ [execution-model.md](./execution-model.md)）

## 阶段二：生产加固

在正确性闭环稳定后，补齐多租户、配额、隔离与可观测的生产强度。

- 事件 schema version、causation/correlation 与 context manifest。（→ [concurrency-and-durability.md](./concurrency-and-durability.md)）
- quota reservation 与按稀缺资源公平的调度算法。（→ [execution-model.md](./execution-model.md)）
- workspace revision 与单写者/copy-on-write 语义。（→ [runtime-and-sandbox.md](./runtime-and-sandbox.md)）
- PostgreSQL RLS、独立 DB role 与管理权限分离。（→ [multi-tenancy-and-security.md](./multi-tenancy-and-security.md)）
- 端到端 SLO、低基数 metrics 与完整故障注入矩阵。（→ [operations.md](./operations.md)）
- 数据保留与删除（crypto-shredding、`SubjectErasureRequested`、投影失效重建）。（→ [multi-tenancy-and-security.md](./multi-tenancy-and-security.md)）

## 阶段三：规模化演进

仅在真实瓶颈出现、且前两阶段的语义契约已固化后才推进，避免过早引入分布式复杂度。

- 按真实瓶颈引入独立 Scheduler 或 durable broker（先固定语义契约，再替换实现）。（→ [capacity-and-scaling.md](./capacity-and-scaling.md)）
- 按租户信任等级升级到 microVM / 隔离节点。（→ [runtime-and-sandbox.md](./runtime-and-sandbox.md)）
- 在确认单区域单写模型站稳后，再讨论跨区域 active-active。

> 阶段一是整套架构成立的前提：当 `Command → 持久发布 → Attempt → 外部副作用 → 结果事件 → Run 状态转换 → 下一个 Command` 这条链上的状态、版本、幂等、未知结果与修复路径被精确定义，PostgreSQL + Redis + Docker 的 Lite 实现就足够可靠；反之即使换成昂贵的托管基础设施，仍会出现重复执行、错误续跑、越权修复和不可解释的状态分叉。
