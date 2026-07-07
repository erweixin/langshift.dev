# Tool System：定义、注册、版本与生命周期

> 本文档是 [architecture.md](./architecture.md) 的子文档，定义工具如何被描述、注册、发现、版本管理和安全审批。工具的运行时隔离见 [runtime-and-sandbox.md](./runtime-and-sandbox.md)；副作用能力分级见 [execution-model.md](./execution-model.md)。

## 问题、决策与风险

**问题**：Agent 的能力边界由工具决定。一个工具不只是"一段可执行代码"——它有输入输出 schema、副作用等级、权限需求、secret 依赖、runtime 要求和版本演进。如果只暴露一个 handler 函数，系统就无法在调用前判断"这个工具能不能安全执行、失败后能不能重试、模型应该怎么调用它"。

**决策**：每个工具必须携带一份完整的声明（Tool Descriptor），涵盖 schema、能力、权限、运行时需求和版本。系统根据声明决定调度、隔离、审批和重试策略，不依赖工具代码本身的行为。

**为什么不直接让 Agent 调用函数**：LLM 决定调用什么工具，但不应该决定工具是否有权执行、失败后是否重试、凭证如何获取。这些是平台责任，必须由声明驱动，不能靠 handler 内部自行判断。

**忽略后果**：缺乏声明的工具会导致——模型调用格式错误时 runtime 才报错；危险写操作被当成安全读操作自动重试；工具升级后旧版 schema 和新版 handler 不匹配；租户自定义工具绕过安全审查直接上线。

| 应该 | 不应该 |
| --- | --- |
| 工具先声明能力，平台再决定调度 | 工具在 handler 里自行判断权限和重试 |
| Schema 版本化，不兼容变更走新版本 | 直接改 schema 并期望旧 run 不受影响 |
| 租户自定义工具经过审批和沙箱验证 | 上传即生效，不检查 schema 和能力声明 |
| 工具 schema 进入 `context_manifest` | 模型调用工具时不记录用的是哪个版本 |

## 工具是什么

在 Lites 中，工具是 Agent 能力的原子单元。Agent 通过 LLM 决定"接下来用哪个工具"，但工具的定义、权限、执行和结果处理都由平台管控。

可以把工具理解为三层：

```text
┌─────────────────────────────────────┐
│  Tool Descriptor（声明）             │  我是谁、输入输出长什么样、
│  schema + 能力 + 权限 + 运行时需求   │  失败后怎么处理、需要什么环境
├─────────────────────────────────────┤
│  Tool Handler（实现）                │  真正执行的代码
│  平台内置 / 租户上传 / 外部 API      │
├─────────────────────────────────────┤
│  Tool Runtime（执行环境）            │  在哪里跑、用什么隔离等级
│  由 RuntimeManager 根据声明分配      │
└─────────────────────────────────────┘
```

平台只关心 Descriptor；Handler 是黑盒，在 sandbox 里执行；Runtime 由 Descriptor 中的需求和租户策略共同决定。

## Tool Descriptor

每个工具必须提供以下声明。AgentWorker 用它构建 LLM function calling schema；ToolWorker 用它决定调度、隔离和重试；Permission Service 用它做准入检查。

```text
tool_descriptor
  # ── 身份 ──
  - tool_name                          # 稳定标识符，例如 "file_write"、"github_create_pr"
  - tool_version                       # 语义版本，例如 "1.2.0"
  - display_name                       # 给模型和用户看的名称
  - description                        # 给模型看的能力描述，直接进入 system prompt
  - category                           # 分类，例如 filesystem、code_execution、external_api、search

  # ── 输入输出 Schema ──
  - input_schema                       # JSON Schema，定义参数名、类型、必填、约束
  - output_schema                      # JSON Schema，定义返回结构
  - input_examples[]                   # 可选，帮助模型理解用法

  # ── 副作用能力（见 execution-model.md）──
  - effect_class                       # read_only | idempotent_write | reconcilable_write |
                                       #   compensatable_write | irreversible_write
  - requires_effect_key: bool
  - supports_reconcile: bool
  - supports_compensation: bool
  - max_attempts                       # 最大重试次数
  - reconcile_after                    # outcome_unknown 多久后触发对账

  # ── 权限与审批 ──
  - required_permissions[]             # 执行前必须满足的权限列表
  - manual_approval_required: bool     # 是否必须走 waiting_approval
  - approval_hint                      # 给审批人看的风险说明
  - secret_scopes[]                    # 需要哪些 secret scope（由 Secret Broker 校验）

  # ── 运行时需求 ──
  - trust_tier                         # trusted | semi_trusted | untrusted | privileged
  - runtime_image                      # 执行环境镜像（平台提供或租户自定义）
  - resource_limits                    # CPU、内存、磁盘、超时
  - network_egress_policy              # deny_all | allowlist | tenant_policy
  - egress_allowlist[]                 # 允许访问的域名列表

  # ── 元信息 ──
  - source                             # platform | tenant_custom | marketplace
  - deprecated: bool                   # 已弃用标记
  - successor_tool                     # 弃用后推荐的替代工具
  - changelog                          # 变更记录
```

### Schema 怎么用

工具的 `input_schema` 和 `output_schema` 有三个消费者：

1. **LLM**：AgentWorker 把 `input_schema` 转成模型的 function calling 格式。模型返回的参数必须通过 schema 校验，校验失败不进入 ToolWorker，而是让模型重新生成。
2. **ToolWorker**：执行前再次校验输入，防止绕过 AgentWorker 直接提交的请求。执行后校验输出，格式不符则记录 `ToolCallFailed`。
3. **`context_manifest`**：每次 LLM 调用记录当时使用的 `tool_schema_versions`，事后可以回答"模型看到的工具定义是哪个版本"。

## 工具来源与注册

工具按来源分三类，注册和审批流程不同：

### 平台内置工具（Platform Tools）

由平台团队开发和维护。随代码版本发布，不需要运行时注册。

```text
注册方式：代码内声明 + 部署时自动加载
审批：不需要额外审批
更新：随平台版本发布，遵循语义版本
示例：file_read、file_write、bash_execute、web_search
```

### 租户自定义工具（Tenant Custom Tools）

由租户通过管理 API 注册。上传 Descriptor + Handler（代码包或容器镜像），经过验证后才能在该租户的 conversation 中使用。

```text
注册流程：
1. 租户通过 Tool Management API 提交 Descriptor + Handler
2. 平台校验 input/output schema 格式
3. 平台校验 effect_class 与声明的一致性（例如声明 read_only 但请求了 secret scope）
4. 平台在隔离环境中执行 dry-run 验证（可选）
5. 工具进入 pending_review 状态
6. 管理员审批或自动策略通过后，状态变为 active
7. 工具对该租户的 AgentWorker 可见
```

### 市场工具（Marketplace Tools）

由第三方开发，经平台审核后发布到工具市场。租户从市场安装到自己的工具集。

```text
注册流程：
1. 开发者提交工具到市场审核
2. 平台安全团队审查 Descriptor、Handler 和镜像
3. 审核通过后发布到市场
4. 租户从市场选择并安装
5. 安装时绑定租户的 secret scope 和 permission policy
6. 工具版本更新由市场推送，租户可选择自动或手动升级
```

## 工具可见性与发现

AgentWorker 在构建 LLM context 时，需要决定"这次调用给模型看哪些工具"。不是把所有注册工具都塞进 prompt——工具太多会降低模型选择准确性，也浪费 token。

### 可见性规则

```text
tool_visibility_resolution
  输入：tenant_id, user_id, conversation_id, run_id, policy_version
  过程：
    1. 加载该 tenant 的 active 工具集（平台 + 自定义 + 已安装市场工具）
    2. 过滤掉 deprecated 且无 successor 的工具
    3. 按 tenant policy 排除禁用工具
    4. 按 user 权限排除无权使用的工具
    5. 按 conversation context 排除不相关的工具（可选，由策略控制）
    6. 输出：该次 run 的可用工具列表 + 对应 schema 版本
  缓存：结果可缓存，cache key = (tenant_id, policy_version, tool_set_hash)
  记录：最终工具列表的 schema 版本进入 context_manifest.tool_schema_versions
```

### 工具集快照

为了保证同一个 run 内工具定义不变，AgentWorker 在 run 开始时锁定一份工具集快照：

- Run 创建时记录 `tool_set_snapshot_id`，指向当时的工具列表和版本。
- 同一 run 内的所有 LLM 调用和 ToolCall 都使用这份快照。
- 如果 run 执行期间工具被更新或删除，当前 run 不受影响。
- 新 run 使用最新工具集。

这和 `context_manifest` 中的 `tool_schema_versions` 配合：快照保证 run 内一致性，manifest 保证事后可审计。

## 版本管理

### 版本规则

工具版本遵循语义版本（major.minor.patch）：

| 变更类型 | 版本动作 | 兼容性 | 示例 |
| --- | --- | --- | --- |
| 新增可选参数 | minor bump | 向后兼容 | 给 `file_write` 加一个可选的 `encoding` 参数 |
| 修复 bug，行为不变 | patch bump | 向后兼容 | 修复 `web_search` 的 URL 编码问题 |
| 删除参数 | major bump | 不兼容 | 删除 `file_write` 的 `mode` 参数 |
| 改变参数含义 | major bump | 不兼容 | `timeout` 从秒改为毫秒 |
| 改变 `effect_class` | major bump | 不兼容 | 从 `read_only` 变为 `idempotent_write` |

### 不兼容版本共存

Major 版本变更时，新旧版本必须共存一段时间：

```text
file_write@1.3.2  →  status: active
file_write@2.0.0  →  status: active
file_write@1.3.2  →  status: deprecated, successor: file_write@2.0.0
```

- 已创建的 run 继续使用快照中的旧版本。
- 新 run 默认使用最新 active 版本。
- `deprecated` 工具在可见性解析时仍可加载（因为旧 run 可能引用它），但新 run 不再选择它。
- 当没有活跃 run 引用旧版本后，可以将其状态改为 `retired`（不再加载）。

## 工具生命周期状态

```text
draft → pending_review → active → deprecated → retired
                \→ rejected
```

| 状态 | 含义 | 谁可以使用 |
| --- | --- | --- |
| `draft` | 租户正在编辑，尚未提交 | 仅租户管理界面可见 |
| `pending_review` | 已提交，等待审批 | 不可使用 |
| `rejected` | 审批未通过 | 不可使用，附带拒绝原因 |
| `active` | 可正常使用 | AgentWorker 可发现和调用 |
| `deprecated` | 仍可使用但不推荐，通常有后继版本 | 旧 run 可用，新 run 不选择 |
| `retired` | 彻底下线 | 不可使用 |

状态变更本身先作为管理事件记录（`ToolVersionActivated`、`ToolVersionDeprecated` 等），再由同一事务或受控投影更新工具注册表状态。禁止绕过管理事件直接手改数据库行。

## Schema 校验流程

模型返回的工具调用必须经过两道校验，才能进入 ToolWorker 执行：

```text
LLM 返回 tool_use
  │
  ▼
AgentWorker：用 input_schema 校验参数
  │
  ├─ 校验通过 → 写入 ToolCallRequested 事件，发出 ExecuteToolCall 命令
  │
  └─ 校验失败 → 不创建 ToolCall
       ├─ 可修复（缺少必填字段、类型错误）→ 把错误信息反馈给模型，让模型重新生成
       │    重试有次数上限（例如 3 次），超过则记录 RunFailed
       └─ 不可修复（工具不存在、版本已 retired）→ 记录错误事件，模型重新规划

ToolWorker 领取 ExecuteToolCall
  │
  ▼
ToolWorker：再次用 input_schema 校验（防止绕过 AgentWorker 的直接提交）
  │
  ├─ 校验通过 → 进入 sandbox 执行
  │
  └─ 校验失败 → 写入 ToolCallFailed，不执行 handler
```

为什么要校验两次：AgentWorker 的校验防止把错误参数发给队列；ToolWorker 的校验防止从非 AgentWorker 路径（Repair API、手动重试）提交的请求绕过检查。

## 工具与 EventStore 的关系

工具本身的注册、版本和生命周期变更是管理面操作，不走 conversation 的 EventStore。但工具的**执行过程**完全由 EventStore 管理：

| 数据 | 存在哪里 | 由谁管理 |
| --- | --- | --- |
| Tool Descriptor（声明） | 工具注册表（PostgreSQL 管理表） | Tool Management API |
| 工具集快照 | 快照引用表 | AgentWorker 在 run 创建时生成 |
| ToolCall 状态和结果 | EventStore（events + tool_calls 投影） | EventService |
| 副作用记录 | Effect Ledger | ToolWorker 通过 EventService 写入 |
| Schema 版本引用 | `context_manifest.tool_schema_versions` | AgentWorker |

工具注册表和 EventStore 通过 `tool_name + tool_version` 关联。EventStore 不保存 Tool Descriptor 的完整副本，只保存版本引用；如果需要回溯"当时的工具定义是什么"，通过版本号从注册表的历史版本中查询。

## 租户自定义工具的安全约束

租户自定义工具和平台工具的执行路径完全相同（ToolWorker → sandbox），但受到额外约束：

- **trust_tier 上限**：租户自定义工具最高为 `semi_trusted`，不能声明 `trusted` 或 `privileged`。需要更高权限的工具必须走市场审核。
- **effect_class 审核**：声明 `irreversible_write` 或 `compensatable_write` 的自定义工具需要管理员审批。
- **网络出口**：默认 `deny_all`，只有显式声明 `egress_allowlist` 且审批通过后才开放。
- **Secret 访问**：自定义工具只能访问租户自己创建的 secret scope，不能访问平台级 secret。
- **资源限制**：自定义工具的 `resource_limits` 有租户级上限，不能超过租户配额。
- **镜像来源**：自定义镜像必须通过扫描；或使用平台提供的基础镜像。

## 工具依赖

有些工具依赖特定的 runtime 能力或其他工具的输出。依赖关系在 Descriptor 中声明：

```text
tool_dependencies
  - requires_runtime_capabilities[]    # 例如 ["python3.11", "gpu", "network"]
  - requires_workspace: bool           # 是否需要 workspace 访问
  - workspace_access: read | read_write
  - suggested_tools[]                  # 建议配合使用的工具（仅影响可见性排序）
```

依赖不构成调用链——Agent 决定调用顺序，平台只在 ToolWorker 分配 runtime 时检查依赖是否满足。如果 runtime 不支持声明的 capability，ToolCall 直接失败，不尝试执行。

## Do / Don't

| 应该 | 不应该 |
| --- | --- |
| 用 Descriptor 声明能力，让平台决定调度和隔离 | 在 handler 里自行判断权限、重试和隔离 |
| Schema 不兼容变更走 major 版本 | 直接修改 schema 并期望所有 run 自动适配 |
| 工具集快照保证 run 内一致性 | 在 run 执行中途切换工具版本 |
| 两次 schema 校验（AgentWorker + ToolWorker） | 只在 AgentWorker 校验一次 |
| 自定义工具经过审批和沙箱验证 | 上传即 active |
| 弃用工具时提供 successor 和共存期 | 直接删除正在被引用的工具版本 |
