# LLM Provider 抽象：模型接入、路由、降级与成本

> 定位：能力专题。本文档解释模型怎么接入平台：AgentWorker 不直接绑死某个 Provider，而是通过 LLM Gateway 统一调用、路由、降级、限流和记录成本。LLM 调用在 Worker 中的位置见 [execution-model.md](./execution-model.md)，token 流式推送见 [realtime.md](./realtime.md)。

## 问题、决策与风险

**问题**：Agent 平台不可能只用一个模型。不同任务需要不同能力的模型（推理 vs 快速回复 vs 代码生成）；同一模型可能由多家 Provider 提供（OpenAI 直连 vs Azure OpenAI vs 自部署）；Provider 会限流、宕机、涨价或弃用模型版本。如果 AgentWorker 直接硬编码某个 Provider 的 SDK 调用，一旦 Provider 故障或模型下线，整个系统就停摆。

**决策**：在 AgentWorker 和 Provider 之间引入 LLM Gateway 抽象层。它提供统一的调用接口、Provider 适配、模型路由、降级切换、速率保护和成本记录。AgentWorker 只说"我要用这个能力等级的模型处理这段上下文"，由 Gateway 决定具体调用哪个 Provider 的哪个模型。

**为什么不让 AgentWorker 直接调用 Provider SDK**：Provider SDK 各自有不同的请求格式、流式协议、错误码、重试语义和计费方式。把这些差异散落在 AgentWorker 里，会导致——切换 Provider 要改业务代码；Provider 限流逻辑和业务重试逻辑混在一起；成本统计分散在多处无法对账。

**忽略后果**：Provider 故障时没有自动降级，所有 run 同时失败；Provider 限流被当成业务错误反复重试，放大故障；不同 Provider 的 token 计费口径不同但用同一个数字统计，成本报表不准；模型版本悄悄更新导致 Agent 行为回归但无法复现。

| 应该 | 不应该 |
| --- | --- |
| 通过统一接口调用，AgentWorker 不感知 Provider 差异 | 在业务代码里直接 `import openai` |
| 模型路由和降级由 Gateway 集中管理 | 每个 AgentWorker 自行判断用哪个 Provider |
| 每次调用记录 `attempt_key`、Provider、模型版本和成本 | 只记录"调用成功"，不记录是哪个 Provider 响应的 |
| Provider 限流和业务重试分层处理 | 把 429 当成普通错误走同一个 retry 队列 |

## 先用白话说

LLM Gateway 像一个“模型调度台”：

- AgentWorker 说清楚自己需要什么能力，例如强推理、快速回复、代码生成或 embedding。
- Gateway 根据租户策略、模型能力、Provider 健康度、速率限制和预算，选择具体 Provider 和模型。
- Provider 出问题时，Gateway 按规则尝试同模型换 Provider，或在允许时换到替代模型。
- 每次调用都写下 `attempt_key`、实际 Provider、实际模型、token、成本和 fallback 事实。

这样业务代码不用到处写 Provider SDK，也能在模型升级、限流、宕机和涨价时集中处理。

## LLM Gateway 在架构中的位置

LLM Gateway 是 AgentWorker 与 Provider 之间的生产基线逻辑层。它可以与 AgentWorker 同进程部署，也可以作为独立 proxy 服务，但接口、路由、限流、成本、审计和 fallback 语义必须独立定义。

```text
AgentWorker
  │
  ├─ Context Builder：组装 prompt（见 execution-model.md）
  │
  ├─ LLM Gateway（本文档的重点）
  │    ├─ 统一调用接口
  │    ├─ Provider Registry（Provider 注册表）
  │    ├─ Model Router（模型路由）
  │    ├─ Rate Limiter（Provider 级速率保护）
  │    ├─ Provider Adapter（协议适配）
  │    └─ Cost Tracker（成本记录）
  │
  └─ 把 LLM 响应写入 EventStore
```

Gateway 不做业务决策（要不要调工具、要不要继续 run），只负责"把请求安全地送到 Provider 并把响应拿回来"。业务逻辑仍在 AgentWorker 中。

## 统一调用接口

AgentWorker 通过一个与 Provider 无关的接口发起 LLM 调用。接口既支持完整响应，也支持流式输出：

```text
llm_request
  # ── 调用身份 ──
  - attempt_key                     # 本次调用的唯一标识（由 AgentWorker 生成）
  - run_id                             # 所属 run
  - tenant_id                          # 租户隔离

  # ── 模型选择 ──
  - model_ref                          # 逻辑模型引用，例如 "reasoning-high"、"fast-chat"
                                       # 也可以是具体 model_id，例如 "provider-reasoning-vYYYYMMDD"
  - model_constraints                  # 可选的硬性约束
    - min_context_window               # 最小上下文窗口
    - required_capabilities[]          # 例如 ["tool_use", "vision"]
    - excluded_providers[]             # 排除特定 Provider（例如合规原因）

  # ── 请求内容 ──
  - messages[]                         # 对话消息（统一格式，非 Provider 原生格式）
  - tools[]                            # 工具定义（统一 JSON Schema 格式）
  - system_prompt                      # system message
  - parameters                         # temperature、max_tokens、top_p 等
  - stream: bool                       # 是否流式输出

  # ── 合规与数据处理 ──
  - data_classification                # public | internal | confidential | restricted | regulated
  - residency_region                   # 数据驻留要求，例如 us、eu、cn、tenant_home
  - retention_policy                   # zero_retention | standard | tenant_custom
  - training_usage_allowed: bool       # Provider 是否可将请求用于训练或改进服务
  - allowed_provider_contracts[]       # 允许的数据处理合同 / DPA / BAA / enterprise profile
  - requires_zero_retention: bool      # 是否强制零保留或等价企业模式

  # ── 预算 ──
  - max_input_tokens                   # 输入 token 上限（超过则拒绝发送）
  - max_output_tokens                  # 输出 token 上限
  - cost_budget_remaining              # 该 run 剩余成本预算

  # ── 超时 ──
  - timeout                            # 单次调用超时
  - stream_idle_timeout                # 流式模式下无新 token 的超时
```

```text
llm_response
  # ── 调用结果 ──
  - attempt_key
  - status: completed | failed | timeout | rate_limited | cancelled
  - finish_reason: stop | tool_use | max_tokens | content_filter

  # ── 响应内容 ──
  - content                            # 文本响应
  - tool_calls[]                       # 工具调用请求（统一格式）
  - refusal                            # 模型拒绝原因（如果有）

  # ── 实际调用信息 ──
  - actual_provider                    # 实际使用的 Provider
  - actual_model_id                    # 实际使用的模型 ID（包含版本）
  - provider_request_id                # Provider 返回的请求 ID（用于对账）

  # ── 用量 ──
  - usage
    - input_tokens
    - output_tokens
    - total_tokens
    - input_cost                       # 按 Provider 费率计算的输入成本
    - output_cost                      # 输出成本
    - total_cost

  # ── 性能 ──
  - latency_ms                         # 总耗时
  - time_to_first_token_ms             # 流式模式下首 token 延迟
  - tokens_per_second                  # 输出速率

  # ── 路由信息 ──
  - route_decision                     # 路由决策记录
    - model_ref_resolved_to            # 逻辑引用解析成的具体模型
    - provider_selection_reason        # 为什么选了这个 Provider
    - fallback_attempted: bool         # 是否触发了降级
    - fallback_from                    # 从哪个 Provider 降级过来的
```

### 与 `context_manifest` 的关系

`context_manifest` 在 LLM 调用前生成，记录这次调用准备喂给模型的输入清单和解析后的调用配置：

- 逻辑模型引用解析后的目标模型或候选模型。
- 本次调用使用的模型参数、工具集快照、policy 快照、router 快照和上下文来源。
- `attempt_key` 作为引用，关联到具体的 `llm_attempts` 记录。

调用完成后的 Provider、实际模型、usage、cost、fallback、错误和响应摘要写入 `llm_attempts`，不反向改写 `context_manifest`。这样事后既能回答"模型当时看到了什么"，也能回答"实际由哪个 Provider 和模型完成调用"。

## Provider Registry

Provider Registry 管理所有可用的 LLM Provider 及其模型。它是配置驱动的，不需要改代码就能增删 Provider。

```text
provider_config
  - provider_id                        # 例如 "anthropic-direct"、"azure-openai-eastus"
  - provider_type                      # anthropic | openai | azure_openai | bedrock | self_hosted
  - display_name                       # 管理界面显示名
  - status: active | degraded | disabled
  - endpoint                           # API 地址
  - auth_method                        # api_key | oauth | iam_role
  - credential_ref                     # 指向 Secrets Manager 中的凭证
  - region                             # 部署区域（用于延迟和合规）
  - contract_profile_id                # 数据处理合同 / 企业协议配置
  - data_residency_regions[]           # Provider 可承诺的数据处理区域
  - retention_modes[]                  # 支持的保留模式，例如 zero_retention / standard
  - training_usage                     # never | opt_in | provider_default

  # ── 能力 ──
  - supported_models[]                 # 该 Provider 提供的模型列表
  - supported_capabilities[]           # tool_use, vision, streaming, json_mode 等

  # ── 限制 ──
  - rate_limits
    - requests_per_minute
    - tokens_per_minute
    - concurrent_requests
  - max_context_window                 # 该 Provider 支持的最大上下文窗口
  - max_output_tokens

  # ── 费率 ──
  - pricing
    - input_cost_per_million_tokens
    - output_cost_per_million_tokens
    - currency
    - effective_from                   # 费率生效日期（费率会变）
```

```text
model_config
  - model_id                           # 例如 "provider-reasoning-vYYYYMMDD"、"provider-fast-vYYYYMMDD"
  - model_family                       # 低基数分类：reasoning、fast-chat、embedding（用于 metrics label）
  - context_window                     # 上下文窗口大小
  - capabilities[]                     # tool_use, vision, streaming, json_mode, reasoning
  - default_parameters                 # 该模型的默认 temperature、top_p 等
  - status: active | deprecated | sunset
  - sunset_date                        # 模型下线日期（Provider 公布的）
  - successor_model_id                 # 下线后推荐的替代模型
```

### 模型别名（Logical Model Ref）

AgentWorker 不直接指定某个具体模型版本，而是使用逻辑引用。逻辑引用描述的是"我需要什么能力"，由 Model Router 解析成具体模型：

```text
logical_model_refs（示例）
  "reasoning-high"    → 强推理能力，用于复杂规划和多步决策
  "reasoning-medium"  → 中等推理，用于一般对话和工具选择
  "fast-chat"         → 快速响应，用于简单问答和确认
  "code-generation"   → 代码生成优化
  "embedding"         → 文本嵌入（用于 Memory 检索，见 memory.md）
```

好处：
- 切换底层模型不需要改业务代码——只改 Router 配置。
- 新模型上线时可以先灰度给部分租户，再全量切换。
- 不同租户可以有不同的模型映射（例如企业租户用自部署模型）。

## Model Router

Model Router 负责把 `llm_request` 路由到具体的 Provider + Model。路由不是随机的，而是基于一组规则：

### 路由决策流程

```text
收到 llm_request
  │
  ▼
1. 解析 model_ref
   - 如果是具体 model_id → 直接使用
   - 如果是逻辑引用 → 查询 model_ref 映射表
   │
  ▼
2. 筛选候选 Provider
   - 状态为 active 或 degraded（degraded 时降低优先级）
   - 支持目标模型
   - 满足 model_constraints（上下文窗口、能力、排除列表）
   - 未被租户策略禁用
   - 满足 data_classification、residency_region、retention_policy、
     training_usage_allowed 和 allowed_provider_contracts
   │
  ▼
3. 排序与选择
   优先级从高到低：
   a. 租户指定的首选 Provider（如果有）
   b. 当前速率余量最充裕的 Provider
   c. 延迟最低的 Provider（基于近期 p50）
   d. 成本最低的 Provider
   规则可配置，不同 model_ref 可以有不同的排序策略。
   │
  ▼
4. 预算检查
   - 按目标模型费率估算本次调用成本
   - 如果超过 run 剩余预算 → 拒绝调用，返回 budget_exceeded
   │
  ▼
5. 速率检查
   - 检查选中 Provider 的当前速率窗口
   - 如果已达限 → 尝试下一个候选 Provider
   - 所有候选都达限 → 返回 rate_limited，由 AgentWorker 决定等待或失败
   │
  ▼
6. 记录路由决策
   - route_decision 写入 llm_response，最终进入 EventStore
```

### 租户级模型策略

不同租户可能有不同的模型访问权限和偏好：

```text
tenant_model_policy
  - tenant_id
  - allowed_model_refs[]               # 该租户可用的逻辑引用
  - allowed_providers[]                # 该租户可用的 Provider（合规限制）
  - allowed_regions[]                  # 该租户允许的数据处理区域
  - data_classification_rules          # 不同数据分类允许的模型、Provider、stream 和保留策略
  - retention_policy                   # 默认保留策略 / 零保留要求
  - training_usage_allowed: bool       # 租户级训练使用开关
  - provider_contract_allowlist[]      # 允许使用的 Provider 合同或企业 profile
  - fallback_policy                    # 同模型 / 跨模型 fallback 的合规边界和最大尝试
  - preferred_provider                 # 首选 Provider
  - model_ref_overrides                # 覆盖默认的逻辑引用映射
    例如：{ "reasoning-high": "my-self-hosted-llama-70b" }
  - cost_tier                          # 成本等级，影响模型选择
  - max_cost_per_run                   # 单次 run 成本上限
  - max_cost_per_day                   # 日成本上限
```

## Provider Adapter

每个 Provider 有自己的 API 格式、认证方式和错误码。Provider Adapter 负责把统一格式翻译成 Provider 原生格式，再把响应翻译回来。

### Adapter 职责

```text
Provider Adapter
  ├─ 请求转换：统一 messages[] → Provider 原生格式
  │    例如：Anthropic 用 content blocks，OpenAI 用 messages array
  ├─ 工具格式转换：统一 tools[] → Provider 的 function calling 格式
  ├─ 认证注入：从 Secrets Manager 获取凭证，注入请求头
  ├─ 流式协议适配：Provider SSE → 统一 token delta 事件
  ├─ 响应转换：Provider 原生响应 → 统一 llm_response
  ├─ 错误码映射：Provider 错误 → 统一错误分类
  └─ 用量提取：从响应中提取 token 用量（各 Provider 格式不同）
```

### 错误分类

Provider 返回的错误种类繁多，Adapter 将它们映射到统一分类，供 Gateway 决定如何处理：

| 统一分类 | 含义 | 典型 Provider 错误 | Gateway 行为 |
| --- | --- | --- | --- |
| `rate_limited` | Provider 限流 | 429、Retry-After | 尝试其他 Provider 或等待 |
| `context_too_long` | 输入超过模型上下文窗口 | 400 + context length | 返回给 AgentWorker，由 Context Builder 压缩后重试 |
| `content_filtered` | 内容安全过滤 | 400 + content filter | 返回给 AgentWorker，记录审计 |
| `model_unavailable` | 模型不可用 | 404、503 | 尝试其他 Provider 或降级模型 |
| `auth_failed` | 认证失败 | 401、403 | 告警，不重试，标记 Provider degraded |
| `provider_error` | Provider 内部错误 | 500、502 | 重试一次，仍失败则尝试其他 Provider |
| `timeout` | 响应超时 | 连接超时、读超时 | 尝试其他 Provider，注意不能确认 Provider 是否已消费 token |
| `invalid_request` | 请求格式错误 | 400 + validation error | 不重试，返回给 AgentWorker 修正 |

Gateway 与 Run 状态机的边界：

- `rate_limited` 和可等待的容量不足优先在 Gateway / Scheduler 层等待或换 Provider，不直接写 `RunFailed`。
- `provider_error`、`model_unavailable` 和 `timeout` 先按 fallback chain 尝试候选；候选耗尽后才把终局错误返回 AgentWorker。
- `budget_exceeded` 表示本次调用在准入阶段被拒绝。AgentWorker 按 Run 的预算策略收敛：达到 `max_cost` 时写 `RunExpired`，不是 provider 失败。
- `invalid_request`、不可恢复的 `content_filtered` 或 fallback 耗尽后的终局错误，才由 AgentWorker 追加 `RunFailed`。

### 新增 Provider

新增一个 Provider 需要：

1. 实现该 Provider 的 Adapter（请求/响应/流式/错误映射）。
2. 在 Provider Registry 中注册配置。
3. 在 Secrets Manager 中配置凭证。
4. 配置费率。
5. 可选：在 Model Router 中更新逻辑引用映射。

不需要修改 AgentWorker 或业务逻辑。

## 降级与 Fallback

Provider 可能限流、宕机或响应变慢。Gateway 提供两层降级：

### 第一层：同模型跨 Provider

同一模型可以由多个 Provider 或多条接入通道提供。主 Provider 不可用时，自动切换到备选 Provider。

```text
fallback_chain（示例）
  model: provider-reasoning-vYYYYMMDD
  providers:
    1. provider-direct-region-a      (primary)
    2. provider-hosted-region-b      (fallback)
```

### 第二层：降级到替代模型

目标模型所有 Provider 都不可用时，可以降级到能力相近的替代模型：

```text
model_fallback（示例）
  "reasoning-high":
    1. provider-reasoning-vYYYYMMDD          (primary)
    2. provider-reasoning-alt-vYYYYMMDD      (model fallback)

  "fast-chat":
    1. provider-fast-vYYYYMMDD               (primary)
    2. provider-fast-alt-vYYYYMMDD           (model fallback)
```

### 降级规则

- **同模型跨 Provider**：只在同一合规 envelope 内自动触发，对 AgentWorker 透明。每个候选 Provider 都必须重新校验数据分类、驻留区域、保留模式、训练使用和合同 profile；不满足时跳过，而不是静默越界。
- **跨模型降级**：需要租户策略允许，且替代模型必须满足同一合规 envelope。有些场景不能降级（例如需要特定模型能力或特定合规承诺），此时返回 `model_unavailable` 或 `rate_limited` 让 AgentWorker 决定。
- **没有合规候选就失败**：如果所有 fallback 候选都因 residency、retention、training usage 或合同不满足而被排除，Gateway 必须返回终局错误，不能为了可用性放宽租户合规边界。
- **降级不是无限重试**：每次 `llm_attempts` 记录最多尝试 `max_fallback_attempts` 个候选（默认 3），全部失败则返回错误。
- **降级记录**：`llm_response` 中标记 `fallback_attempted = true` 和 `fallback_from`，进入 EventStore 和 metrics。

### Provider 健康检测

Gateway 维护每个 Provider 的健康状态，不依赖外部健康检查端点：

```text
provider_health
  - provider_id
  - status: healthy | degraded | unhealthy
  - recent_success_rate               # 近 N 次调用成功率
  - recent_p50_latency                # 近期延迟
  - consecutive_failures              # 连续失败次数
  - last_success_at
  - last_failure_at
  - circuit_state: closed | open | half_open
```

健康判定基于滑动窗口内的实际调用结果：

- **healthy → degraded**：成功率低于阈值（例如 < 95%）或延迟异常升高。降级 Provider 仍可用但优先级降低。
- **degraded → unhealthy**：连续失败超过阈值（例如连续 5 次）或成功率低于危险线（例如 < 50%）。unhealthy Provider 暂停使用。
- **unhealthy → half_open**：冷却期（例如 30 秒）后，放少量请求试探。
- **half_open → healthy / unhealthy**：试探成功则恢复；失败则继续冷却。

这就是熔断器（Circuit Breaker）模式。它的目的是"快速失败，别把请求堆积在已经有问题的 Provider 上"。

## 速率管理

Provider 限流和系统自身的请求限流是两件事，需要分开处理：

### Provider 限流保护

Gateway 在调用 Provider 前做客户端侧速率控制，避免频繁触发 Provider 的 429：

```text
provider_rate_limiter
  - provider_id
  - window: sliding_window | token_bucket
  - requests_per_minute: N            # 按 Provider 配额的安全比例设置（例如 80%）
  - tokens_per_minute: N
  - concurrent_requests: N
```

- 速率窗口按 Provider 粒度，不按 tenant。
- 当多个 tenant 共享一个 Provider 时，速率是共享的。需要更精细隔离时，给不同 tenant 配置不同的 Provider 实例。
- 收到 Provider 429 时，读取 `Retry-After` 头，动态调低速率窗口。

### 系统级 LLM 并发控制

这是 [execution-model.md](./execution-model.md) 中提到的 `N_llm`（并发模型调用数）的实现。Gateway 用并发信号量限制同时进行的 LLM 调用总数：

```text
全局并发上限 = min(所有 Provider 并发之和, Worker 池承受能力)
每租户并发上限 = tenant_model_policy.max_concurrent_llm_calls
```

超过上限时，AgentWorker 的调用会排队等待或快速失败（由策略决定）。

## 成本追踪

LLM 调用是平台最大的可变成本。成本追踪贯穿调用全过程：

### 调用前：预算准入

```text
1. AgentWorker 传入 cost_budget_remaining
2. Gateway 按目标模型费率估算本次调用成本：
   estimated_cost = (input_tokens × input_price) + (max_output_tokens × output_price)
3. 如果 estimated_cost > cost_budget_remaining → 拒绝调用
4. 否则预留预算（reserve），关联到 reservation_id
```

### 调用后：成本结算

```text
1. 从 Provider 响应中提取实际 token 用量
2. 按 Provider 当前费率计算实际成本
3. settle 预留：释放 estimated - actual 的差额
4. 成本写入 `llm_attempts` 记录：
   - attempt_key
   - actual_provider
   - actual_model_id
   - input_tokens / output_tokens
   - input_cost / output_cost / total_cost
   - pricing_version（使用的费率版本）
```

### 成本归属

成本沿 `tenant_id → conversation_id → run_id → attempt_key` 链路归属：

```text
成本聚合层次：
  tenant_id          → 租户月度账单
  conversation_id    → 单个会话的累计成本
  run_id             → 单次 run 的成本（可能包含多次 LLM 调用）
  attempt_key     → 单次调用的精确成本
```

### 费率管理

Provider 费率会变。费率配置带 `effective_from` 时间戳，确保历史成本按当时费率计算：

```text
pricing_entry
  - provider_id
  - model_id
  - input_cost_per_million_tokens
  - output_cost_per_million_tokens
  - effective_from
  - effective_until                    # null 表示当前生效
```

成本计算总是使用 `llm_attempts` 发生时刻对应的费率版本，不回溯修改历史成本。

## 流式输出

很多交互场景会使用流式输出，让用户更快看到响应。流式不是默认无条件开启：LLM Gateway 必须根据 run 风险、secret scope、tenant policy、DLP 结果和输出目的地决定 `stream_mode`。

```text
AgentWorker                 LLM Gateway                Provider
    │                           │                         │
    │── stream request ──▶      │                         │
    │                           │── Provider 原生请求 ──▶  │
    │                           │                         │
    │                           │◀── SSE: token delta ──  │
    │◀── 统一 token delta ──    │                         │
    │     （通过 pre-emit 检查后推送）│                      │
    │                           │◀── SSE: token delta ──  │
    │◀── 统一 token delta ──    │                         │
    │          ...              │          ...            │
    │                           │◀── SSE: [DONE] ──────   │
    │◀── stream_end ──────      │                         │
    │                           │                         │
    │  （此时才有完整 usage）    │                         │
```

Gateway 在流式过程中的职责：

- **协议转换**：不同 Provider 的 SSE 格式不同（Anthropic 用 `content_block_delta`，OpenAI 用 `choices[0].delta`），Adapter 统一成平台内部的 token delta 格式。
- **流式准入**：在发送请求前决定 `disabled | buffered_until_checked | chunk_checked`。高风险 run、含 secret scope 的上下文、跨租户管理操作、DLP 命中或策略要求人工检查时，不允许 token 直出。
- **pre-emit 检查**：对准备发给 Realtime 的 chunk 做 secret、PII、危险链接、未授权 artifact 引用和 policy 检查；未通过则停止流式、阻断后续 chunk，并把 run 转入重写、审批或失败路径。
- **token 计数**：部分 Provider 在流式过程中不返回 token 用量，只在结束时给出。Gateway 在收到完整响应后才记录用量和成本。
- **超时检测**：如果 `stream_idle_timeout` 内没有新 token，视为超时。
- **不把 token delta 当事实源**：允许通过 Realtime Gateway 推给客户端（见 [realtime.md](./realtime.md)），也可写入有界 `run_message_chunks`，但不逐 token 写入 EventStore。只有最终的完整消息和 token 用量才持久化。

## 模型版本管理

LLM 模型不是静态的——Provider 会发布新版本、弃用旧版本。平台需要主动管理这些变化：

### 版本固定（Model Pinning）

生产环境不应该使用 Provider 的 "latest" 别名（例如 `provider-reasoning-latest` 可能随时指向不同的快照版本）。平台应该固定到具体版本：

```text
model_pin
  逻辑引用: "reasoning-high"
  固定到: "provider-reasoning-vYYYYMMDD"          # 具体版本，不是 "provider-reasoning-latest"
```

好处：模型行为可预测、可复现；升级是显式操作，不是 Provider 暗中切换。

### 模型升级流程

```text
1. 新模型版本可用
   - 在 Provider Registry 中注册新 model_config
   - status: candidate

2. Release gate
   - 跑固定离线 eval：任务成功率、工具调用准确率、格式遵循、拒答质量、幻觉率、DLP/secret 泄露、成本和延迟
   - 跑红队样本：prompt injection、越权工具调用、恶意网页、敏感输出、长上下文污染
   - 记录 eval_suite_id、基线模型、通过阈值、风险负责人、回滚计划和批准事件
   - 通过后 status: active；不通过则保持 candidate，不进入生产路由

3. 灰度测试
   - 对部分租户（或内部测试租户）更新 model_ref 映射
   - 观察质量指标（成功率、用户反馈、工具调用准确率）
   - 对比新旧模型的成本
   - 命中回滚阈值时恢复旧 model_ref，并保留灰度事件和失败样本

4. 全量切换
   - 更新全局 model_ref 映射
   - 旧模型标记 deprecated

5. 退役
   - Provider 公布 sunset_date 后，确认无活跃 run 使用旧模型
   - 标记 sunset，从路由候选中移除
```

同样的 release gate 也适用于 prompt template、tool descriptor、agent profile、guardrail policy 和 model routing policy。只要会改变 Agent 的行为，就不能直接改当前配置；必须发布新的 snapshot，让历史 Run 仍能回放当时的决策环境。

### 模型弃用告警

当 Provider 宣布模型 sunset 时，平台应该：

- 在 model_config 中记录 `sunset_date`。
- 提前告警（例如 sunset 前 30 天）。
- 强制管理员配置 `successor_model_id`。
- sunset 日期到达后，自动从路由候选中移除。

## 可观测性

LLM 调用的 metrics 使用低基数维度（与 [operations.md](./operations.md) 一致）：

```text
指标维度（低基数）：
  provider         → "anthropic-direct"、"azure-openai-eastus"
  model_family     → "reasoning"、"fast-chat"
  status           → "completed"、"failed"、"timeout"、"rate_limited"
  error_class      → "rate_limited"、"provider_error"、"content_filtered"
  route_type       → "primary"、"fallback"
  tenant_tier      → "free"、"pro"、"enterprise"

不做指标维度（高基数）：
  run_id、conversation_id、attempt_key → 放在 trace/log 中
```

关键指标：

| 指标 | 含义 |
| --- | --- |
| `llm_request_duration` | 调用耗时（histogram，按 provider × model_family） |
| `llm_time_to_first_token` | 首 token 延迟（histogram） |
| `llm_tokens_total` | token 总量（counter，按 direction=input/output） |
| `llm_cost_total` | 成本总量（counter，按 provider × model_family） |
| `llm_request_total` | 调用总数（counter，按 status） |
| `llm_fallback_total` | 降级次数（counter，按 fallback_from → fallback_to） |
| `llm_rate_limit_total` | 触发限流次数（counter，按 provider） |
| `llm_provider_health` | Provider 健康状态（gauge，0/0.5/1） |
| `llm_concurrent_requests` | 当前并发调用数（gauge，按 provider） |
| `llm_budget_exceeded_total` | 预算不足拒绝次数（counter） |

## 生产基线

| 组件 | 基线要求 |
| --- | --- |
| LLM Gateway | 统一接口、Provider adapter、stream 适配、错误分类、成本记录和审计 |
| Provider Registry | 版本化 Provider / model / pricing 配置；支持状态、region、能力、合规和 sunset |
| Model Router | 基于租户策略、能力约束、数据驻留、预算、健康度和速率余量选择模型 |
| Provider Adapter | 每个 Provider 一个协议适配层；统一 request/response/tool/stream/error/usage 格式 |
| Rate Limiter | Provider 级、tenant 级和系统级并发/请求/token 闸门 |
| Health Check | 基于实际调用的滑动窗口、熔断、half-open 探测和告警 |
| Cost Tracker | `llm_attempts` 记录 attempt、provider、model、usage、pricing_version、fallback 和成本 |
| 费率管理 | 费率带 `effective_from` / `effective_until`，历史成本不回溯改写 |
| 合规路由 | 请求携带数据分类、区域、保留和训练使用约束；fallback 不能越过租户合规边界 |

Provider 数量可以逐步增加，但每个新增 Provider 必须通过 adapter、路由、限流、成本、审计、数据处理和故障演练验收后才能进入生产候选。

## 应该 / 避免

| 应该 | 不应该 |
| --- | --- |
| 使用逻辑引用，让 Router 决定具体模型 | 在业务代码中硬编码 model_id |
| 固定到具体模型版本 | 使用 "latest" 别名在生产环境 |
| Provider 限流和业务重试分层 | 把 429 当成普通错误统一重试 |
| 降级时记录 fallback 事实 | 静默降级，事后无法追踪 |
| 成本按调用粒度记录，按费率版本计算 | 用固定费率估算，不记录实际用量 |
| 新模型先灰度再全量 | 全局一次性切换，无法回滚 |
| 流式 token 不持久化，只持久化最终消息 | 把每个 token delta 写入 EventStore |
