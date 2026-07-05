# Agent Execution Kernel

本文定义当前代码库中“Cloud Agent 执行内核”的边界。它服务于大纲生成、课程正文生成、Review、任务生成等业务 Agent，但不包含这些业务自己的字段、prompt 或落库规则。

## 目标

Agent 任务和普通后台任务的区别是：它通常会调用 LLM 或工具，花钱、耗时、可能重复、可能超时，也可能在外部副作用已经发生后崩溃。因此执行内核必须统一处理这些非业务不变量：

- run 生命周期只能按状态机推进。
- job lease 过期后，旧 worker 不能覆盖新结果。
- worker 成功写事件和 ack 当前 job 必须原子提交。
- API 幂等和 worker fence 不能散落在业务 handler 里。
- LLM 调用必须有 attempt key、prompt version 和 context manifest。
- 业务输出校验失败时，run 要收敛到明确失败；执行基础设施失败时，job 要可重试。

## 边界

内核负责：

- `agent_runs`：执行实例的状态、版本和错误投影。
- `agent_jobs`：异步执行队列、lease、fence、attempt、fail/dead。
- `agent_events` / `EventService.Append`：事件追加、per-user seq、幂等、Run CAS、JobFence ack。
- `agent_idempotency_keys`：API 写入口的请求重放响应和服务端 `command_id` 映射。
- `agent_llm_ledger`：LLM 调用前置记账和 `pending/ok/provider_error/unknown` 语义。
- worker runtime：claim job -> allocate attempt -> build LLM request -> call LLM -> append terminal events -> ack/fail job。

内核不负责：

- 学习大纲字段。
- 课程正文结构。
- Review 输出结构。
- 用户画像语义。
- prompt 文案。
- 业务 validator。
- `content_artifacts`、未来 `learning_outlines`、`learning_tasks` 等业务事实表。

换句话说，大纲、正文、Review 复用的是“可靠执行能力”，不是同一张输出表，也不是同一份 prompt。

## 当前代码结构

```text
backend/internal/agentcore
  Worker                  # 通用执行骨架
  Handler                 # 业务插件点
  Queue / EventAppender   # 内核依赖的最小接口

backend/internal/event
  Service.Append          # 原子 append、幂等、Run CAS、JobFence ack

backend/internal/job
  Queue                   # PostgreSQL lease queue

backend/internal/run
  Reducer                 # Run 状态机投影
  Event helpers           # RunAccepted / RunQueued / RunStarted / RunSucceeded / RunFailed / RunExpired payload 构造

backend/internal/llm
  Client / ledger         # LLM 调用与账本

backend/internal/content
  contentGenerationHandler # 课程正文生成业务 handler

backend/internal/outline
  outlineGenerationHandler # 学习大纲生成业务 handler
```

`content_generation` 和 `outline_generation` 现在都运行在 `agentcore.Worker` 上。后续 `review_generation` 也应该实现 handler，而不是复制 worker 生命周期代码。

## Handler 合约

业务 handler 只需要实现三件事：

```go
type Handler interface {
    JobKinds() []string
    BuildLLMRequest(ctx context.Context, job JobContext) (llm.Request, error)
    BuildAppendRequest(ctx context.Context, completion Completion) (CompletionAppend, error)
}
```

含义：

- `JobKinds`：声明这个 handler 消费哪些 job kind。
- `BuildLLMRequest`：解析业务 payload，构建 LLM 请求。这里可以读业务输入，但不应该改数据库。
- `BuildAppendRequest`：把 LLM 成功/失败转成业务事件；必要时做结构化输出解析、校验和业务事实落库。

`CompletionAppend` 只暴露内核需要知道的内容：

```text
- user_id
- run_id
- expected_run_version
- events[]
```

内核会补上：

```text
- Actor{worker}
- JobFence{job_id, lease_token}
- RunAggregate{run_id, expected_version}
```

这样业务 handler 不需要直接碰 worker fence，也不负责自己 ack job。

## Worker 生命周期

```mermaid
sequenceDiagram
  autonumber
  participant W as agentcore.Worker
  participant Q as job.Queue
  participant H as business Handler
  participant L as LLM Client
  participant E as EventService

  W->>Q: Claim(job kinds, worker_id)
  Q-->>W: job + fence
  W->>W: allocate attempt_key
  W->>H: BuildLLMRequest(job, attempt_key)
  H-->>W: llm.Request
  W->>L: Complete(request)
  L-->>W: response or error
  W->>H: BuildAppendRequest(completion)
  H-->>W: user_id + run_id + events + expected_version
  W->>E: Append(events + JobFence + Run CAS)
  alt append ok
    E-->>W: job marked done in same transaction
  else stale run
    W->>E: Append(JobFence only)
    E-->>W: old job acked without changing run
  else append infrastructure error
    W->>Q: Fail(job fence, cause)
  end
```

## 错误语义

| 位置 | 例子 | 内核处理 |
| --- | --- | --- |
| Claim 没有 job | 队列为空 | 返回 `processed=false`，worker sleep |
| payload / request 构建失败 | job payload 无法解析、缺 user_id | fail 当前 job，等待重试或 dead |
| LLM 返回错误 | provider error、预算拒绝 | 交给 handler 生成 `RunFailed` 业务事件 |
| 业务输出无效 | JSON 不合法、validator 不通过 | handler 生成 `RunFailed` 业务事件 |
| 业务落库失败 | artifact 存储失败 | fail 当前 job，稍后重试 |
| Append Run CAS 冲突 | sweeper 或另一个 worker 已推进 run | fence-only ack 旧 job，不再改 run |
| Append 基础设施失败 | DB 错误、fence 无效 | fail 当前 job |

注意：LLM 错误不等于 worker 基础设施错误。前者通常应该收敛成 run 失败事件；后者应该让 job 可重试。

## 数据库命名

当前 migration 已按执行内核边界收敛到 `agent_*` 前缀：

```text
agent_runs
agent_events
agent_event_cursors
agent_jobs
agent_idempotency_keys
agent_llm_ledger

learning_missions
learning_tasks
learning_outlines
content_artifacts
review_results
user_profiles
```

关键原则：业务里的 task 是学习任务；内核里的 job 是执行队列任务。二者不能混用。代码包名仍保留 `event` / `job` / `run` / `llm` 这些领域名，`agent_*` 只表达数据库物理表属于执行内核。

## 复用方式

后续业务 Agent 应该这样接入：

| Handler | 输入 | 输出 | 业务落库 |
| --- | --- | --- | --- |
| `outline_generation` | 用户信息 + 用户要求 | LearningOutline + TaskSpecs | `learning_outlines` / `learning_tasks` |
| `content_generation` | TaskSpec | ContentArtifact | `content_artifacts` |
| `review_generation` | Evidence + Task + ExerciseResult | ReviewResult + profile delta | `review_results` / profile projection |

它们共用同一个 `agentcore.Worker`，但各自拥有 schema、prompt、validator 和业务事实表。

## 当前验证

当前代码里有三层测试：

- `backend/internal/agentcore/worker_test.go`：不依赖数据库的单元测试，覆盖 claim、fail、append、stale ack 和 append 失败语义。
- `backend/internal/content/service_test.go`：内容生成 worker 的集成测试，覆盖 run/job/artifact/event 的数据库链路。
- `backend/internal/outline/service_test.go`：大纲生成 worker 的集成测试，覆盖第二个业务 handler 的 run/job/outline/tasks 数据库链路。
- `backend/internal/api/content_generation_smoke_test.go`：API 端到端 smoke，覆盖 `POST /api/content-generation-runs -> worker.ProcessOne -> GET run -> GET artifact`。
- `backend/internal/api/outline_generation_smoke_test.go`：API 端到端 smoke，覆盖 `POST /api/outline-generation-runs -> worker.ProcessOne -> GET run -> GET outline`。

这些测试证明当前 `content_generation` 与 `outline_generation` 已经运行在同一内核骨架上。目标不是证明所有未来业务都完成，而是证明大纲和正文两类业务已经不再拥有通用 worker 生命周期代码。

## 后续抽离顺序

1. 为 handler 增加非 LLM 执行模式，支持纯工具或无需模型的 agent job。
2. 将 worker 指标标准化：claimed、completed、failed、stale_acked、append_failed、llm_error。
3. 实现 `review_generation` handler，继续验证内核覆盖不同业务输出。
