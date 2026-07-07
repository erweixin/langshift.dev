# Memory 架构：记忆、检索与上下文召回

> 本文档解释 Agent 的“记忆”怎么产生、怎么存、怎么召回、怎么删除。上下文压缩和 `context_manifest` 见 [execution-model.md](./execution-model.md)。

## 问题、决策与风险

**问题**：一次 Agent run 的上下文窗口有限，但用户期望 Agent "记住"跨会话的偏好、项目知识和历史决策。如果每次 run 都从零开始，Agent 会反复问同样的问题、忘记之前的约定、忽略已有的项目上下文。

**决策**：Memory 是 EventStore 之外的**可重建投影**。它的写入流经事件（`MemoryUpserted` / `MemoryDeleted`），内容由向量索引和全文索引提供检索能力。AgentWorker 在构建上下文时按相关度召回 memory chunk，召回结果记录在 `context_manifest` 中。

**为什么 Memory 不是事件本身**：事件是编排事实（"发生了什么"），Memory 是从事实中提炼的知识（"应该记住什么"）。事件可能包含大量中间过程（工具输出、LLM 推理步骤），直接把它们塞进下一次 prompt 既浪费 token 又引入噪声。Memory 是筛选和压缩后的知识投影。

**为什么 Memory 必须可重建**：向量索引可能损坏、嵌入模型可能升级、数据可能需要重新分片。如果 Memory 是唯一事实源，这些操作就会丢失数据。Memory 的写入事件保存在 EventStore 中，索引随时可以从事件重建。

**忽略后果**：Memory 没有事件支撑时，索引损坏就是数据丢失；没有租户隔离时，A 租户的记忆可能泄漏给 B 租户；没有版本记录时，无法回答"Agent 当时记住了什么"导致行为不可解释。

| 应该 | 不应该 |
| --- | --- |
| Memory 写入流经事件，索引是可重建投影 | 把 Memory 直接写入向量数据库，不记录事件 |
| 每次召回记录在 `context_manifest` | 用了哪些记忆不留痕迹 |
| 按 tenant 和 scope 隔离 Memory | 共享一个全局向量空间 |
| Memory 有明确的写入、更新和淘汰策略 | 只写不删，无限增长 |

## 先用白话说

Memory 不是聊天记录原文，也不是另一套事实源。它更像是从事件里提炼出来的“便签”：

- 用户说过的偏好。
- 项目里稳定的事实。
- 之前 run 做过的重要决策。
- 后续任务经常需要用到的上下文。

这些便签可以放进向量索引里方便检索，但真正能证明它们来源的，是 EventStore 里的 `MemoryUpserted` 和 `MemoryDeleted` 事件。索引坏了可以重建，事件不能丢。

## Memory 的层次

Agent 的"记忆"不是一块铁板，而是按生命周期和用途分成三层。每层的写入时机、存储方式和召回策略不同：

```text
┌──────────────────────────────────────────────┐
│  Working Memory（工作记忆）                    │
│  当前 run 的对话历史 + 工具结果 + 摘要         │
│  生命周期：单次 run                            │
│  存储：EventStore events + snapshot            │
│  不需要向量检索，直接按 seq 读取               │
├──────────────────────────────────────────────┤
│  Conversation Memory（会话记忆）               │
│  跨 run 但在同一 conversation 内的累积知识     │
│  生命周期：conversation 存续期间               │
│  存储：Memory 投影 + 向量索引                  │
│  按 conversation_id 隔离                      │
├──────────────────────────────────────────────┤
│  Long-term Memory（长期记忆）                  │
│  跨 conversation 的用户偏好、项目知识、学习     │
│  生命周期：直到显式删除或 retention 到期        │
│  存储：Memory 投影 + 向量索引                  │
│  按 tenant_id + memory_scope 隔离             │
└──────────────────────────────────────────────┘
```

### Working Memory

Working Memory 就是当前 run 的上下文窗口内容。它不是本文档的重点，因为它完全由 EventStore 管理：

- 对话消息、工具请求和结果都是事件。
- AgentWorker 通过 snapshot + 增量 events 恢复当前上下文。
- 当历史太长时，Context Builder 做滚动摘要（见 [execution-model.md](./execution-model.md)）。
- 摘要本身是可重建投影，不污染事件事实源。

### Conversation Memory

同一个 conversation 中，Agent 可能跑过多轮 run。Conversation Memory 记录跨 run 的知识，例如：

- "用户在第 3 轮 run 中说过这个项目用 Go 语言"
- "第 5 轮 run 修改了 auth 模块，引入了 JWT"
- "用户偏好：不要自动格式化代码"

这些信息不是每次都在对话历史中——早期 run 的详细对话可能已被压缩或不在当前 run 的事件范围内。Conversation Memory 把这些知识提炼出来，供后续 run 召回。

### Long-term Memory

跨 conversation 的持久知识。例如：

- 用户级偏好："总是用中文回复"、"commit message 用英文"
- 项目级知识："这个 repo 的入口是 cmd/server/main.go"、"数据库用 PostgreSQL 14"
- 团队级约定："API 命名用 snake_case"、"错误码格式是 ERR_MODULE_NAME"

Long-term Memory 的 scope 比 conversation 更大，按 `tenant_id` + `memory_scope`（user / project / team）隔离。

## Memory Document

Memory 的最小存储单元是 Memory Document。不论哪个层次的记忆，都以 Document 的形式存储和检索：

```text
memory_document
  # ── 身份 ──
  - memory_id                          # 唯一标识
  - tenant_id                          # 租户隔离
  - memory_scope                       # conversation | user | project | team
  - scope_id                           # conversation_id | user_id | project_id | team_id
  - version                            # 文档版本，每次更新递增

  # ── 内容 ──
  - content_ref                        # 指向加密 payload；projection 不保存用户内容明文
  - content_hmac                       # tenant/subject scoped HMAC，用于去重；避免可枚举明文 hash
  - content_type                       # text | structured | code_snippet
  - summary_ref?                       # 可选，指向加密或已脱敏摘要；不能保存未授权 PII/secret 明文
  - sensitivity_labels[]               # pii | secret | private_code | regulated | public 等

  # ── 来源 ──
  - source_kind                        # agent_extracted | user_stated | system_derived
  - source_run_id                      # 产生这条记忆的 run
  - source_event_id                    # 关联的事件
  - confidence                         # Agent 提取时的置信度（0-1）

  # ── 检索 ──
  - embedding_vector                   # 向量表示（存储在向量索引中）
  - embedding_model_id                 # 使用的嵌入模型
  - tags[]                             # 标签，辅助过滤
  - last_accessed_at                   # 最近被召回的时间

  # ── 生命周期 ──
  - created_at
  - updated_at
  - expires_at                         # 可选，到期后由 Sweeper 清理
  - deleted_at                         # 软删除标记
  - superseded_by                      # 被哪条新记忆取代
```

## 写入：Memory 怎么产生

Memory 不是凭空出现的，每一条写入都有明确来源。写入路径有三种：

### 1. Agent 提取（Agent Extracted）

AgentWorker 在 run 执行过程中，识别出值得记住的信息，主动写入 Memory。

```text
run 执行过程中：
  AgentWorker 调用 LLM
  → LLM 判断"用户刚说的偏好值得记住"
  → AgentWorker 调用 MemoryService.upsert()
  → MemoryService 在 EventStore 中写入 MemoryUpserted 事件
  → 异步更新向量索引和全文索引
```

Agent 提取是最常见的写入方式。它依赖 LLM 的判断力，因此带有 `confidence` 字段。低置信度的记忆可以在后续被确认、修正或淘汰。

触发提取的典型信号：
- 用户明确说"记住这个"或"以后都这样做"
- 用户纠正了 Agent 的行为（隐含偏好）
- run 结束时的关键决策和结果
- 工具执行产生的重要发现（例如"这个项目用的是 monorepo 结构"）

### 2. 用户显式声明（User Stated）

用户通过 API 或界面直接管理自己的 Memory：

```text
用户操作：
  → 通过 Memory Management API 创建/更新/删除记忆
  → MemoryService 写入 MemoryUpserted / MemoryDeleted 事件
  → 更新索引
```

用户显式声明的 Memory `confidence = 1.0`，优先级高于 Agent 提取的记忆。

### 3. 系统派生（System Derived）

平台根据使用模式自动派生的记忆，例如：

- 从多次 run 的 `context_manifest` 中统计高频使用的工具和文件
- 从 conversation 历史中提取反复出现的主题

系统派生的 Memory 标记为 `source_kind: system_derived`，可以被用户或 Agent 覆盖。

## 写入与 EventStore 的关系

Memory 写入必须流经 EventStore，保证可审计和可重建：

```text
MemoryUpserted 事件
  - tenant_id
  - memory_id
  - memory_scope + scope_id
  - content_ref                        # 指向加密 payload；事件不直接保存用户内容明文
  - content_hmac                       # tenant/subject scoped HMAC；不存裸 hash
  - encryption_subject_id / key_ref     # 用于 erasure 时销毁或失效
  - source_kind + source_run_id + source_event_id
  - embedding_model_id
  - version
  - supersedes_memory_id              # 如果是更新，指向被取代的记忆

MemoryDeleted 事件
  - tenant_id
  - memory_id
  - reason: user_request | expiration | superseded | erasure
  - erased_subject_ids?                # erasure 时标记已销毁的主体密钥或 payload 引用
```

向量索引和全文索引是这些事件的**异步投影**。投影更新可以延迟，但不会影响 EventStore 的一致性。投影损坏时，从事件重放即可重建。若 memory 可能包含用户内容、PII、凭证、私有代码或受保留策略约束的数据，明文只能保存在加密 payload 中，不能直接进入不可变事件或可重放 projection；EventStore 和 `memory_documents` 只保存 HMAC/digest、引用、来源、分类标签和密钥元数据。摘要、embedding、关键词索引同样按敏感派生物处理，必须能随主体删除而失效或重建。

## 检索：Memory 怎么被召回

AgentWorker 在构建 LLM context 时，通过 Memory Retrieval 召回相关记忆。召回不是"把所有记忆塞进 prompt"，而是有选择地检索：

### 检索流程

```text
AgentWorker 构建上下文
  │
  ▼
1. 构造检索查询
   - 来源：当前用户消息 + 最近几轮对话 + 当前 workspace 上下文
   - 生成查询向量（embedding）
   │
  ▼
2. 向量检索 + 过滤
   - 在该 tenant 的向量索引中做近似最近邻搜索（ANN）
   - 过滤条件：memory_scope ∈ 允许的 scope、未过期、未删除
   - 按 scope 优先级排序：conversation > user > project > team
   - 取 top-K 候选
   │
  ▼
3. 相关度打分与重排
   - 向量相似度 × scope 权重 × 新鲜度衰减 × confidence 权重
   - 去除与当前 Working Memory 重复的内容（content_hmac / digest 比对）
   - 超过 token 预算时截断
   │
  ▼
4. 组装进 context
   - 召回的 memory chunk 放入 prompt 的指定位置（通常在 system prompt 之后、对话历史之前）
   - 记录到 context_manifest：
     - memory_document_ids + versions
     - retrieval_chunk_ids
     - retrieval_query_hash
     - retrieval_model_id
   │
  ▼
5. 更新访问记录
   - 被召回的 memory document 更新 last_accessed_at
   - 用于后续淘汰策略
```

### 检索隔离

检索必须严格按租户和 scope 隔离：

```text
向量索引的查询条件（不可省略）：
  WHERE tenant_id = $current_tenant_id
  AND   memory_scope IN ($allowed_scopes)
  AND   scope_id IN ($allowed_scope_ids)
  AND   deleted_at IS NULL
  AND   (expires_at IS NULL OR expires_at > now())
```

即使使用外部向量数据库，tenant 过滤也必须是查询的一部分，不能只靠应用层过滤——和 EventStore 的 RLS 原则一致。

### 检索与 `context_manifest`

每次 LLM 调用的 `context_manifest` 必须记录召回了哪些 memory，这样事后可以回答：

- "Agent 当时知道用户有什么偏好？"→ 查 manifest 中的 `memory_document_ids`
- "这条记忆影响了哪些 run？"→ 反查引用了该 `memory_id` 的 manifest
- "嵌入模型升级后，召回结果会变吗？"→ 对比不同 `retrieval_model_id` 的召回集合

## 更新与去重

Memory 不是 append-only 的——同一知识点可能被多次提到，或者用户偏好可能改变。更新策略：

### 内容去重

写入前用 tenant / subject scoped `content_hmac` 检查是否已存在相同内容。不要使用裸 SHA 之类可离线枚举的 hash 存敏感短文本：

```text
写入 Memory 时：
  1. 计算 content_hmac
  2. 查询同 scope 内是否存在相同 HMAC 的 active memory
  3. 如果存在且内容相同 → 跳过写入，只更新 last_accessed_at
  4. 如果存在但有补充信息 → 创建新版本，旧版本标记 superseded_by
  5. 如果不存在 → 创建新 memory document
```

### 语义合并

同一知识点可能用不同措辞表达多次。语义合并不是自动的——由 Agent 在提取时判断：

```text
Agent 发现"用户又提到了偏好使用 TypeScript"
  → 检索到已有 memory："用户偏好 TypeScript"
  → Agent 判断这是同一知识点的确认，不是新信息
  → 更新已有 memory 的 confidence（提高）和 updated_at
  → 写入 MemoryUpserted 事件，version + 1
```

### 矛盾处理

用户偏好可能改变（"以前说用 JavaScript，现在说用 TypeScript"）：

```text
Agent 发现新偏好与旧 memory 矛盾
  → 创建新 memory document
  → 旧 memory 标记 superseded_by = 新 memory_id
  → 旧 memory 的 deleted_at 设为当前时间
  → 两条事件：MemoryUpserted（新）+ MemoryDeleted（旧，reason: superseded）
```

## 淘汰与清理

Memory 不能无限增长。淘汰策略由 Sweeper 执行：

### 淘汰规则

```text
memory_eviction_policy
  # 按优先级从高到低：
  1. 已过期：expires_at < now()                               → 删除
  2. 已被取代：superseded_by IS NOT NULL 且新版本已稳定        → 删除
  3. 长期未访问：last_accessed_at < now() - retention_period   → 标记待清理
  4. 低置信度且未被确认：confidence < threshold AND 未被用户确认 → 标记待清理
  5. 超过 scope 容量上限：同一 scope 内 document 数超过限制    → 按访问频率淘汰
```

### 容量限制

每个 scope 有 Memory 容量上限，由租户策略控制：

```text
memory_quota
  - tenant_id
  - scope: conversation | user | project | team
  - max_documents                      # 该 scope 内最大 document 数
  - max_total_size_bytes               # 该 scope 内最大总内容大小
  - retention_period                   # 未访问多久后可淘汰
  - eviction_strategy                  # lru | lfu | confidence_weighted
```

### Sweeper 行为

Memory 清理由 Timer / Sweeper 执行，和其他 Sweeper 任务遵循相同规则（见 [operations.md](./operations.md)）：

- 定期扫描过期和待淘汰的 memory document。
- 每次清理写入 `MemoryDeleted` 事件。
- 清理后更新向量索引（删除对应 embedding）。
- 清理受限于每次批量大小，不一次性删除所有待淘汰记忆。

## 嵌入模型管理

Memory 的向量检索依赖嵌入模型。模型升级时需要重建索引：

### 模型版本

```text
embedding_model_config
  - model_id                           # 例如 "text-embedding-3-small-v2"
  - model_version                      # 模型版本
  - vector_dimensions                  # 向量维度
  - status: active | deprecated | rebuilding
```

### 模型升级流程

```text
嵌入模型升级：
  1. 新模型注册为 active，旧模型标记 deprecated
  2. 后台任务逐批重新 embedding 所有 active memory document
  3. 新旧索引并存期间，检索同时查询两个索引，合并结果
  4. 全部重建完成后，旧索引标记为可删除
  5. memory_document 的 embedding_model_id 更新为新模型
```

重建是幂等的：每个 document 的重建以 `(memory_id, model_id)` 为幂等键。重建中途失败可以从断点继续。

## Memory 与数据删除

当用户请求数据删除（GDPR "被遗忘权"等）时，Memory 必须配合清除：

- `SubjectErasureRequested` 事件触发 Memory 清理。
- 删除或失效所有以该用户为数据主体的 memory document，包括 project/team scope 中包含该用户原始内容或由其派生出的记忆；不包含该主体数据的共享知识不应被整段误删。
- 从向量索引中移除对应 embedding。
- 销毁或失效 memory content 的主体密钥 / payload key（crypto-shredding，见 [multi-tenancy-and-security.md](./multi-tenancy-and-security.md)）。
- 写入 `MemoryDeleted` 事件（reason: `erasure`）。
- 清理完成后，即使从事件重建索引，已删除的内容也不可恢复：事件只剩不可逆 hash、引用和审计元数据，明文 payload 因密钥销毁不可解密。

## 生产基线

| 组件 | 基线要求 |
| --- | --- |
| Memory 事件存储 | Memory 写入和删除必须流经 EventStore；事件是可审计事实源 |
| 向量索引 | tenant-scoped 向量索引；外部向量库也必须在查询层强制 tenant/scope filter |
| 全文检索 | 支持关键词 + metadata filter，与向量召回共同接受 ACL 约束 |
| 嵌入计算 | 记录 `embedding_model_id` 和重建幂等键；模型升级支持双索引过渡 |
| 检索服务 | 独立 Retrieval Service 或等价逻辑边界；召回结果必须进入 `context_manifest` |
| 删除与加密 | 可能包含 PII/用户内容的 payload、summary、embedding 和全文索引派生物必须使用加密内容引用、可销毁密钥或可失效投影；erasure 后不可通过 replay 恢复明文 |

Memory 索引是可重建投影，不是事实源。无论使用 pgvector、独立向量数据库还是托管 RAG，替换或扩展时只允许重建索引投影，不能绕过 EventStore、ACL、召回留痕和删除语义。

## 应该 / 避免

| 应该 | 不应该 |
| --- | --- |
| Memory 写入流经事件，索引作为可重建投影 | 直接写向量数据库，不记录事件 |
| 每次召回记录在 `context_manifest` | 召回了什么不留记录 |
| 按 tenant + scope 严格隔离检索 | 共享向量空间，只靠应用层过滤 |
| 有明确的淘汰、去重和容量策略 | 只写不删，让索引无限膨胀 |
| 嵌入模型升级时新旧索引并存过渡 | 直接替换索引，中间丢失检索能力 |
| 数据删除时连带清除 Memory 和索引 | 只删事件不删 embedding |
| Agent 提取的记忆标记 confidence | 把 Agent 猜测的偏好当成确定事实 |
