# Event Catalog（v0 数据契约）

> 这篇是 v0 的第一份冻结契约：系统里会发生哪些 event、每条长什么样。事件语义是 v0 唯一"不许欠债"的东西（见 [v0-product-slice.md](./v0-product-slice.md)），所以先冻结这一页，再写 migration。新增或修改事件类型，必须先改本文件。

几个词先说清：

- **envelope**：每条 event 都有的公共字段。
- **payload**：每种事件自己携带的数据。
- **投影**：消费 event 计算出来的当前状态表（任务列表、画像等），永远可以从事件流重建。

## 命名与版本规范

- 事件名用 PascalCase 的"名词 + 完成动作"：`TaskGenerated`、`EvidenceSubmitted`（与平台文档的 `RunAccepted` 风格一致）。
- 事件一旦提交不修改、不删除；payload 结构要变，就升 `schema_version`，投影逻辑同时兼容新旧版本。
- **唯一例外：账户删除**。作为特权流程按 `user_id` 物理删除整条事件流与全部表行（v0 采用硬删除；v1 演进为 per-user 加密 + 销毁密钥）。删除动作本身留一条**不含用户内容**的系统审计（`erasure_audit` 表，阶段 5 建；邮箱 / user_id 仅存哈希）；删除 job 自身与该审计表不在删除范围。这是"全库按 user 检索无残留"DoD 与 append-only 语义的闭环方式。
- 每条事件由一个 command / 请求产生，`command_id` 用于追溯与去重：API 写入经 `idempotency_keys` 复用同一服务端 `command_id`，worker 写入经 `JobFence` 校验并在同事务内 append + 标记 job done，避免 at-least-once 投递下重复落账。

## Envelope

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `event_id` | ULID | 主键，天然按时间有序 |
| `seq` | bigint | **用户内提交顺序号**（v0 的顺序边界收敛到 user，对应平台契约里 conversation-scoped 的 `seq`）。SSE 补拉与客户端去重的游标：客户端记 `last_seen_seq`，按 `seq` 去重，语义对齐 [realtime.md](./realtime.md) |
| `type` | string | 事件类型（下表） |
| `schema_version` | int | payload 结构版本，从 1 开始 |
| `user_id` | string | 所属用户（v0 的隔离边界与顺序边界） |
| `mission_id` / `task_id` / `run_id` | string? | 关联键，按需填 |
| `command_id` | string? | 产生本事件的 command（命令去重与追溯）。**一律是服务端生成的 ULID**——客户端 `Idempotency-Key` 经 `idempotency_keys` 映射表（作用域 user_id + endpoint）换取 command_id，不直接入库，避免跨用户 / 跨操作的同名 key 冲突 |
| `causation_id` / `correlation_id` | string? | 由哪条 event / command 引起；属于哪条因果链 |
| `created_at` | timestamp | 提交时间 |
| `payload` | JSON | 类型专属数据 |

**显式推迟的字段**（对齐 [concurrency-and-durability.md](./concurrency-and-durability.md) 的持久化契约，v0 不落列、migration 注释预留）：

- `tenant_id`：v0 以 `user_id` 为边界，多租户是 v1 升级项（见 [v0-product-slice.md](./v0-product-slice.md) 升级信号）。
- `conversation_id`：v0 把顺序边界收敛到 user；Coach 对话另有 `conversations` 投影，不承担事件排序职责。
- `store_epoch`：随 v1 的备份恢复演练一起引入；v0 内测数据可承受重建。

## 事件清单（v0 全集）

### 平台层（run 生命周期，所有 run 类型共用）

命名以 [state-machines.md](./state-machines.md) 的转换表为准（终态：`succeeded` / `failed` / `expired` / `cancelled`，普通流程不能再推进）。v0 不使用审批与取消相关事件。历史文档中的 `RunCompleted`（end-to-end-flow 旧称）与 `RunTimedOut` 均已收敛，以本表为准。

| type | payload 要点 | 更新投影 |
| --- | --- | --- |
| `RunAccepted` | `run_type`（diagnosis / task_gen / lesson_gen / review / reentry）, `input_ref`（已有事实引用：event_id / evidence_id / content_key 等，不存大块输入） | runs |
| `RunQueued` / `RunStarted` | — / `attempt_id` | runs |
| `RunSucceeded` / `RunFailed` / `RunExpired` | `error?`, `ledger_attempt_keys?`（**引用** LLM 账本行，不内嵌用量）；`RunExpired` 由 sweeper 依 `due_at` 产生 | runs。`llm_ledger` 由 LLM client 在调用路径直接写入（独立事实账本，pre-call pending → 补全 / unknown），**不由事件驱动** |

### 目标与路线

| type | 产生者 | payload 要点 | 更新投影 |
| --- | --- | --- | --- |
| `MissionCreated` | 诊断 run | name, current, target, roadmap[], adv[], gaps[], bridge | missions, profile 初始化 |
| `MissionAdjusted` | 用户纠正（删/补标签、改跳板）或重校准 run | field, old, new, source: user \| system | missions, profile |

### 任务与课程

| type | 产生者 | payload 要点 | 更新投影 |
| --- | --- | --- | --- |
| `TaskGenerated` | 任务生成 run | task_id, title, judge, minutes, stage, **date**（任务归属日，按用户时区；预生成明日任务、补任务时不能从 created_at 推断）, seed_ref（来自哪次 Review） | tasks |
| `TaskDowngraded` | 用户点「太难了 / 没时间」 | task_id, reason, new_task | tasks, profile（难度信号） |
| `LessonPublished` | 内容管线 run | task_id, content_key, cache_hit | tasks / 各投影中的内容引用；**artifact 本体在 `content_cache`（事实源，replay 不清），事件只携带 content_key 指针** |

### 练习与提交

| type | 产生者 | payload 要点 | 更新投影 |
| --- | --- | --- | --- |
| `ExerciseRunRecorded` | 练习运行时（无 LLM） | task_id, code_hash, status, cases[], judge_hit, duration_ms | exercise_runs |
| `EvidenceSubmitted` | 用户提交 | evidence_id, task_id, **code**（用户代码全文入 payload；练习代码体量小，事件即事实源，Review 组上下文、导出、删除都从这里走；v1 引入 repo 级产出时再设 artifact 表）, code_hash, exercise_run_ref, reflection, uncertainty, review_style | evidence（状态：草稿→待 Review） |

### Review 与画像

| type | 产生者 | payload 要点 | 更新投影 |
| --- | --- | --- | --- |
| `ReviewCompleted` | Review run | 模型输出契约见 `schemas/review-output.schema.json`；事件 payload 契约见 `schemas/review-completed.schema.json`（模型输出 + 调用方注入 evidence_id） | profile（能力/误解/记忆）, evidence（→已 Review）, 明日任务种子 |
| `UserEditedProfile` | 用户编辑画像 / 删除记忆 | field, op, old, new | profile（用户改动权威最高）+ 派生物失效标记 |
| `ProfileSummaryRefreshed` | 摘要刷新（小模型） | profile_version, summaries{surface → **text**}（摘要短文本直接入 payload，profile.summaries 投影可重建，无需另设 artifact 表） | profile.summaries；供 context_manifest 追溯 |

### 对话与偏好（交互面，stub 先行 + 最终事件兜底）

| type | 产生者 | payload 要点 | 更新投影 |
| --- | --- | --- | --- |
| `RewriteApplied` | 选中重写 | task_id, paragraph_anchor, kind（simple / analogy）, text（重写全文入 payload，保证 delta 投影可重建） | user_content_delta |
| `PreferenceRecorded` | 重写 / 对话中的偏好信号 | kind, value, stage: candidate \| confirmed | profile（≥2 次同类才 confirmed） |
| `ChatTurnLogged` | Drawer 对话结束后 | conversation_id, role, text, quote?, surface_ctx | conversations |
| `CraftGenerated` | 作品化 | evidence_id, kind（resume / portfolio / interview / article）, content（作品全文入 payload——体量小，保证 crafts 投影可重建） | crafts, evidence（→可展示） |

### 节律与数据主权

| type | 产生者 | payload 要点 | 更新投影 |
| --- | --- | --- | --- |
| `DayCompleted` | 完成循环（`POST /api/tasks/:id/complete`） | date, task_id | **tasks（当日 task → done）**+ profile 的 rhythm 段（streak，含宽容规则；节律不设独立表，嵌在 profile 内） |
| `ReentryTaskIssued` | 断更回归 run | gap_days, task, date（任务归属日） | tasks, profile 的 rhythm 段 |
| `DataExported` | 用户导出 | format, scope | 审计 |
| `AccountDeletionRequested` | 用户删除 | — | 触发硬删除特权流程（见上方"唯一例外"规则）：投影/delta/摘要 → 事实类行 → events → users，最后留无内容审计 |

## 投影清单

**投影**（可由事件流重建，更新以 `event_id` 幂等、重放不二次生效）：`runs`、`missions`、`tasks`、`user_content_delta`、`exercise_runs`、`evidence`、`profile`（含 rhythm 段与分面摘要）、`conversations`、`crafts`。

**事实类 / 操作类表**（不是投影，`lites replay` 不清除）：`events` 本身、`idempotency_keys`（请求重放响应）、`jobs`（执行队列历史）、`llm_ledger`（花费事实，含 pending/unknown 状态）、`content_cache` 的 artifact 本体（`LessonPublished` 只携带 `content_key` 指针，对齐平台"EventStore 不保存文件本体"原则）。

## Do / Don't

| 应该 | 不应该 |
| --- | --- |
| 新事件先改本文件再写代码 | 代码里随手发明事件名 |
| payload 变更升 schema_version | 原地改旧事件结构 |
| 用户改动标 source，权威最高 | 系统悄悄覆盖用户编辑 |
| LLM client 负责 pre-call ledger；Run 事件只引用 `ledger_attempt_keys` | 把 ledger 做回事件投影，或把用量内嵌进 Run 事件 |
