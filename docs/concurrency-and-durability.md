# EventStore、乐观并发与持久化

> 定位：主线文档。本文档定义“怎么把事实安全写进数据库”。重点不是表名，而是写入顺序、版本检查、重复投递去重，以及数据库恢复后的保护边界。

## 问题、决策与风险

**问题**：多个 API 请求、Worker、Sweeper 和 Repair API 会同时追加事实。如果只靠唯一键或 `seq`，只能防重复编号，不能防止两个旧决策同时推进同一个 Run。

**决策**：具备强事务、条件写和租户隔离能力的数据库是 EventStore 持久化核心，PostgreSQL 是默认实现选择。EventStore 保存编排事实、聚合状态、outbox、inbox、attempt、effect ledger、审计、snapshot 引用和恢复代次。所有状态推进都走同一个“记账入口”：先检查权限和版本，再把事件、状态和下一步命令一起写入事务。

**为什么不简单用 `seq` 或全局锁**：`seq` 是 user 内提交顺序，不代表某个 Run 或 ToolCall 的当前状态；全局锁会把无关 run、无关 tool call 和用户新消息全部阻塞。

**忽略后果**：并行 ToolCall 会在 `run_version` 上产生假冲突；用户新消息会和工具完成互相阻塞；按时间点恢复数据库后，旧消息队列里的消息可能污染已回滚的事实源。

| 应该 | 不应该 |
| --- | --- |
| 用 `run_version` 和 `tool_call_version` 做 CAS | 用 `seq` 当并发令牌 |
| 在一个事务中写事件、聚合状态、outbox、idempotency response | 先发消息再写事件 |
| 对重复 command 用 inbox 去重 | 假设 publisher 不会重复发布 |
| 按时间点恢复数据库时在独立恢复控制面轮换 `store_epoch` | 从已回滚数据库里的旧 epoch 简单 `+1` |

## 先用白话说

EventStore 像一本不能随便涂改的账本。每次系统要推进状态，都不是“改一下 status”这么简单，而是要同时完成几件事：

1. 检查写入者有没有权限、是不是拿着最新版本。
2. 写下已经发生的事实，也就是 event。
3. 更新当前状态投影，方便快速查询。
4. 如果还要继续异步执行，把下一步 command 写进 outbox。
5. 把实时通知、审计和幂等响应一起落库。

这些动作必须在同一个数据库事务里完成。事务提交后，外部发布器才能把 command 或通知发出去。

## 持久化边界

EventStore 是 **Agent 编排状态、决策过程和引用关系** 的事实源。它不单独保存完整世界：

- workspace 和 artifact 是内容事实源。
- memory 向量索引是可重建投影，前提是 memory 写入也流经事件，如 `MemoryUpserted` / `MemoryDeleted`。
- runtime session 可丢弃，不保存业务事实。
- prompt、policy、tool descriptor、模型配置、workspace revision 等必须以不可变 snapshot、引用或 digest 进入 `context_manifest`。

## Append 合约总览

这里的 `append` 可以理解为“追加一笔系统账”。无论请求来自 API、Worker、Sweeper 还是 Repair API，都要把下面这些信息带齐，EventService 才能判断这笔账能不能写、写完后要不要继续发命令。

所有 append API 都共享同一个请求外壳：

```text
append_request
- tenant_id                              # 哪个租户
- actor: api | agent_worker | ...       # 谁在写
- conversation_id                        # 哪个会话
- aggregate_kind                         # 写 Run、ToolCall、Command 还是 Repair
- aggregate_id                           # 具体对象 id
- expected_version or dedupe key         # 版本检查或去重键
- command_id + attempt_id + fence_token  # Worker 持有租约时必须带，且必须匹配当前 active command/attempt
- idempotency_scope + idempotency_key    # 客户端可重试请求必须带
- causation_id / correlation_id          # 这次写入由什么触发
- events[]                               # 已发生的事实
- commands[]                             # 下一步要做的事
- realtime_notifications[]               # 提交通知
```

通用事务顺序：

1. 打开事务，并设置 tenant/RLS 上下文。
2. 读取当前 `store_epoch`，拒绝与当前 epoch 不完全相等的 command；epoch 是恢复代次身份，不做大小猜测。
3. 校验写入者权限、租户归属、状态转换和幂等信息。
4. 对目标聚合做条件更新：Run 用 `run_version`，ToolCall 用 `tool_call_version`，command/inbox/effect 用唯一键。
5. 如转换需要 join、审批 policy 或父子 Run 协调，先锁定这些协调行并读取当前聚合版本。
6. 锁定 `event_cursors(user_id)` 行，按事件数量原子分配连续 `seq`。
7. 插入事件，事件携带 `store_epoch`、`seq`、因果字段和 schema version。
8. 插入 outbox rows、realtime notification rows、audit rows、idempotency response。
9. 提交事务。事务提交后 publisher 才能发布实时通知或 command。

### 事务隔离与锁顺序纪律

EventService 写事务使用 PostgreSQL `READ COMMITTED`。原因很简单：join、审批和 child group 都需要在拿到协调行锁后，再看一眼其他成员的最新已提交状态。不要在这些写路径上使用 `REPEATABLE READ` 快照，否则容易拿着旧画面做新决定。

所有写事务遵守下面的锁顺序：

1. 幂等、inbox、effect ledger 等去重键。
2. 当前命令直接推进的业务聚合行，例如 `runs` 或 `tool_calls`。
3. 协调行，例如 `parallel_groups`、`approval_groups`、`child_groups`、`continuations`。
4. 需要被协调推进的父聚合行，例如 join 满足后锁父 `runs` 行。
5. `event_cursors(user_id)`。
6. `events`、`outbox`、`jobs`、notification、audit 和 idempotency response 写入。

`event_cursors` 必须在所有业务状态行和协调行之后锁定。任何流程都不能先分配 `seq` 再回头更新 Run 或 ToolCall；否则 join 与 cancel、approval 与 timeout 这类路径会形成反向锁序。

如果 PostgreSQL 返回 deadlock detected (`40P01`) 或 serialization failure (`40001`)，EventService 可以重试整个短事务，并且重试必须重新读取当前状态和版本。Worker 不得因此重放外部 I/O；外部效果已经发生时，只重试“提交结果到 EventStore”的短事务，仍受 fence、effect ledger 和版本检查保护。

## 数据模型总览

表结构可以随实现细化，但语义边界应保持如下划分：

| 表 / 投影 | 语义归属 | 关键规则 |
| --- | --- | --- |
| `events` | 不可变事实日志 | 带 `tenant_id`、`user_id`、`seq`、`store_epoch`、因果字段和 schema version |
| `runs` | Run 当前状态投影 | 只由 Run 状态转换更新；`run_version` 是 CAS 令牌；排队时绑定 `pending_command_id`，执行中保存 `active_command_id`、`active_attempt_id`、`current_fence_token`、`lease_expires_at` |
| `tool_calls` | ToolCall 当前状态投影 | 只由 ToolCall 状态转换更新；`tool_call_version` 是 CAS 令牌；排队时绑定 `pending_command_id`，执行中保存 `active_command_id`、attempt / fence / lease |
| `parallel_groups` / `child_groups` | join 协调行 | join 检查必须 `SELECT ... FOR UPDATE` 后读取成员状态 |
| `continuations` | 恢复命令唯一占位 | `committed` 才能对应 outbox；不得用旧版本造成永久 `skipped` 占位 |
| `outbox` | 待发布 command | 至少一次发布；不代表业务执行成功 |
| `inbox` | command 消费 claim / completion ledger | `UNIQUE (tenant_id, consumer_name, command_id)`；区分 `running` 与 `completed`，插入不等于已处理 |
| `jobs` / `job_attempts` | 队列与执行尝试 | attempt 记录执行过程；业务状态仍在 Run / ToolCall |
| `tool_effects` | 外部副作用 ledger | 用 `effect_key`、请求摘要和 provider id 支撑幂等与对账 |
| `event_cursors` | user-scoped `seq` 分配 | 只做提交顺序和补拉游标，不做业务 CAS |
| `run_messages` | 最终消息投影 | 保存消息 envelope 或 artifact 引用；敏感正文用 `payload_ref` |
| `run_message_chunks` | token / delta 短期流日志 | run-scoped cursor，有 TTL；chunk 也走 payload envelope，不是 EventStore 事实源 |
| `llm_attempts` | LLM 调用 ledger | `attempt_key`、Provider、模型、usage、cost、fallback 和错误 |
| `memory_documents` | Memory 可重建投影 | 写入来源必须有 `MemoryUpserted` / `MemoryDeleted` 事件 |
| `snapshots` | replay 优化 | 可丢弃重建；不能成为唯一事实源 |

## 敏感载荷 Envelope

EventStore 要回答“系统知道这件事发生过吗”，不应该变成保存所有正文的仓库。凡是可能包含用户隐私、模型正文、工具输出、审批 diff、外部网页内容、子 Run 结果、Realtime chunk、memory 内容或 artifact 摘要的字段，都统一使用 payload envelope。

通俗地说：事件里放收据，正文包裹放到加密存储。收据能证明包裹是哪一个、有没有被篡改、谁能看、多久删除；但普通 replay 不需要把包裹拆开。

```text
payload_envelope
- payload_ref?              # 指向加密 payload / artifact / object storage
- payload_hmac              # 用稳定密钥计算，用来去重、审计和证明未被篡改
- payload_summary?          # 给 UI 展示的脱敏摘要；不能包含 secret 或未授权 PII
- sensitivity_labels[]      # public | internal | pii | secret | customer_data | tool_output 等
- retention_class           # ephemeral | run_lifetime | audit_limited | legal_hold
- source_trust              # user | model | tool | child_agent | human_approver | platform
- payload_schema_version
```

硬规则：

- 不确定是否敏感时，按敏感处理，存 `payload_ref`，不要把正文直接写进事件、audit、trace 或 snapshot。
- `payload_summary` 只是展示摘要，不是授权来源；父 Agent、审批界面或 Repair API 需要原文时，必须重新做 ACL、DLP、retention 和 guardrail 检查。
- 明文只允许用于小型、低风险、已经 `OutputChecked(decision=allow|redact)` 且 policy 允许持久化的展示字段；即使如此也要带 `payload_hmac` 和敏感标签。
- 删除主体数据时，payload 使用租户/主体密钥 crypto-shredding；message、chunk、memory、snapshot、search index 和 cache 等派生物都必须失效或重建。

通用错误结果：

| 错误 | 含义 | 调用方怎么处理 |
| --- | --- | --- |
| `idempotency_replay` | 相同 key 和请求内容已经成功过 | 返回已保存的响应 |
| `idempotency_conflict` | 相同 key 但请求内容不同 | 拒绝请求，避免一个 key 表示两件事 |
| `invalid_transition` | 状态机不允许该转换 | 不重试，除非 Repair API 明确处理 |
| `stale_version` | 对象版本已经被别人推进 | 重新读取状态，再决定是否放弃 |
| `stale_fence` | Worker 的租约已经过期或被别人替代 | 标记为旧尝试，不推进状态 |
| `duplicate_command` | 这条 command 已被该消费者完成处理 | ack 并忽略；若只是旧 claim 未完成，必须按 lease / retry 规则重新领取或等待 |
| `duplicate_effect` | 同一个 `effect_key` 的外部效果已确认 | 返回已确认结果，或进入对账 |
| `duplicate_effect_unknown` | 同一个 `effect_key` 已被人工收敛为 `accepted_unknown` | 禁止再次执行；返回残余风险记录，只能由新的人工修复流程处理 |
| `wrong_store_epoch` | command 的恢复代次与当前事实源代次不相等 | 拒绝执行并记录审计；不能按数值大小推断新旧 |

## Run 级 append

Run 状态转换由 `append_run_transition` 完成。

```text
append_run_transition(
  tenant_id,
  conversation_id,
  run_id,
  expected_run_version,
  command_id?,
  attempt_id?,
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
- 如果请求来自 Worker，`command_id` / `attempt_id` 必须分别等于当前 active command / attempt，`fence_token` 必须精确等于 `current_fence_token`，lease 尚未过期，且 inbox claim 仍属于同一 attempt。
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
  command_id?,
  attempt_id?,
  fence_token?,
  effect_key?,
  result_kind,
  events[],
  join_check?
)
```

前置条件：

- ToolCall 属于该 tenant、conversation 和 run。
- 当前 `tool_call_version == expected_tool_call_version`。
- 如果结果来自 ToolWorker，`command_id` / `attempt_id` 分别等于 ToolCall 当前 active command / attempt，`fence_token` 精确等于当前 fence，lease 尚未过期，且 inbox claim 仍属于同一 attempt。Sweeper/Reconciler/Repair 通过各自已 claim 的 command、actor 权限和 `tool_call_version` 推进，不冒充原 ToolWorker fence。
- 如果存在外部副作用，`effect_scope`、`provider_id`、`tool_name`、`effect_key` 和 `request_hash` 必须写入 effect ledger。
- `outcome_unknown` 不能直接转为 retry；必须通过对账或 Repair API 收敛。

写入内容：

- ToolCall projection（当前状态投影）：`status`、`tool_call_version + 1`、`result_event_id`。
- effect ledger：`prepared` / `executing` / `commit_authorized` / `confirmed` / `failed` / `outcome_unknown` / `accepted_unknown`。
- 结果事件：`ToolCallSucceeded`、`ToolCallFailed`、`ToolCallOutcomeUnknown` 等。
- 可选 join 检查：在同一事务中锁定 `parallel_group` 行，满足条件时尝试 Run 级推进。

失败行为：

- `stale_version` 或 `stale_fence`：结果不推进 ToolCall；attempt 记录为旧尝试。
- effect 已确认且 `request_hash` 一致：返回已确认结果，不重复外部副作用。
- effect 已是 `accepted_unknown`：不得把它当失败重试或生成新 ToolCall；返回人工处理记录并保持外部去重占位。
- effect key 已存在但 `request_hash` 不一致：拒绝写入并升级人工裁定，避免一个幂等键表示两种外部意图。
- effect 结果未知：进入 `outcome_unknown`，由 Sweeper 在 `due_at` 后对账。

## Command / inbox 合约

命令发布和命令消费分开建模。Inbox 不是“插入即完成”的去重表，而是消费者对某条 command 的 claim 与 completion ledger。

- outbox 只负责发布：`pending -> publishing -> published`。
- inbox 负责消费 claim 与完成去重：`UNIQUE (tenant_id, consumer_name, command_id)`，并保存 `state`、`lease_token`、`lease_expires_at`、`attempt_id`、`command_payload_hash`、`completed_at`。
- job attempt 只负责记录一次执行尝试：`claimed -> running -> succeeded / failed / timed_out`。
- Run / ToolCall 负责业务状态。

Publisher 可能在消息队列确认后、标记 `published` 前崩溃。因此重复发布是正常情况，消费者必须先 claim inbox 再执行 command，但 claim 只能表示“某个 attempt 正在处理”，不能表示“command 已经处理完”。

```text
command_inbox
- tenant_id
- consumer_name
- command_id
- command_payload_hash
- state: running | completed | abandoned
- lease_token
- fence_token
- lease_expires_at
- attempt_id
- completed_at?
```

领取规则：

- 首次投递：插入 `running` row，创建 `job_attempt`，原子安装新的 `attempt_id`、单调递增的 `fence_token` 和 `lease_token` 后才能执行外部 I/O。
- 重复投递且 row 为 `completed`：ack 并忽略。
- 重复投递且 row 为 `running` 且 lease 未过期：不执行第二份外部 I/O，按队列语义 ack / nack / 延迟重投。
- 重复投递且 row 为 `running` 但 lease 已过期：用条件更新抢占新 lease，旧 attempt 之后提交会因 stale fence 被拒绝。
- command payload hash 与已存在 inbox row 不一致：拒绝并审计，避免一个 `command_id` 表示两种意图。

消费者执行完成后，不通过 outbox 标记业务成功，而是在同一数据库事务中通过 Run/ToolCall append 写入结果事件，并把对应 inbox row 从 `running` 改为 `completed`。事务提交后再 ack 外部队列消息。若 Worker 在 claim 后、完成前崩溃，后续重投会在 lease 过期后重新领取，不会因为 inbox row 已存在而永久丢任务。

如果 `jobs` 同时是队列和唯一消费入口，它必须承载与 inbox 等价的 claim / completion 状态机，并用 `UNIQUE (command_id)` 保证同一消费域内唯一。一旦引入独立 outbox / MQ，或出现第二类消费者，`jobs` 就不再是唯一入口，必须使用独立 inbox 表或等价 ledger，否则去重和重领语义会静默丢失。

## Attempt、Lease 与 Fence 合约

lease 表示当前执行权是否仍然有效，fence 标识当前生效的执行权代次。Worker 结果提交和 heartbeat 必须同时精确匹配 `command_id`、`attempt_id`、fence 和 lease；单独比较单调递增的 fence 不能证明执行权有效。

领取 Start / Resume / Tool command 时，EventService 在同一个短事务里：

1. claim 对应 inbox row；
2. 校验 Run / ToolCall 的 `pending_command_id` 与本 command 精确匹配，再创建新的 `job_attempt`；
3. 把 `pending_command_id` 移为 `active_command_id`，并在对应 Run 或 ToolCall 上安装 `active_attempt_id`；
4. 对该聚合原子递增并保存 `current_fence_token`；
5. 保存 `lease_owner`、`lease_token` 和 `lease_expires_at`；
6. 对 Agent Run，把 `queued -> executing`，首次写 `RunStarted`，恢复写 `RunResumed`。

heartbeat 只能在 `attempt_id`、`lease_token` 和 fence 都精确匹配时延长 `lease_expires_at`，不能替换 active attempt。lease 过期后的抢占必须创建新 attempt 并递增 fence；旧 Worker 即使稍后恢复，也无法重新续租或提交。

当前步骤结束时，业务结果、inbox completion、`job_attempt` completion 和 active lease 释放必须在同一 EventStore 事务完成，并要求 command/attempt/fence 精确匹配。释放会清空 `active_command_id`、`active_attempt_id`、lease owner/token/expiry，但保留 `current_fence_token` 作为单调计数器；下次 Start / Resume / Retry claim 在此基础上递增。取消或 Sweeper 只有在确认旧执行体已停止，或先用新 fence 使其失效后，才能释放/替换 lease。

Worker 结果提交的条件必须等价于：

```sql
WHERE aggregate_version = :expected_version
  AND active_command_id = :command_id
  AND active_attempt_id = :attempt_id
  AND current_fence_token = :fence_token
  AND lease_expires_at > now()
```

禁止使用 `worker_fence >= current_fence_token`。大于当前值不代表它由系统签发，也不能证明它属于当前 command 和 attempt。`lease_token` 是不可猜的持有凭证，`fence_token` 是每个 Run / ToolCall claim 域内的单调序号；二者都不能由客户端或业务 handler 自行提供。

## `seq` 的分配

baseline 中 `seq` 是 user-scoped 的提交游标，conversation 只是事件过滤维度：

1. 锁定 `event_cursors(user_id)` 行。
2. 读取并递增 `next_seq`。
3. 在同一事务中插入事件。

不要使用 `SELECT MAX(seq)+1`，也不要给每个 conversation 创建 PostgreSQL sequence。用户级 cursor 行锁会串行化同一用户内 append；不同 Run 和 ToolCall 的并发安全由各自聚合版本负责。

## 关键唯一约束

```text
UNIQUE (tenant_id, user_id, seq)
UNIQUE (tenant_id, event_id)
UNIQUE (tenant_id, idempotency_scope, idempotency_key)
UNIQUE (tenant_id, consumer_name, command_id)         -- inbox / 命令去重
UNIQUE (tenant_id, effect_scope, provider_id, tool_name, effect_key) -- 外部副作用去重，跨 ToolCall / replacement run / redrive 生效
UNIQUE (tenant_id, run_id, parallel_group_id, continuation_kind)
UNIQUE (tenant_id, tool_call_id)                      -- ToolCall 聚合主键
```

如果使用分片或分区，唯一约束必须保留 tenant 与聚合边界，不能把唯一性降级成单节点假设。

## `store_epoch` 与恢复

PITR 是按时间点恢复数据库。恢复后，数据库可能回到一个更早的时间点，但外部消息队列里还留着“恢复点之后发布过”的 command。为了防止这些旧 command 回来污染新的事实源，每次事实源恢复代次都使用唯一的 `store_epoch`。

epoch 的权威值不能只存在于会被同一次 PITR 回滚的 EventStore 中。恢复控制面必须在独立故障域保存当前 epoch，例如独立控制存储中的 CAS record，或带条件写的恢复 manifest。记录至少包含单调 `generation_number`、随机不可复用的 `generation_id`、创建时间和恢复操作审计；command 携带完整 epoch 身份，消费者按精确相等判断。

正常规则：

- 每个事件、outbox command、job attempt 和 inbox 记录都携带 `store_epoch`。
- 正常运行时 epoch 不变。
- Publisher 只发布与当前外部权威 epoch 精确匹配的 outbox row。
- 消费者执行前同时读取外部权威 epoch 和本地已安装 epoch；command 只要与当前 epoch 不相等就拒绝并审计。
- 外部恢复控制面不可读、签名/版本校验失败或本地安装值不一致时，Publisher 和新 claim 必须 fail closed；正在执行的 Worker 只能提交到仍匹配的当前代次，不能自行猜测 epoch。
- Repair API 可以根据审计决定是否在当前 epoch 创建 replacement command。

PITR、主库替换或事实源回退必须执行封闭恢复流程：

1. 停止新请求、Publisher、Scheduler 和 Worker claim；
2. 在独立恢复控制面用 CAS 轮换到全新的 epoch，不能从恢复后的数据库值简单 `+1`；
3. 恢复数据库，并在任何异步消费者启动前把新 epoch 安装到 EventStore；
4. 隔离或清理外部队列中的旧消息；即使清理不完全，epoch 精确匹配也会拒绝它们；
5. 从当前 EventStore 状态重建仍合法的 command，使用新的 `command_id`、causation 和新 epoch；
6. 先灰度开放消费者，再开放写入口，并记录恢复完成审计。

`store_epoch` 不是业务版本，也不参与 Run/ToolCall CAS。它只回答一个问题：这条异步 command 是否来自当前事实源代次。不要用 `<` / `>` 比较来替代身份相等校验。

## Context Manifest

`context_manifest` 是“当时喂给模型的上下文清单”。它不一定保存全文，但必须保存足够引用，让事后能回答：模型当时看到了哪些对话、文件、记忆、工具 descriptor snapshot/hash、策略快照和模型配置快照。

每次 LLM 调用记录不可变 `context_manifest`：

- `conversation_through_seq`
- `snapshot_id`
- `workspace_revision` / `commit_hash`
- `memory_document_ids` + versions
- `retrieval_chunk_ids`
- `prompt_template_snapshot_id` + `prompt_template_hash`
- `policy_snapshot_id` + `policy_hash`
- `tool_set_snapshot_id` + `tool_descriptor_hashes`
- `model_router_snapshot_id` + `provider_policy_hash`
- `model_id` + parameters + `model_config_hash`
- `agent_profile_snapshot_id` + `agent_profile_hash`
- `context_builder_version`
- `compaction_strategy_version`

所有可被管理面修改的配置都必须通过 append-only snapshot 进入 manifest，而不是只保存可变表的当前 version。回放、审计或重试时如果 snapshot 缺失、hash 不匹配或已被硬删除，系统必须 fail closed，不能用“当前最新配置”补齐历史上下文。

Replay 是从历史事件重建状态。它不重新调用 LLM、不重新执行工具。新 Worker 使用已记录的外部结果事件，从当前状态继续下一步。

## Snapshot

Snapshot 是状态快照，用来减少从头 replay 的成本。它是优化，不是事实源；损坏时可以丢弃并从事件重建。它包含：

- `through_seq`
- `projection_version`
- `event_schema_version`
- `context_builder_version`
- `payload_ref`；只有小型、非敏感、可重建投影才允许内联 `payload`
- `checksum`

checksum 不匹配、projection 不兼容、upcaster 不支持、`through_seq` 越界或 workspace revision 不存在时，系统自动丢弃 snapshot 并从事件重建。这里的 projection 是从事件推导出的当前状态，upcaster 是把旧事件格式升级到新格式的转换器。

Snapshot 写入有明确归属：AgentWorker 可在 run 终态时写；Timer / Sweeper 可按“距上一快照事件数 >= N 或时间 >= T”触发写入。写入必须条件化、幂等，并带 `through_seq`。
