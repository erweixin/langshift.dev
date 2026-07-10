# 部署形态与容量规划

> 定位：生产边界文档。本文档解释怎么谈容量：不要只说“万级”，而是拆成连接数、事件写入、活跃 run、模型并发、runtime session、token 吞吐和热点用户等具体变量。

## 问题、决策与风险

**问题**：一句“支持万级”没有工程含义。在线连接数、活跃 run、并发 sandbox、事件写入、token 吞吐和热点 tenant/user/conversation 是完全不同的瓶颈。

**决策**：先用负载向量描述目标，再用压测和故障演练验证。部署形态直接按生产基线设计：EventStore、durable queue/stream、独立 realtime、独立 worker pool、强隔离 runtime、对象存储、secret broker 和可观测性都从一开始进入容量模型。

**为什么基础设施仍要服从语义契约**：Kafka、NATS、microVM、K8s 或托管服务都不能自动修复状态机、幂等、未知副作用和权限边界。生产基础设施是基线，但每个组件都必须通过本文档的语义验收。

**忽略后果**：系统可能能撑 1 万 WebSocket，却撑不住 500 个 runtime；或者平均吞吐达标，但单个热点 user 的 `seq` cursor 行成为写入瓶颈。

| 应该 | 不应该 |
| --- | --- |
| 按负载向量声明容量 | 用一个“万级”标签概括所有能力 |
| 用语义契约验收队列、stream、runtime 和存储 | 把基础设施当成自动正确的黑盒 |
| 把 heartbeat、snapshot、replay 算进容量 | 只测 API happy path |
| 识别热点 tenant/user/conversation | 只看全局平均 QPS |

## 先用白话说

容量规划要回答的是“哪个资源先满”，不是“系统看起来能不能跑”。1 万个 WebSocket 连接、500 个 sandbox、2000 条事件写入每秒和 100 个并发 LLM 调用，是四种完全不同的压力。每个负载向量都要单独测、单独设上限、单独设计降级策略。

## 生产部署基线

架构边界不等于网络服务边界，但生产部署必须保留独立扩缩、安全域和故障隔离。推荐基线如下：

| 平面 / 服务组 | 包含内容 | 基线要求 |
| --- | --- | --- |
| Public API Plane | Gateway、Conversation API、Approval API、Cancel API | 对外认证、租户解析、限流和请求校验；不执行长任务 |
| Control Plane | EventService、状态机、Permission、Quota、Repair API | 所有状态推进都经 EventService；Repair 走审批和审计 |
| Scheduler / Queue Plane | Scheduler、durable queue/stream、outbox publisher fleet、DLQ | 支持至少一次投递、延迟投递、lease/claim、ack、retry、dead-letter、去重、`store_epoch` 精确匹配和 backpressure |
| Realtime Plane | SSE/WebSocket Gateway fleet、pub/sub bus、cursor backfill | 独立扩缩；慢消费者治理；断线后回源 EventStore 补拉 |
| Agent Worker Plane | AgentWorker、Context Builder、LLM Gateway | 按交互/后台、模型、优先级、租户 tier 拆池；受 provider 配额和预算闸门约束 |
| Tool / Runtime Plane | ToolWorker、RuntimeManager、Sandbox、Secret Broker client | 独立安全域；不可信代码使用 microVM、隔离节点或等价强边界；默认无网络和 secret |
| Data Plane | EventStore、workspace service、artifact store、memory/retrieval、secrets manager | EventStore 高可用备份；workspace 版本化；artifact/secret/memory 都按 tenant ACL 隔离 |
| Observability Plane | Metrics、logs、traces、audit、alerting、incident runbooks | 覆盖事件、队列、worker、runtime、LLM、realtime、guardrail 和 repair |

基础设施基线：PostgreSQL 或兼容事务数据库承担 EventStore 与事务内 outbox；队列使用 durable queue/stream；实时使用独立 pub/sub bus；artifact 使用 S3-compatible object store；secret 使用云 secrets manager 与 broker；runtime 使用 microVM、隔离节点或等价强隔离；全链路接入 OpenTelemetry 和审计。

## 扩展路径

| 关注点 | 生产基线 | 扩展方式 |
| --- | --- | --- |
| Control Plane | 内部服务组，共享 EventService 合约 | 按写入热点拆 API、Permission、Quota、Repair，但状态推进仍归一 |
| Agent Worker Pool | 按任务类别拆池 | 继续按模型/provider/优先级/租户 tier 分池 |
| EventStore | HA PostgreSQL / 分区表 / 事务内 outbox | 按 tenant/user/time 分区，必要时分片；保留 `seq`、CAS、outbox 语义 |
| Outbox Publisher | 独立 publisher fleet | 按 command class、tenant tier 和区域扩容 |
| Queue / Stream | Durable queue/stream | 分资源队列、优先级队列、租户公平调度和 DLQ redrive |
| Scheduler | 独立 scheduler service | 引入全局资源闸门、provider quota 感知和 runtime capacity 感知 |
| Timer / Sweeper | 独立 timer / 对账服务 | 按 due class 拆分，隔离 unknown 对账、runtime GC、quota 回收 |
| Realtime | Gateway fleet + pub/sub bus | 按区域和 tenant shard 扩展；补拉仍回源 EventStore |
| Runtime | microVM / 隔离节点 / 专用 pool | 按 trust tier、network policy、GPU/CPU、租户 tier 拆池 |
| Workspace | 版本化 workspace service | copy-on-write、分支合并、冲突审查和对象存储型 workspace |
| Artifact Store | S3-compatible object store | 生命周期策略、扫描隔离、跨区域复制和加密密钥分层 |
| Memory | tenant-scoped retrieval service | 混合检索、向量库分片、嵌入重建 pipeline |
| Secrets | Secrets manager + broker | 按租户、工具、目标资源和审批状态发放短期能力 |
| Observability | OTel + audit + alerting | SIEM/SOAR、故障注入、SLO burn-rate 告警 |

扩展或迁移组件时必须满足已有语义契约，包括至少一次投递、去重、延迟投递、领取超时、死信重投、取消、epoch 检查、backpressure、审计和 tenant 隔离。组件变化不能改变业务语义。

## 负载向量

| 变量 | 含义 |
| --- | --- |
| `C_conn` | 同时存在的 SSE/WebSocket 连接 |
| `R_api` | API 请求数/秒 |
| `R_event` | 持久事件写入数/秒 |
| `N_run` | 活跃 Agent run |
| `N_llm` | 并发模型调用 |
| `N_runtime` | 活跃 sandbox/runtime session |
| `T_token` | 每秒生成或处理的 token |
| `B_artifact` | artifact/workspace 读写带宽 |
| `D_retention` | 每日事件、日志、artifact 增量 |
| `S_hot` | 单一热点 tenant、user 或 conversation 的负载 |
| `R_heartbeat` | Worker 租约心跳的写入或检查频率 |
| `R_replay` | 从事件和 snapshot 恢复状态的读取压力 |

整体吞吐上限近似由最小瓶颈决定：

```text
min(
  DB event append throughput / events per run,
  scheduler dispatch rate,
  worker slots / average job duration,
  LLM provider quota,
  runtime slots / average session duration,
  artifact bandwidth,
  realtime fanout capacity
)
```

### 参考生产验收组合

Lites 的首个生产基线必须在同一场压测中同时承受下面的持续负载，而不是分别测出各自峰值再相加。该组合持续 30 分钟，随后承受 2 倍入口/事件突发 5 分钟；全程满足 [operations.md](./operations.md) 的 SLO，不丢事件、不重复外部副作用、不出现跨租户访问，突发结束后 outbox、queue、Sweeper backlog 在 15 分钟内回到稳态。

| 向量 | 基线值 |
| --- | ---: |
| `C_conn` | 10,000 个并发连接 |
| `R_api` | 500 请求/秒 |
| `R_event` | 2,000 持久事件/秒 |
| `N_run` | 10,000 个活跃 Run |
| `N_llm` | 200 个并发 Provider 请求 |
| `N_runtime` | 500 个活跃 sandbox session |
| `T_token` | 100,000 token/秒（输入 + 输出） |
| `B_artifact` | 500 MiB/秒聚合读写 |
| `D_retention` | 1 TiB/日新增事件、payload、artifact 与观测数据的生命周期处理能力 |
| `S_hot` | 单租户可占 20% 总容量；单 tenant-user 可持续 100 次 event append/秒而不影响其他租户 SLO |
| `R_heartbeat` | 1,000 次 lease heartbeat/秒 |
| `R_replay` | 100 个 Run/秒并发恢复或 projection shard 重放等价压力 |

这是一组最低验收值，不是容量承诺上限。生产环境按租户合同和实际峰值生成版本化 `capacity_profile`，记录硬件、分片数、Provider quota、runtime image、数据大小、压测代码版本和结果 hash；目标高于此表时必须用新的完整组合重新验收，不能只线性外推。

## 需要提前知道的瓶颈

- `seq` 分配会锁定 `event_cursors(tenant_id, user_id)` 行，同一租户内同一用户的追加事件天然串行。热点 user 或其下单个 conversation 的高频写入会推高该 cursor 的压力；解决手段是减少事件数量、避免 token delta 落库、拆分高频交互或在确认需要后调整 `seq` 作用域。
- 万量级活跃 run 的 heartbeat 会形成额外写压力。生产基线仍以 EventStore 中的 attempt/fence/lease token/expiry 为权威，结果提交必须在同一事务读取它们；不能把 heartbeat 单独迁到 Redis 后仍用过期的数据库 lease 判断提交。优化手段是按预计任务时长设置租约、限制最小 heartbeat 间隔、批量更新独立 lease 分区表和减少空闲 session，而不是引入两个互相矛盾的租约事实源。
- Snapshot 不及时会导致 replay 变慢。replay 是从历史事件恢复状态；事件越长，恢复越慢。
- Realtime fanout 会受连接数、消息大小、慢连接和补拉 QPS 影响。fanout 就是一条事件要推给多少连接。
- Runtime cold start 和活跃 session 数通常与 API QPS 无关，需要独立建模。
- Artifact 大文件读写可能绕过数据库成为主瓶颈。
- `outcome_unknown` 会阻塞 `all` join，直到对账收敛为成功、失败，或人工显式接受为 `resolved_unknown`。交互式工具应把 `reconcile_after` 设得足够短，并在容量规划里单独观察 unknown backlog、accepted-unknown 数量和 join wait time。

## 规模化就绪检查清单

在声明某个负载向量达标前，需要验证：

- 使用版本化 `capacity_profile`，至少达到上述参考生产组合，而不是笼统“万级”。
- 事件追加吞吐和 p95/p99 延迟满足量化 SLO。
- Worker 或队列故障期间，outbox lag 保持有界。
- 重复投递下 inbox 去重生效。
- 并行 ToolCall join 只产生一个 Run continuation。
- 租户公平按稀缺资源生效，单租户不能耗尽 interactive 容量。
- Replay 使用 checksum snapshot，不随会话长度线性恶化。
- LLM timeout、provider 故障和 fallback 行为可追踪成本。
- Runtime cold start 和 session 上限满足交互式延迟目标。
- Sandbox 隔离防止跨租户文件、网络和 secret 泄漏。
- Realtime reconnect 基于 `last_seen_seq` 补回缺失事件。
- Slow consumer、auth 到期、权限撤销不会把 realtime 当事实源。
- Admin repair 经 Repair Command API，不破坏不变量。
- 核心表备份、量化 RPO/RTO 恢复演练、schema migration、event upcasting、双 projection 切换和 poison-event 隔离正常。
- 按时间点恢复时，独立恢复控制面先轮换 `store_epoch`；消费者能拒绝所有不精确匹配当前 epoch 的 command。
- Timer / Sweeper 对超时、unknown、quota 回收和 runtime GC 有界生效。
- 数据保留/删除能失效 memory、snapshot、search index 和 artifact 派生物。
