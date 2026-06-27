# 部署形态与容量规划

> 本文档是 [architecture.md](./architecture.md) 的子文档，定义部署替换路径、负载向量和规模化就绪标准。

## 问题、决策与风险

**问题**：一句“支持万级”没有工程含义。在线连接数、活跃 run、并发 sandbox、事件写入、token 吞吐和热点 conversation 是完全不同的瓶颈。

**决策**：先用负载向量描述目标，再用压测和故障演练验证。Lite v1 以 PostgreSQL 为中心；只有真实瓶颈出现，并且队列、状态机、幂等这些语义已经稳定后，才替换基础设施。

**为什么不先上 Kafka/NATS/microVM 全家桶**：复杂基础设施不能自动修复状态机、幂等、未知副作用和权限边界。语义不清时，替换消息队列只会放大问题。

**忽略后果**：系统可能能撑 1 万 WebSocket，却撑不住 500 个 runtime；或者平均吞吐达标，但单个热点 conversation 被 `seq` 行锁钉死。

| 应该 | 不应该 |
| --- | --- |
| 按负载向量声明容量 | 用一个“万级”标签概括所有能力 |
| 先固定队列语义契约再换消息队列 | 把消息队列当透明替换件 |
| 把 heartbeat、snapshot、replay 算进容量 | 只测 API happy path |
| 识别热点 tenant/conversation | 只看全局平均 QPS |

## Lite v1 部署形态

架构边界不等于网络服务边界。首版建议控制在四类应用进程：

1. **Control Plane（模块化单体）**：Gateway、Conversation API、EventService、状态机、Permission、Quota、Scheduler、Timer / Sweeper、Repair API。
2. **Realtime Gateway**：SSE/WebSocket、鉴权、缓冲、cursor 补拉。
3. **Agent Worker Pool**：Agent step、Context Builder、LLM Gateway。
4. **Tool / Runtime Plane**：ToolWorker 与 RuntimeManager，独立进程和独立安全域。

基础设施保持 PostgreSQL + Redis Pub/Sub + S3/MinIO + Secrets provider + OpenTelemetry collector。PostgreSQL job queue 足以表达 Lite v1 的队列语义，并能保持“事件、投影、命令”在同一个事务里写入。

## 替换路径

| 关注点 | Lite v1 | 生产替换 |
| --- | --- | --- |
| EventStore | PostgreSQL | 分区 PostgreSQL 或分片事件存储 |
| Outbox | PostgreSQL 表 | PostgreSQL outbox + 独立 publisher fleet |
| Queue | PostgreSQL jobs (`SKIP LOCKED`) | Redis Streams、Kafka 或 NATS JetStream，按语义契约替换 |
| Scheduler | Control Plane 内 dispatcher | 独立 scheduler service |
| Timer / Sweeper | 周期扫描到期表 | 独立 timer / 对账服务 |
| Realtime | Redis Pub/Sub + SSE/WebSocket | NATS、托管 Pub/Sub 或 gateway fleet |
| Runtime | Docker 本地池 | K8s、microVM、隔离节点或 E2B |
| Workspace | 本地或挂载 volume | 隔离持久卷或对象存储型 workspace |
| Artifact Store | 本地磁盘或 MinIO | S3-compatible object store |
| Memory | PostgreSQL 表或轻量索引 | 向量数据库或托管 RAG |
| Secrets | 本地加密文件或 env-backed provider | 云 secrets manager / 受控代发服务 |
| Observability | 结构化日志 + OTel | 指标、链路追踪、告警、采样策略 |

替换要求：替换后的组件必须满足已有语义契约，包括至少一次投递、去重、延迟投递、领取超时、死信重投、取消、epoch 检查和反压。换组件不能改变业务语义。

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
| `S_hot` | 单一热点 tenant 或 conversation 的负载 |
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

## 已知瓶颈

- `seq` 分配会锁定 conversation 元数据行，同一 conversation 内追加事件天然串行。热点 conversation 无法靠增加 Worker 解决，只能减少事件数量、避免 token delta 落库、拆分会话或调整产品交互。
- 万量级活跃 run 的 heartbeat 会形成额外写压力。heartbeat 是 Worker 定期告诉系统“我还活着”的信号，需要计入 DB 写预算，或在合适阶段迁移到 Redis/lease store。
- Snapshot 不及时会导致 replay 变慢。replay 是从历史事件恢复状态；事件越长，恢复越慢。
- Realtime fanout 会受连接数、消息大小、慢连接和补拉 QPS 影响。fanout 就是一条事件要推给多少连接。
- Runtime cold start 和活跃 session 数通常与 API QPS 无关，需要独立建模。
- Artifact 大文件读写可能绕过数据库成为主瓶颈。

## 规模化就绪检查清单

在声明某个负载向量达标前，需要验证：

- 明确目标组合，例如 `C_conn=10000`、`N_runtime=500`、`R_event=2000/s`，而不是笼统“万级”。
- 事件追加吞吐和 p95/p99 延迟达标。
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
- 核心表备份、恢复演练、schema migration、event upcasting 正常。
- 按时间点恢复后，`store_epoch` 能拒绝旧 command。
- Timer / Sweeper 对超时、unknown、quota 回收和 runtime GC 有界生效。
- 数据保留/删除能失效 memory、snapshot、search index 和 artifact 派生物。
