# 运维：Sweeper、可观测性与故障测试

> 本文档是 [architecture.md](./architecture.md) 的子文档，定义后台巡检、监控体系与故障验证矩阵。

## Timer / Sweeper 与后台巡检

系统的主链路是事件 / 命令驱动（反应式），但若干不变量**只能靠周期性主动扫描维持**——它们没有"下一条命令"自然触发。因此 Lite 首版就需要一个 **Timer / Sweeper** 后台进程（实现上是一张带 `due_at` 索引的到期表 + `SELECT ... FOR UPDATE SKIP LOCKED` 扫描，或复用 job 队列的延迟投递），统一驱动：

| 巡检项 | 触发条件 | 动作（均经 Event Service 条件写） |
| --- | --- | --- |
| run deadline / 挂起超时 | `waiting_tool` / `waiting_approval` 超过 `due_at` 且无 worker 在跑 | 追加 `expired`（`run_version` CAS） |
| approval 超时 | `waiting_approval` 超过审批时限 | 按策略 `expired` 或升级提醒 |
| `outcome_unknown` 复核 | tool_call 进入 `outcome_unknown` 后到 `due_at` | 调用 reconcile，落 `succeeded` / `failed`，无法判定则升级人工 |
| quota reservation 回收 | `reservation_id` 超 TTL 未 settle/release | 释放预留，防止配额泄漏 |
| lease / orphan GC | lease 过期、attempt 成为 orphaned | 标记回收、释放 run 所有权，有副作用者转 reconcile |
| retry backoff | `available_at` 到期的重试 | 重新入队 |
| snapshot 触发 | 距上一快照事件数 ≥ N 或时间 ≥ T | 触发快照写入 |

**关键纪律**：Sweeper **不绕过状态机**——它和普通 worker 一样，只能通过 Event Service 以对应聚合版本（`expected_run_version` / `expected_tool_call_version`）或去重键（`effect_key` / `command_id`）做条件写，失败即放弃本轮、下轮重试。它驱动的是"到期的合法转换"，不是特权写路径，与 Repair Command API 的人工修复路径互补：Sweeper 自动、有界、无需审批；Repair 人工、可改终态、需 dual approval。`outcome_unknown` 的自动复核也由此有了归属——在引入 Sweeper 之前，这些非终态会无人认领地堆积。

## 可观测性

调试异步链路需要足够可见性，但**不要把高基数 ID 放进 metrics label**（`conversation_id`/`run_id`/`user_id` 适合日志字段、trace attribute、audit、exemplar，不适合指标标签）。

指标只用低基数维度：`service`、`region`、`queue_class`、`job_type`、`provider`、`model_family`、`tool_category`、`status`、`error_code`、`tenant_tier`。覆盖：API 延迟/错误率/限流；事件追加延迟与写入量；outbox lag 与发布失败；队列深度/任务年龄/attempts/retries/DLQ；worker 成功率/失败率/lease timeout/job duration；LLM 延迟/超时/token/fallback/provider 错误；runtime cold start/活跃 session/资源/清理失败；Timer/Sweeper 到期积压与处理延迟（outcome_unknown 待复核数、超时未触发的挂起 run、未回收的 reservation）。

定义端到端 **SLO**：API accept latency、queue wait、time to first token、run completion latency、interactive run success rate、realtime gap recovery time、runtime cold-start latency、tool outcome-unknown rate、tenant throttle accuracy。日志与 trace 携带 `tenant_id`、`user_id`、`conversation_id`、`run_id`、job id 与 `request_id`。

Admin/Ops Plane 应支持（经 Repair Command API）：查看 conversation events 与 run state、取消 run、重试失败 job、修复后 redrive DLQ、调整租户配额、查看并清理卡住的 runtime session。

## 故障测试与不变量

故障测试应围绕**不变量**，而非只做压力测试：

| 故障点 | 期望行为 |
| --- | --- |
| DB commit 成功但 API 响应丢失 | 相同 idempotency key 返回原 run |
| broker publish 成功、publisher 标记前崩溃 | 重复 command 被 inbox 去重 |
| 工具副作用成功、worker 完成事件前崩溃 | reconcile 或下游幂等，不盲目重做 |
| lease 过期、旧 worker 恢复 | fence 不匹配，不能推进 run |
| Redis 通知丢失 | 客户端按 cursor 补拉 |
| cancel（Run 级）与 ToolCall completed 同时提交 | ToolCall 完成可成功（tool_call_version CAS），但 join 推进 Run 被拒（run status 已 cancelled） |
| DLQ redrive 时外部效果未知 | 禁止直接重试，先查 effect ledger |
| 部署新事件 schema | 新旧 worker 均可安全读取（upcasting） |
| snapshot 损坏 | 从事件自动重建 |
| DB 恢复到旧时间点 | broker 中更晚的 command 不得反向污染事实源 |

最后一行的缓解机制需具体化：为 EventStore 维护单调的 **`store_epoch`（generation）**，PITR 恢复后递增 epoch；命令在 outbox 中携带其 `store_epoch`，消费端拒绝 epoch 低于当前 store 的命令（视同 stale）。这样"恢复点之后、却来自旧 epoch 的在途命令"被 fence 掉，而不会反向写入已回滚的事实源。
