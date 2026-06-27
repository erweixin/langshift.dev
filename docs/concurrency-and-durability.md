# EventStore、乐观并发与持久化

> 本文档是 [architecture.md](./architecture.md) 的子文档，定义持久化模型、append 合约、并发控制和恢复边界。

## 问题、决策与风险

**问题**：多个 API 请求、Worker、Sweeper 和 Repair API 会同时追加事实。如果只靠唯一键或 `seq`，只能防重复编号，不能防止两个旧决策同时推进同一个 Run。

**决策**：PostgreSQL 是 Lite 架构的持久化核心。EventStore 保存编排事实、聚合状态、outbox、inbox、attempt、effect ledger、审计、snapshot 引用和恢复代次。所有状态推进都走同一个“记账入口”：先检查权限和版本，再把事件、状态和下一步命令一起写入事务。

**为什么不简单用 `seq` 或全局锁**：`seq` 是 conversation 内提交顺序，不代表某个 Run 或 ToolCall 的当前状态；全局锁会把无关 run、无关 tool call 和用户新消息全部阻塞。

**忽略后果**：并行 ToolCall 会在 `run_version` 上产生假冲突；用户新消息会和工具完成互相阻塞；按时间点恢复数据库后，旧消息队列里的消息可能污染已回滚的事实源。

| 应该 | 不应该 |
| --- | --- |
| 用 `run_version` 和 `tool_call_version` 做 CAS | 用 `seq` 当并发令牌 |
| 在一个事务中写事件、聚合状态、outbox、idempotency response | 先发消息再写事件 |
| 对重复 command 用 inbox 去重 | 假设 publisher 不会重复发布 |
| 按时间点恢复数据库后递增 `store_epoch` | 接受旧 epoch 的 command |

## 持久化边界

EventStore 是 **Agent 编排状态、决策过程和引用关系** 的事实源。它不单独保存完整世界：

- workspace 和 artifact 是内容事实源。
- memory 向量索引是可重建投影，前提是 memory 写入也流经事件，如 `MemoryUpserted` / `MemoryDeleted`。
- runtime session 可丢弃，不保存业务事实。
- prompt、policy、tool schema、模型配置、workspace revision 等必须以版本或引用进入 `context_manifest`。

## Append 合约总览

这里的 `append` 可以理解为“追加一笔系统账”。无论请求来自 API、Worker、Sweeper 还是 Repair API，都要把下面这些信息带齐，EventService 才能判断这笔账能不能写。

所有 append API 都共享同一个请求外壳：

```text
append_request
- tenant_id                              # 哪个租户
- actor: api | agent_worker | ...       # 谁在写
- conversation_id                        # 哪个会话
- aggregate_kind                         # 写 Run、ToolCall、Command 还是 Repair
- aggregate_id                           # 具体对象 id
- expected_version or dedupe key         # 版本检查或去重键
- fence_token                            # Worker 持有租约时必须带
- idempotency_scope + idempotency_key    # 客户端可重试请求必须带
- causation_id / correlation_id          # 这次写入由什么触发
- events[]                               # 已发生的事实
- commands[]                             # 下一步要做的事
- realtime_notifications[]               # 提交通知
```

通用事务顺序：

1. 打开事务，并设置 tenant/RLS 上下文。
2. 读取当前 `store_epoch`，拒绝低于当前 epoch 的 command。
3. 校验写入者权限、租户归属、状态转换和幂等信息。
4. 对目标聚合做条件更新：Run 用 `run_version`，ToolCall 用 `tool_call_version`，command/inbox/effect 用唯一键。
5. 锁定 conversation 元数据行，按事件数量原子分配连续 `seq`。
6. 插入事件，事件携带 `store_epoch`、`seq`、因果字段和 schema version。
7. 插入 outbox rows、realtime notification rows、audit rows、idempotency response。
8. 提交事务。事务提交后 publisher 才能发布实时通知或 command。

通用错误结果：

| 错误 | 含义 | 调用方怎么处理 |
| --- | --- | --- |
| `idempotency_replay` | 相同 key 和请求内容已经成功过 | 返回已保存的响应 |
| `idempotency_conflict` | 相同 key 但请求内容不同 | 拒绝请求，避免一个 key 表示两件事 |
| `invalid_transition` | 状态机不允许该转换 | 不重试，除非 Repair API 明确处理 |
| `stale_version` | 对象版本已经被别人推进 | 重新读取状态，再决定是否放弃 |
| `stale_fence` | Worker 的租约已经过期或被别人替代 | 标记为旧尝试，不推进状态 |
| `duplicate_command` | 这条 command 已被该消费者处理 | ack 并忽略 |
| `duplicate_effect` | 同一个 `effect_key` 的外部效果已确认 | 返回已确认结果，或进入对账 |
| `wrong_store_epoch` | command 来自数据库恢复前的旧代次 | 拒绝执行并记录审计 |

## Run 级 append

Run 状态转换由 `append_run_transition` 完成。

```text
append_run_transition(
  tenant_id,
  conversation_id,
  run_id,
  expected_run_version,
  fence_token?,
  transition,
  events[],
  commands[],
  idempotency?
)
```

前置条件：

- `tenant_id` 拥有该 `conversation_id` 和 `run_id`。
- 当前 Run 状态允许该 `transition`。
- 当前 `run_version == expected_run_version`。
- 如果请求来自 Worker，`fence_token` 必须不低于当前 run lease fence。
- 终态 Run 只能由 Repair Command API 创建 replacement run，不能直接改写。

写入内容：

- Run projection（当前状态投影）：`status`、`run_version + 1`、必要的 `cancel_requested` / `due_at` / budget 字段。
- `events[]`：例如 `RunStarted`、`ToolCallRequested`、`RunSucceeded`、`RunExpired`。
- `commands[]`：例如 `ExecuteToolCall`、`ResumeAgentRun`、`RuntimeTerminationRequested`。
- idempotency response：客户端可见请求必须持久保存响应。

失败行为：

- `stale_version`：调用者重新读取 Run。Worker 不应盲目重放 LLM 输出。
- `stale_fence`：attempt 变成旧尝试；如果已产生外部副作用，交给对账处理。
- `idempotency_replay`：返回原 `run_id` 和原响应，不创建新 run。

## ToolCall 级 append

ToolCall 状态转换由 `append_tool_call_result` 完成。

```text
append_tool_call_result(
  tenant_id,
  conversation_id,
  run_id,
  tool_call_id,
  expected_tool_call_version,
  fence_token,
  effect_key?,
  result_kind,
  events[],
  join_check?
)
```

前置条件：

- ToolCall 属于该 tenant、conversation 和 run。
- 当前 `tool_call_version == expected_tool_call_version`。
- `fence_token` 有效。
- 如果存在外部副作用，`effect_key` 和请求摘要必须写入 effect ledger。
- `outcome_unknown` 不能直接转为 retry；必须通过对账或 Repair API 收敛。

写入内容：

- ToolCall projection（当前状态投影）：`status`、`tool_call_version + 1`、`result_event_id`。
- effect ledger：`prepared` / `executing` / `confirmed` / `failed` / `outcome_unknown`。
- 结果事件：`ToolCallSucceeded`、`ToolCallFailed`、`ToolCallOutcomeUnknown` 等。
- 可选 join 检查：在同一事务中锁定 `parallel_group` 行，满足条件时尝试 Run 级推进。

失败行为：

- `stale_version` 或 `stale_fence`：结果不推进 ToolCall；attempt 记录为旧尝试。
- effect 已确认：返回已确认结果，不重复外部副作用。
- effect 结果未知：进入 `outcome_unknown`，由 Sweeper 在 `due_at` 后对账。

## Command / inbox 合约

命令发布和命令消费分开建模。

- outbox 只负责发布：`pending -> publishing -> published`。
- inbox 只负责消费去重：`UNIQUE (tenant_id, consumer_name, command_id)`。
- job attempt 只负责记录一次执行尝试：`claimed -> running -> succeeded / failed / timed_out`。
- Run / ToolCall 负责业务状态。

Publisher 可能在消息队列确认后、标记 `published` 前崩溃。因此重复发布是正常情况，消费者必须先写 inbox 再执行 command。消费者执行完成后，不通过 outbox 标记业务成功，而是通过 Run/ToolCall append 写入结果事件。

## `seq` 的分配

`seq` 是 conversation-scoped 的提交游标：

1. 锁定 conversation 元数据行。
2. 读取并递增 `current_seq`。
3. 在同一事务中插入事件。

不要使用 `SELECT MAX(seq)+1`，也不要给每个 conversation 创建 PostgreSQL sequence。conversation 行锁会串行化同一会话内 append；不同 Run 和 ToolCall 的并发安全由各自聚合版本负责。

## 关键唯一约束

```text
UNIQUE (tenant_id, conversation_id, seq)
UNIQUE (tenant_id, event_id)
UNIQUE (tenant_id, idempotency_scope, idempotency_key)
UNIQUE (tenant_id, consumer_name, command_id)         -- inbox / 命令去重
UNIQUE (tenant_id, tool_call_id, effect_key)          -- 外部副作用去重
UNIQUE (tenant_id, run_id, parallel_group_id, continuation_kind)
UNIQUE (tenant_id, tool_call_id)                      -- ToolCall 聚合主键
```

如果使用分片或分区，唯一约束必须保留 tenant 与聚合边界，不能把唯一性降级成单节点假设。

## `store_epoch` 与恢复

PITR 是按时间点恢复数据库。恢复后，数据库可能回到消息队列已经看不到的旧时间点。旧消息队列中仍可能有“恢复点之后发布过”的 command。为了避免这些 command 反向污染事实源，EventStore 维护单调 `store_epoch`。

规则：

- 每个事件、outbox command、job attempt 和 inbox 记录都携带 `store_epoch`。
- 正常运行时 epoch 不变。
- 按时间点恢复（PITR）、主库替换或确认事实源回退后，必须递增 epoch。
- 消费者在执行 command 前读取当前 epoch；command epoch 低于当前 epoch 时拒绝执行并审计。
- Publisher 只发布当前 epoch 的 outbox row。
- Repair API 可以根据审计决定是否在新 epoch 重新创建 replacement command。

`store_epoch` 不是业务版本，也不参与 Run/ToolCall CAS。它只回答一个问题：这条异步 command 是否来自当前事实源代次。

## Context Manifest

`context_manifest` 是“当时喂给模型的上下文清单”。它不一定保存全文，但必须保存足够引用，让事后能回答：模型当时看到了哪些对话、文件、记忆、工具 schema 和策略版本。

每次 LLM 调用记录不可变 `context_manifest`：

- `conversation_through_seq`
- `snapshot_id`
- `workspace_revision` / `commit_hash`
- `memory_document_ids` + versions
- `retrieval_chunk_ids`
- `prompt_template_version`
- `policy_version`
- `tool_schema_versions`
- `model_id` + parameters
- `context_builder_version`
- `compaction_strategy_version`

Replay 是从历史事件重建状态。它不重新调用 LLM、不重新执行工具。新 Worker 使用已记录的外部结果事件，从当前状态继续下一步。

## Snapshot

Snapshot 是状态快照，用来减少从头 replay 的成本。它是优化，不是事实源；损坏时可以丢弃并从事件重建。它包含：

- `through_seq`
- `projection_version`
- `event_schema_version`
- `context_builder_version`
- `payload` 或 `payload_ref`
- `checksum`

checksum 不匹配、projection 不兼容、upcaster 不支持、`through_seq` 越界或 workspace revision 不存在时，系统自动丢弃 snapshot 并从事件重建。这里的 projection 是从事件推导出的当前状态，upcaster 是把旧事件格式升级到新格式的转换器。

Snapshot 写入有明确归属：AgentWorker 可在 run 终态时写；Timer / Sweeper 可按“距上一快照事件数 >= N 或时间 >= T”触发写入。写入必须条件化、幂等，并带 `through_seq`。
