# 运维：Sweeper、可观测性与故障测试

> 本文档解释系统怎么自己“收尾”：哪些过期状态要扫，哪些指标要看，哪些故障必须提前演练。

## 问题、决策与风险

**问题**：事件/命令链路主要靠“有新事件才触发下一步”。但有些状态不会自然再收到命令，例如审批超时、`outcome_unknown` 复核、泄漏的配额预留和过期租约。

**决策**：Timer / Sweeper 是生产基线能力。它不是特权写库脚本，而是一个普通 actor，通过 EventService 和同样的 CAS/幂等规则推进到期转换。

**为什么不等下一次用户请求顺便处理**：很多 conversation 长时间没有新请求。等待用户触发会让未知副作用、过期 run、配额泄漏和 runtime session 无界堆积。

**忽略后果**：run 永远卡在 waiting 状态；配额被泄漏 reservation 占满；未知副作用无人处理；snapshot 不生成导致 replay 随会话长度线性变慢。

| 应该 | 不应该 |
| --- | --- |
| Sweeper 通过 EventService 条件写 | Sweeper 直接 update 业务表 |
| 按 due table 或 delayed job 扫描 | 靠用户下一次请求顺便修复 |
| 指标使用低基数 label | 把 run_id/conversation_id 放进 metrics label |
| 故障测试围绕不变量 | 只做正常路径压测 |

## 先用白话说

Sweeper 是后台闹钟，不是管理员脚本。它定期找“已经到时间但没人推进”的事情，然后像普通 actor 一样通过 EventService 写事件。它不能绕过状态机直接改库。

## Timer / Sweeper

Sweeper 可以理解成“后台闹钟 + 清理工”。它定期找已经到期但没人处理的事项，再通过 EventService 推进合法状态。实现上可以是一张带 `due_at` 索引的到期表、独立 timer service，或复用 durable queue 的延迟投递；无论哪种实现，都必须有 lease/claim、去重、backoff 和审计。

| 巡检项 | 触发条件 | 动作（均经 EventService 条件写） |
| --- | --- | --- |
| run deadline / 挂起超时 | `waiting_tool` / `waiting_approval` 超过 `due_at` 且无有效 Worker | 追加 `RunExpired`（检查 `run_version`） |
| approval 超时 | `waiting_approval` 超过审批时限 | 按策略过期或升级提醒 |
| `outcome_unknown` 复核 | ToolCall 进入 unknown 后到 `due_at` | 调用对账逻辑，落成功/失败，仍未知则升级人工 |
| quota reservation 回收 | `reservation_id` 超 TTL 未 settle/release | 释放预留，记录审计 |
| 租约 / 旧尝试回收 | lease 过期、attempt 失去 owner | 标记为旧尝试，释放所有权，有副作用者转对账 |
| retry backoff | `available_at` 到期 | 重新入队或写 DLQ |
| snapshot 触发 | 距上一快照事件数 >= N 或时间 >= T | 条件写 snapshot |
| runtime cleanup | session idle/TTL/kill deadline 到期 | 发 `RuntimeTerminationRequested` 或强制清理 |

Sweeper 与 Repair API 互补：Sweeper 自动、有界、无需审批，只处理合法到期转换；Repair API 人工、可处理终态和裁定未知结果，需权限和双人审批。

## AI 变更发布门禁

模型、prompt、tool descriptor、agent profile、guardrail policy 和路由策略都会改变 Agent 行为，所以都按“生产变更”处理，而不是直接改配置表。

一次 AI 变更进入生产前至少要留下这些记录：

- `change_snapshot_id` 和 hash：说明这次到底改了什么。
- `eval_suite_id`：固定离线样本，覆盖成功率、工具调用准确率、格式遵循、拒答质量、幻觉率、敏感输出、成本和延迟。
- red-team 结果：覆盖 prompt injection、越权工具调用、恶意网页、敏感 payload 泄露和长上下文污染。
- 通过阈值：例如不能低于旧版本成功率、不能增加 secret/PII 泄露、成本和延迟不能超过预算。
- 风险负责人和批准事件：谁接受这次变更的剩余风险。
- 回滚计划：灰度失败时恢复哪个 snapshot、如何处理已经启动的 run、哪些指标触发回滚。

灰度期间要同时看业务指标和安全指标。只要命中回滚阈值，先恢复旧 snapshot，再分析样本；不能为了保留灰度继续扩大影响面。

## 可观测性

Metrics 只使用低基数维度。低基数的意思是“取值种类有限”，例如服务名、区域、状态；不要把每个 run id 都当成指标标签。

- `service`
- `region`
- `queue_class`
- `job_type`
- `provider`
- `model_family`
- `tool_category`
- `status`
- `error_code`
- `tenant_tier`

`conversation_id`、`run_id`、`user_id` 适合放在日志、trace、审计或 exemplar 里，不适合作为 metrics label。

覆盖范围：

- API 延迟、错误率、限流。
- 事件追加延迟、写入量、CAS 冲突率。
- outbox lag、发布失败、重复发布。
- 队列深度、任务年龄、attempts、retries、DLQ。
- Worker 成功率、失败率、lease timeout、job duration。
- LLM 延迟、超时、token、fallback、provider 错误、成本。
- Runtime cold start、活跃 session、资源用量、清理失败。
- Sweeper 到期积压、处理延迟、对账积压、未回收 reservation。
- 实时缺口恢复时间、慢连接断开、登录刷新失败。

端到端 SLO（用户能感受到的服务目标）：

- API accept latency：API 接受请求的延迟。
- queue wait：任务排队等待时间。
- time to first token：从发起到看到第一个 token 的时间。
- run completion latency：run 完成总耗时。
- interactive run success rate：交互式 run 成功率。
- realtime gap recovery time：实时断线后补齐缺口的时间。
- runtime cold-start latency：runtime 冷启动耗时。
- tool outcome-unknown rate：工具结果未知的比例。
- tenant throttle accuracy：租户限流是否准确。

## 运维控制台

运维控制台应支持：

- 查看 conversation events 与 run state。
- 查看 command、attempt、outbox、inbox、effect ledger。
- 取消 run。
- 重试失败 job，或把 DLQ 中的死信任务重新投递。
- 手工裁定 `outcome_unknown`。
- 调整租户配额。
- 查看并清理 runtime session。
- 触发主体数据删除。

所有写操作经 Repair Command API；只读可走只读副本或受限查询 API。

## 故障测试与不变量

| 故障点 | 期望行为 |
| --- | --- |
| API commit 成功但响应丢失 | 相同 idempotency key 返回原 run |
| Publisher 发出 command 后、标记 published 前崩溃 | command 重复投递，consumer inbox 去重 |
| 两个 ToolWorker 同时完成 | 两个 ToolCall 事实都保留，只有一个 Run continuation |
| ToolWorker 用旧 run_version 尝试 join | Join 在事务内重读当前 Run version；不得留下永久 `skipped` continuation |
| join 与 cancel 同时提交 | 不因 `event_cursors` 与 Run 反向锁序死锁；若 PostgreSQL 返回 `40P01`，短事务用新状态重试 |
| 工具副作用成功、Worker 写结果前崩溃 | 对账或幂等键防止盲目重做 |
| lease 过期、旧 Worker 恢复 | fence 不匹配，不能推进 Run |
| Run cancel 与 ToolCall completed 同时提交 | ToolCall 事实可保留，但 Run 不恢复执行 |
| Redis / realtime 通知丢失 | 客户端用 `last_seen_seq` 补拉 |
| 客户端消费太慢 | Gateway 断开连接，客户端重连补拉 |
| auth 到期或权限撤销 | 停止发送，重新鉴权和 ACL 检查 |
| DLQ redrive 时外部效果未知 | 先查 effect ledger，不直接重试 |
| 部署新事件 schema | 新旧 Worker 均可安全读取或 upcast |
| snapshot 损坏 | 自动丢弃 snapshot，从事件重建 |
| 按时间点恢复后存在旧 command | `store_epoch` 拒绝旧代次 command |
| 租户/主体删除 | 加密载荷不可还原，memory/snapshot/search/artifact 派生物失效 |

`store_epoch` 的持久化合约定义在 [concurrency-and-durability.md](./concurrency-and-durability.md)。运维侧需要告警：旧 epoch command 被拒绝、按时间点恢复后 epoch 未递增、publisher 发布非当前 epoch outbox row。
