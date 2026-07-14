# 运维：Sweeper、可观测性与故障测试

> 定位：生产边界文档。本文档解释系统怎么自己“收尾”：哪些过期状态要扫，哪些指标要看，哪些故障必须提前演练。

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
| approval 超时 | `waiting_approval` 超过审批时限 | 按策略过期或升级提醒；工具审批过期时同时取消仍为 `awaiting_approval` 的 proposed ToolCall |
| `outcome_unknown` 复核 | ToolCall 进入 unknown 后到 `due_at` | 调用对账逻辑，落成功/失败；仍未知则升级人工，最终只能显式接受为 `resolved_unknown`，不能标成 cancelled |
| quota reservation 回收 | `reservation_id` 超 TTL 未 settle/release | 释放预留，记录审计 |
| 租约 / 旧尝试回收 | lease 过期、attempt 失去 owner | 标记为旧尝试，释放所有权，有副作用者转对账 |
| cancellation barrier | Run 已请求取消但 barrier 未 settled | 终止或 fence 掉残余执行权，推进逐层传播；仅在 active attempt、ToolCall、runtime、Child Run 全部收敛后写 `RunCancelled` |
| retry backoff | `available_at` 到期 | 重新入队或写 DLQ |
| command 对账 | 聚合仍有 `pending_command_id`，但 inbox 无 completed/有效 owner | 校验 outbox payload/epoch 后重投同一 command；outbox 缺失或冲突进入 Repair |
| LLM 成本对账 | Provider attempt 为 `outcome_unknown` / usage unknown | 用 provider request id 或账单数据收敛 usage/cost，结算保守预算预留 |
| snapshot 触发 | 距上一快照事件数 >= N 或时间 >= T | 条件写 snapshot |
| projection rebuild | 新 projection version 正在重建 | 按 shard checkpoint 追平、校验 checksum；poison event 阻止对应 shard 切换 |
| runtime cleanup | session idle/TTL/kill deadline 到期 | 发 `RuntimeTerminationRequested` 或强制清理 |
| workspace prepared revision GC | prepared revision 超 TTL 且未成为可见 head | 查 EventStore effect ledger；无引用且不在对账中的才回收 |

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

## 软件供应链与生产镜像

四个 Go 进程共用一份参数化生产 `Dockerfile`，但每个镜像只包含对应静态二进制、CA 根证书、时区数据库、许可证和校验过的迁移源文件。最终层以 `scratch` 为根文件系统，固定数值 UID/GID `65532:65532`，不包含 shell、包管理器或动态链接器；入口只能是 `/lites`。构建阶段的 Go 官方镜像同时固定可读 tag 与 OCI index digest，避免 tag 被重写后静默改变构建输入。

供应链流水线分成两个权限域：

- PR 和主分支都执行 `go mod verify`、`govulncheck`、Trivy 文件系统扫描、CycloneDX 模块/逐服务 SBOM、四个镜像构建、shell-free/non-root 检查和镜像 HIGH/CRITICAL 漏洞门禁。
- 只有 `refs/heads/main` 且所有前置门禁通过后，发布 job 才获得 `packages: write` 和 `id-token: write`。它发布 `linux/amd64` 与 `linux/arm64` OCI index，生成 BuildKit `mode=max` provenance 和镜像 SBOM，再对不可变 digest 做 Cosign keyless 签名，并把证书身份严格验证为当前仓库的 `supply-chain.yml@refs/heads/main`。

所有第三方 GitHub Action 必须固定 40 位 commit SHA；版本注释只用于人工升级提示。仓库内的静态检查会拒绝 tag、branch 和短 SHA。Dockerfile frontend、Buildx、BuildKit builder、扫描器和签名工具本身也固定版本或 OCI digest，升级时必须核对上游安全公告、替换 SHA、重新生成证据，不能使用 `latest`。

镜像只按 digest 进入部署清单；`main` tag 仅用于发现，不是部署信任锚。每个发布证据至少记录 source commit、OCI digest、逐服务 CycloneDX SBOM、镜像 SBOM attestation、provenance 和已验证的签名身份。部署系统在拉取前必须按相同 issuer/identity 策略验证签名，并拒绝只有 tag、缺少 provenance 或签名身份不匹配的镜像。

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

生产基线 SLO 按自然月计算，并在 [capacity-and-scaling.md](./capacity-and-scaling.md) 的参考负载组合下验收：

| SLI | 生产目标 | 统计口径 |
| --- | --- | --- |
| API accept availability / latency | `99.95%`；p95 `<= 250ms`，p99 `<= 1s` | 已鉴权、未被合法限流的 create/cancel/approve 请求 |
| EventService append | `99.99%`；p99 `<= 200ms` | 不含调用方状态冲突；包含 event + projection + outbox 事务 |
| interactive queue wait | p95 `<= 2s`，p99 `<= 10s` | command 可执行到 Worker claim；不含审批等待 |
| time to first safe token | p95 `<= 5s`，p99 `<= 15s` | 从 RunAccepted 到第一个通过 pre-emit 检查的 token；禁用流式的高风险 Run 单独统计 |
| run deadline adherence | `>= 99%` 在声明 deadline 前进入等待态或终态 | 排除用户主动暂停；不能用无限 deadline 改善指标 |
| interactive run platform success | `>= 99.5%` | 排除用户取消、内容策略拒绝和业务工具确定失败；平台/状态机/队列错误计入 |
| realtime gap recovery | p95 `<= 2s`，p99 `<= 10s` | 重连鉴权完成到补齐 high-water mark |
| runtime cold start | p95 `<= 5s`，p99 `<= 15s` | 标准 untrusted runtime image；GPU/专用镜像单列 |
| tool outcome-unknown | 日比例 `< 0.1%`；其中 `99%` 在 15 分钟内自动收敛 | 按有副作用 ToolCall 计；`accepted_unknown` 永远单列 |
| tenant isolation / throttle | 越权放行 `0`；合法请求误限流 `< 0.1%` | 安全越权不使用误差预算，出现即事故 |

每个 SLO 都有月度 error budget 和 5 分钟、1 小时、6 小时多窗口 burn-rate 告警。预算耗尽时停止非必要 AI 配置变更和扩量，优先恢复可靠性；不能通过排除失败样本、延长 deadline 或关闭审计改善数字。

## 可用性与灾难恢复目标

系统是单区域单写，但区域内跨可用区部署。最终生产目标：

| 故障 | RPO | RTO | 恢复方式 |
| --- | ---: | ---: | --- |
| 单实例 / 单可用区故障 | `0` | `<= 5 分钟` | PostgreSQL 同步副本、无状态服务跨区、队列和对象存储区域级冗余 |
| 整个主区域不可恢复 | `<= 5 分钟` | `<= 60 分钟` | 异地连续归档 + 对象版本复制；封闭恢复并轮换 `store_epoch` |
| 外部队列数据丢失 | EventStore RPO | `<= 15 分钟` | CommandReconciler 从 pending projection、inbox、outbox 重投同一 command |
| projection / 向量索引损坏 | EventStore RPO | `<= 4 小时` | 按 version/shard 从事件重建，旧 projection 在校验通过前继续服务 |

EventStore、payload/object、workspace revision、tool/profile/policy snapshot 和外部 epoch anchor 必须属于同一份恢复清单；只恢复数据库但丢失被事件引用的内容不算恢复成功。备份每日做自动可读性验证，每月恢复到隔离环境并校验 checksum，每季度演练完整区域恢复；演练结果记录实际 RPO/RTO、缺失引用和 command 对账报告。

## 运维控制台

运维控制台应支持：

- 查看 conversation events 与 run state。
- 查看 command、attempt、outbox、inbox、effect ledger。
- 取消 run。
- 重试失败 job，或把 DLQ 中的死信任务重新投递。
- 手工处理 `outcome_unknown`；无法查明时只能记录 `resolved_unknown / accepted_unknown` 和残余风险。
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
| Resume command 已创建但 Worker 尚未领取 | Run 保持 `queued`；claim 时才创建 attempt/fence/lease 并进入 `executing` |
| lease 过期、旧 Worker 恢复 | command、attempt、精确 fence、lease token 或期限任一不匹配都不能推进 Run；更大的未签发 fence 也必须拒绝 |
| 外部队列丢失但 outbox 已 published | CommandReconciler 依据 pending id 和 inbox 重投同一 command；已完成或已有有效 owner 的 command 不重复执行 |
| LLM 第一个 Provider 超时后 fallback 成功 | 两个物理 provider attempt 都保留 request id、usage status 和成本；未知费用保守预留并对账 |
| LLM 已发送 token 后 Provider 断流 | 当前 generation 显式失败；禁止透明拼接 fallback，重新生成必须发送 replacement/reset 和新 generation |
| 危险工具批准后参数或 workspace revision 变化 | 原 approval scope 失效，不发 ExecuteToolCall，要求重新审批 |
| Workspace prepared revision 创建后、commit authorization 前崩溃 | revision 保持不可见；无合法引用且不在对账中的对象由 GC 回收 |
| `WorkspaceRevisionCommitAuthorized` 已提交、发布前崩溃 | 从 outbox 重投同一 `CommitWorkspaceRevision`，按 authorization/effect key 幂等发布 |
| Workspace revision 发布后、结果事件提交前崩溃 | 按 effect key 查询并补写 `WorkspaceRevisionCommitted`；不创建第二份 revision |
| Run cancel 与 ToolCall completed 同时提交 | ToolCall 事实可保留，但 Run 不恢复执行 |
| 取消跨越大量 Child Run | 逐层、分批 command 传播；只有 cancellation barrier 证明 attempt、ToolCall、runtime、Child Run 全部收敛后才写 RunCancelled |
| 实时通知丢失（pub/sub bus 抖动、重启或消息被丢弃） | 客户端用 `last_seen_seq` 补拉 |
| 客户端消费太慢 | Gateway 断开连接，客户端重连补拉 |
| auth 到期或权限撤销 | 停止发送，重新鉴权和 ACL 检查 |
| DLQ redrive 时外部效果未知 | 先查 effect ledger，不直接重试 |
| 部署新事件 schema | 新旧 Worker 均可安全读取或 upcast |
| projection 遇到 poison event | 受影响 aggregate/shard 进入 rebuild_blocked，禁止跳过事件或切换新 projection；其他 shard 继续服务 |
| snapshot 损坏 | 自动丢弃 snapshot，从事件重建 |
| 按时间点恢复后存在旧 command | 独立恢复控制面先轮换 epoch；任何不精确匹配当前 epoch 的 command 都被拒绝 |
| 租户/主体删除 | 加密载荷不可还原，memory/snapshot/search/artifact 派生物失效 |
| 共享 Memory 同时含多个主体 | 根据 subject/derivation lineage 重派生非目标内容或整条删除；所有存储回执齐全后才完成 erasure |

`store_epoch` 的持久化合约定义在 [concurrency-and-durability.md](./concurrency-and-durability.md)。运维侧需要告警：非当前 epoch command 被拒绝、恢复时外部 epoch 未轮换、EventStore 尚未安装新 epoch 就启动消费者、publisher 发布非当前 epoch outbox row。PITR runbook 必须按“停入口与消费者 → 外部控制面 CAS 轮换 epoch → 恢复并安装 epoch → 重建合法 command → 灰度开放消费者 → 开放入口”的顺序演练。
