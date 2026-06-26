# 部署形态与容量规划

> 本文档是 [architecture.md](./architecture.md) 的子文档，定义部署拓扑、替换路径与万级就绪标准。

## Lite 首版部署形态

架构边界不等于网络服务边界。首版控制在四类应用进程，避免变成"小规模微服务大杂烩"：

1. **Control Plane（模块化单体）**：Gateway、Conversation API、Event append、run 状态机、Permission、Quota reservation、Scheduler、Timer / Sweeper、Repair API。
2. **Realtime Gateway**：SSE/WebSocket、鉴权、缓冲与 cursor 补拉（极早期可与 Control Plane 同进程）。
3. **Agent Worker Pool**：Agent step、Context Builder、LLM Gateway。
4. **Tool / Runtime Plane**：ToolWorker 与 RuntimeManager 独立进程/独立安全域（更高权限与代码执行能力）。

基础设施保持 PostgreSQL + Redis Pub/Sub + S3/MinIO + Secrets provider + OpenTelemetry collector；首版不引入 Kafka/NATS，PostgreSQL job queue 足以暴露全部关键语义并保证 event/projection/command 的事务一致性。

## Lite 到生产的替换路径

| 关注点 | Lite 实现 | 生产替换 |
| --- | --- | --- |
| EventStore | PostgreSQL | 分区 PostgreSQL 或分片事件存储 |
| Outbox（发布） | PostgreSQL 表 | PostgreSQL outbox + 独立 publisher fleet |
| Queue | PostgreSQL jobs（SKIP LOCKED） | Redis Stream → Kafka 或 NATS JetStream（按语义契约） |
| Scheduler | 进程内 dispatcher | 独立 scheduler service |
| Timer / Sweeper | 进程内周期扫描（PG `due_at` + SKIP LOCKED） | 独立 timer / reconcile service 或 durable timer |
| Realtime | Redis PubSub + SSE/WebSocket | NATS 或托管 pub/sub + gateway fleet |
| Runtime | Docker 本地池 | K8s、microVM（Firecracker）或 E2B，按信任等级 |
| Workspace | 本地或挂载 volume（单写者） | 隔离持久卷或对象存储型 workspace |
| Artifact Store | 本地磁盘或 MinIO | S3-compatible object store |
| Memory | PostgreSQL 表（可重建投影） | 向量数据库或托管 RAG 服务 |
| Secrets | 本地加密文件或 env-backed provider | 云 secrets manager / broker |
| Observability | 结构化日志 | OpenTelemetry、低基数指标、链路追踪、告警 |

## 万级负载向量

架构图本身无法证明万级，必须先确定工作负载模型。至少区分：

| 变量 | 含义 |
| --- | --- |
| `C_conn` | 同时存在的 SSE/WebSocket 连接 |
| `R_api` | API 请求数/秒 |
| `R_event` | 持久事件写入数/秒 |
| `N_run` | 活跃 Agent run |
| `N_llm` | 并发模型调用 |
| `N_runtime` | 活跃 sandbox |
| `T_token` | 每秒生成/处理的 token |
| `B_artifact` | artifact 读写带宽 |
| `D_retention` | 每日事件/日志/artifact 增量 |
| `S_hot` | 单一热点 tenant/conversation 的负载 |

"1 万在线 WebSocket"与"1 万同时运行的 sandbox"几乎是两个不同产品。整体吞吐上限近似：

```text
min(
  DB 可承受事件写入 / 每 run 事件数,
  Scheduler dispatch rate,
  Worker slots / 平均执行时间,
  LLM provider quota,
  Runtime slots / 平均 session 时间,
  Artifact bandwidth
)
```

事件 append 的单写主吞吐是头号扩展瓶颈，分区主要缓解存储与查询，需在 append 路径预留未来分片/换 broker 的接口。

两个常被低估的次级瓶颈需在容量模型里显式计入：其一，`seq` 分配靠锁定 conversation 元数据行，**同一会话内的 append 因此天然全串行**——这是正确性所需，但也意味着 `S_hot`（单一热点会话）的写吞吐被这把行锁钉死，加并行度救不了单会话热点，只能靠减少每步事件数（如 `assistant.delta` 不落库）缓解。其二，万级并发 run 的 **lease heartbeat 写入会叠加到 append 写路径**（heartbeat 频率 × 活跃 run 数），可能与 `R_event` 争抢同一瓶颈；必要时把 lease / heartbeat 移到 Redis，或将其计入 DB 写预算。

## 万级就绪检查清单

在宣称达到万级能力之前，需要用压测和故障测试验证：

- 在预期租户组合与明确负载向量下，事件追加吞吐和 p95 延迟达标。
- worker 或队列故障期间，outbox lag 保持有界。
- 队列重试和 DLQ 在重复投递下行为正确（inbox 去重生效）。
- 租户公平性按稀缺资源（而非 job 数）防止单个租户耗尽 worker 容量。
- conversation replay 使用带 checksum 的 snapshot，不随会话长度线性恶化。
- LLM 超时、provider 故障和 fallback 行为有明确边界，且每次调用可追踪成本。
- runtime cold start 和活跃 session 数满足交互式延迟目标。
- sandbox 隔离按信任等级防止跨租户文件、网络和 secret 泄漏。
- realtime reconnect 通过无竞态协议基于 `last_seen_seq` 补回缺失事件。
- Admin repair 经 Repair Command API 重试/取消/dead-letter，且不破坏不变量。
- 核心表的备份、恢复演练、schema migration 与事件 upcasting 可以正常工作。
- Timer / Sweeper 在挂起 run 超时、outcome_unknown 复核、quota 回收上有界生效，无堆积。
- 数据保留/删除（crypto-shredding 与投影失效）在租户注销与被遗忘权场景下可验证。
