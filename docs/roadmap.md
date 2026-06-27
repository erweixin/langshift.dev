# 技术演进路线图

> 本文档是 [architecture.md](./architecture.md) 的子文档，定义架构能力的建议交付顺序。它描述技术依赖顺序，不描述当前项目实施状态。

## 问题、决策与风险

**问题**：Cloud Agent 平台可以先轻量，但状态机、幂等、权限和恢复语义不能后补。越晚补，越多历史数据和异步路径会变成兼容负担。

**决策**：演进分三阶段：先建立正确性闭环，再补生产安全和运维强度，最后按真实瓶颈替换基础设施。

**为什么不先做规模化基础设施**：如果“命令发出 -> Worker 尝试执行 -> 外部效果发生 -> 结果写回 -> Run 状态推进”这条链没有闭环，换更强的 MQ、runtime 或多区域部署也无法避免重复执行和状态分叉。

**忽略后果**：系统会在小流量下可用，但一遇到 Worker 崩溃、重复投递、并行工具、取消竞态或权限撤销，就出现难以修复的数据状态。

## 阶段一：正确性闭环

目标：用最少基础设施交付一个语义自洽、可故障测试的系统。

- Run、ToolCall、Command 三个状态机及转换表。（见 [state-machines.md](./state-machines.md)）
- EventService append 合约：谁能写、检查什么版本、一个事务里写入哪些内容、失败如何返回。（见 [concurrency-and-durability.md](./concurrency-and-durability.md)）
- 两级 CAS：Run 用 `run_version`，ToolCall 用 `tool_call_version`；`seq` 只做提交顺序。（见 [concurrency-and-durability.md](./concurrency-and-durability.md)）
- outbox、inbox、job_attempt、run/tool_call 当前状态投影分离：发布、消费、执行尝试、业务状态不要混在一张表里。（见 [concurrency-and-durability.md](./concurrency-and-durability.md)）
- `store_epoch` 恢复代次，防止按时间点恢复后旧 command 污染事实源。（见 [concurrency-and-durability.md](./concurrency-and-durability.md)）
- Effect ledger 和工具能力分类，禁止盲目重试 `outcome_unknown`。（见 [execution-model.md](./execution-model.md)）
- 并行 ToolCall join：`FOR UPDATE` 串行化检查，Run CAS 成功后才写 `ResumeAgentRun`。（见 [execution-model.md](./execution-model.md)）
- cancel、deadline、`max_steps`、`max_cost`、approval timeout 和 runtime termination 路径。（见 [state-machines.md](./state-machines.md)）
- Timer / Sweeper：deadline、未知结果对账、quota 回收、租约/旧尝试清理、snapshot、runtime cleanup。（见 [operations.md](./operations.md)）
- Realtime after-commit 发布、`last_seen_seq` 补拉和无竞态重连。（见 [realtime.md](./realtime.md)）
- Run 内上下文预算、压缩和 `context_manifest`。（见 [execution-model.md](./execution-model.md)）
- 平台工具 Descriptor 注册、input/output schema 校验、工具集快照和 `tool_schema_versions` 进入 manifest。（见 [tool-system.md](./tool-system.md)）
- Memory 三层模型（Working / Conversation / Long-term）、写入流经事件、向量检索召回、`memory_document_ids` 进入 manifest。（见 [memory.md](./memory.md)）
- LLM Gateway 统一调用接口、Provider Adapter、`llm_attempt_id` 与成本记录、逻辑模型引用与基本路由。（见 [llm-provider.md](./llm-provider.md)）

## 阶段二：生产加固

目标：让系统在多租户、安全、运维和合规方面具备生产强度。

- PostgreSQL RLS、受限 DB role、租户归属二次校验。（见 [multi-tenancy-and-security.md](./multi-tenancy-and-security.md)）
- 权限只来自可信策略上下文；危险工具必须等待人工审批。（见 [multi-tenancy-and-security.md](./multi-tenancy-and-security.md)）
- Secret Broker、网络出口策略、镜像来源记录、artifact 扫描。（见 [runtime-and-sandbox.md](./runtime-and-sandbox.md)）
- Workspace 单写者、copy-on-write branch、显式合并/冲突语义。（见 [runtime-and-sandbox.md](./runtime-and-sandbox.md)）
- Realtime 连接治理：缓冲上限、慢连接、登录态到期、权限撤销、多标签页和租户连接配额。（见 [realtime.md](./realtime.md)）
- 低基数指标、trace、audit、SLO 和故障注入矩阵。（见 [operations.md](./operations.md)）
- 数据保留与删除：销毁密钥式删除、投影失效、删除审计事件。（见 [multi-tenancy-and-security.md](./multi-tenancy-and-security.md)）
- Admin 写路径全部经 Repair Command API。（见 [multi-tenancy-and-security.md](./multi-tenancy-and-security.md)）
- 租户自定义工具注册审批流程、版本共存与弃用策略、市场工具安装与升级。（见 [tool-system.md](./tool-system.md)）
- Memory 淘汰策略、容量配额、嵌入模型升级与新旧索引并存、数据删除联动 Memory 清除。（见 [memory.md](./memory.md)）
- 多 Provider 降级与熔断、租户级模型策略、模型版本固定与灰度升级、费率管理与成本归属。（见 [llm-provider.md](./llm-provider.md)）

## 阶段三：规模化演进

目标：在前两阶段语义稳定后，根据实际瓶颈替换基础设施。

- 按负载向量压测并声明容量：连接、事件写入、活跃 run、并发 LLM、runtime、token、artifact、热点 conversation。（见 [capacity-and-scaling.md](./capacity-and-scaling.md)）
- 引入独立 Scheduler 或可持久化的消息队列服务，前提是新实现满足既有队列语义契约。（见 [capacity-and-scaling.md](./capacity-and-scaling.md)）
- 按信任等级升级 runtime 到 K8s、microVM 或隔离节点。（见 [runtime-and-sandbox.md](./runtime-and-sandbox.md)）
- 事件表分区、分片或替换存储，但保持 append 合约和状态重建语义。
- 单区域单写模型稳定后，再讨论跨区域 active-active。

阶段一是整套架构成立的前提。只要状态、版本、幂等、未知结果和修复路径被精确定义，PostgreSQL + Redis + Docker 的 Lite 实现就可以可靠运行；如果这些语义缺失，更昂贵的托管基础设施也无法避免重复执行、错误续跑、越权修复和不可解释的状态分叉。
