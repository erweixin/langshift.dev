# EventStore、乐观并发与持久化

> 本文档是 [architecture.md](./architecture.md) 的子文档，定义持久化模型、并发控制与数据一致性机制。

在 lite 架构中，PostgreSQL 是事实源。它应存储事件、outbox（发布状态）、job attempt（执行状态）、run state、tool call、inbox、effect ledger、审计事件、快照引用和运维修复元数据。

## Append API 与两级乐观并发

仅靠唯一键和单调 `seq` 只能防止重复编号，**不能防止两个 worker 基于旧状态做出各自合法但相互冲突的决策**（例如都从某状态恢复后，一个决定完成 run、一个又发起 tool call）。因此条件写的并发令牌取聚合版本，而不是 conversation seq。

**Run 和 ToolCall 是两个独立聚合，因此需要两套 append 路径**，以避免并行 ToolCall 完成时在 `run_version` 上的假冲突：

**路径 A：ToolCall 级写入**（ToolWorker 写入完成/失败事件，不竞争 `run_version`）

```text
append_tool_call_result(
  tenant_id,
  conversation_id,
  tool_call_id,
  expected_tool_call_version,   -- ToolCall 级乐观并发条件
  fence_token,
  events[]
)
```

只有当前 `tool_call_version == expected_tool_call_version` 且 `fence_token` 有效时才允许写入，写入成功后 `tool_call_version` 递增。**不递增 `run_version`**——N 个并行 ToolWorker 各自只竞争自己的 `tool_call_version`，互不干扰。

**路径 B：Run 级写入**（AgentWorker 推进 Run 状态，或 join 胜出者触发 `ResumeAgentRun`）

```text
append_run_transition(
  tenant_id,
  conversation_id,
  run_id,
  expected_run_version,   -- Run 级乐观并发条件
  fence_token,
  events[],
  commands[]
)
```

只有当前 `run_version == expected_run_version` 且 `fence_token` 不低于当前值时才允许写入，写入成功后 `run_version` 递增。

两条路径共享同一个 conversation 的 `seq` 分配机制（锁 conversation 元数据行、原子递增），但并发控制在各自聚合上独立进行。

> **为什么不用单一 `run_version` 保护所有写入**：并行 tool call 场景下，N 个 ToolWorker 同时完成各自独立的工具调用。如果所有写入都竞争 `run_version`，N-1 个会因假冲突被拒绝并重试——它们完成的是不同的 ToolCall，本质上互不冲突。将 ToolCall 拆为独立聚合后，只有在 join 满足、需要推进 Run 状态时才竞争 `run_version`，假冲突降为零。

> **为什么不用 `expected_seq` 做并发控制**：一个会话里允许多个 run 并存。用户发来新消息（开新 run）与某个在飞 run 的工具完成，**应当都能成功且互不冲突**；若把 conversation `seq` 当并发条件会造成假冲突。`seq` 由服务端原子分配、只作排序/游标，并发控制只在聚合上做。

## `seq` 的分配

`seq` 通过锁定 conversation 元数据行、递增其 `current_seq`、并在同一事务插入事件来分配。不要用 `SELECT MAX(seq)+1`，也不要为每个 conversation 创建 PostgreSQL sequence。该行锁天然串行化同一会话内的 append，与各自聚合的版本 CAS（`run_version` / `tool_call_version`）配合即可。

## 关键唯一约束

```text
UNIQUE (tenant_id, conversation_id, seq)
UNIQUE (tenant_id, event_id)
UNIQUE (tenant_id, idempotency_scope, idempotency_key)
UNIQUE (tenant_id, consumer_name, command_id)         -- inbox / 命令去重
UNIQUE (tenant_id, tool_call_id, effect_key)          -- 外部副作用去重
UNIQUE (run_id, parallel_group_id, continuation_kind) -- 并行 join 唯一续跑
UNIQUE (tenant_id, tool_call_id)                      -- ToolCall 聚合主键（承载 tool_call_version）
```

## 分离 outbox 发布状态与 job 执行状态

旧设计让 outbox 同时承载 `pending/leased/published/done/failed/dead_lettered`，在 "outbox → Scheduler → MQ → Worker" 模式下会产生双重事实源。应拆分：

- **`outbox`**：只负责把 command 发到 broker，状态 `pending → publishing → published`；字段含 `command_id`、`topic`、`partition_key`、`available_at`、`publish_attempts`、`lease_owner`、`lease_until`、`published_at`。
- **`job_attempt`**：负责执行过程，状态 `claimed → running → succeeded / failed / timed_out`。
- **`run` / `tool_call`**：负责业务状态。

事件追加、聚合状态更新（run state 或 tool_call state）与 outbox 写入必须在同一事务完成。publisher 仍可能"发布成功、标记 published 前崩溃"，因此重复投递必须被接受、消费端必须经 inbox 去重。

终态 run state 不应被普通流程修改，只有显式的管理员修复流程可以处理。高容量表按时间、租户或 conversation hash 分区/分片。

## 事实源边界

EventStore 无法单独重建完整世界（上下文还依赖 workspace 文件、git 状态、memory 索引、artifact、prompt/policy 版本、模型配置、tool schema、runtime session）。精确定义：

> EventStore 是 **Agent 编排状态、决策过程和引用关系**的事实源；workspace 和 artifact 是**内容**事实源；memory 向量索引是**可重建投影**；runtime session 不保存业务事实。

memory 作为"可重建投影"有一个前提：**agent 写记忆这件事本身必须流经事件**（如 `MemoryUpserted` / `MemoryDeleted` 事件或其派生），向量索引只是对这些事件的物化。否则索引一旦丢失就无法从事实源重建，"可重建"便不成立；而检索回来的文本仍按不可信输入处理（见 [multi-tenancy-and-security.md](./multi-tenancy-and-security.md)）。

## Context Manifest

每次 LLM 调用记录不可变 `context_manifest`（`conversation_through_seq`、`snapshot_id`、`workspace_revision/commit_hash`、`memory_document_ids+versions`、`retrieval_chunk_ids`、`prompt_template_version`、`policy_version`、`tool_schema_versions`、`model_id+parameters`），用于事后解释"为什么当时调用了这个工具"。

**Replay 语义**：replay 用于重建状态；不重新调用 LLM、不重新执行工具；使用已记录的外部结果事件；新 worker 只从当前状态继续下一步。

## Snapshot

**Snapshot 是优化不是事实**，包含 `through_seq`、`projection_version`、`event_schema_version`、`context_builder_version`、`payload/payload_ref`、`checksum`。checksum 不匹配 / projection 不兼容 / upcaster 不支持 / `through_seq` 越界 / workspace revision 不存在时自动丢弃并从事件重建。Lite 阶段小快照放 PostgreSQL、大快照放对象存储，PG 只存引用、hash 与 `through_seq`（与图中独立 Snapshot Store 统一）。

**快照的写入有明确归属与触发策略**，不是"碰巧存在"：由 AgentWorker 在 run 终态时、以及 Timer / Sweeper 按"距上一快照事件数 ≥ N 或时间 ≥ T"周期性触发写入（条件写、带 `through_seq`、幂等）。没有写者 / 没有触发策略，"replay 不随会话长度线性恶化"这条万级承诺就会悬空。
