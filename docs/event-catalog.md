# Event Catalog（v0 数据契约）

> 这篇是 v0 的第一份冻结契约：系统里会发生哪些 event、每条长什么样。事件语义是 v0 唯一"不许欠债"的东西（见 [v0-product-slice.md](./v0-product-slice.md)），所以先冻结这一页，再写 migration。新增或修改事件类型，必须先改本文件。

几个词先说清：

- **envelope**：每条 event 都有的公共字段。
- **payload**：每种事件自己携带的数据。
- **投影**：消费 event 计算出来的当前状态表（任务列表、画像等），永远可以从事件流重建。

## 命名与版本规范

- 事件名用 PascalCase 的"名词 + 完成动作"：`TaskGenerated`、`EvidenceSubmitted`（与平台文档的 `RunAccepted` 风格一致）。
- 事件一旦提交不修改、不删除；payload 结构要变，就升 `schema_version`，投影逻辑同时兼容新旧版本。
- 每条事件由一个 command / 请求产生，`command_id` 去重保证 at-least-once 投递下不重复落账。

## Envelope

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `event_id` | ULID | 主键，天然按时间有序 |
| `seq` | bigint | **用户内提交顺序号**（v0 的顺序边界收敛到 user，对应平台契约里 conversation-scoped 的 `seq`）。SSE 补拉与客户端去重的游标：客户端记 `last_seen_seq`，按 `seq` 去重，语义对齐 [realtime.md](./realtime.md) |
| `type` | string | 事件类型（下表） |
| `schema_version` | int | payload 结构版本，从 1 开始 |
| `user_id` | string | 所属用户（v0 的隔离边界与顺序边界） |
| `mission_id` / `task_id` / `run_id` | string? | 关联键，按需填 |
| `command_id` | string? | 产生本事件的 command（inbox 去重与追溯）；由用户请求直接产生的事件填该请求的 idempotency key |
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
| `RunAccepted` | `run_type`（diagnosis / task_gen / lesson_gen / review / reentry）, `input_ref` | runs |
| `RunQueued` / `RunStarted` | — / `attempt_id` | runs |
| `RunSucceeded` / `RunFailed` / `RunExpired` | `error?`, `llm_usage?`（tok_in / tok_out / cost / surface）；`RunExpired` 由 sweeper 依 `due_at` 产生 | runs + llm_ledger |

### 目标与路线

| type | 产生者 | payload 要点 | 更新投影 |
| --- | --- | --- | --- |
| `MissionCreated` | 诊断 run | name, current, target, roadmap[], adv[], gaps[], bridge | missions, profile 初始化 |
| `MissionAdjusted` | 用户纠正（删/补标签、改跳板）或重校准 run | field, old, new, source: user \| system | missions, profile |

### 任务与课程

| type | 产生者 | payload 要点 | 更新投影 |
| --- | --- | --- | --- |
| `TaskGenerated` | 任务生成 run | task_id, title, judge, minutes, stage, seed_ref（来自哪次 Review） | tasks |
| `TaskDowngraded` | 用户点「太难了 / 没时间」 | task_id, reason, new_task | tasks, profile（难度信号） |
| `LessonPublished` | 内容管线 run | task_id, content_key, cache_hit, artifact_ref | content_cache |

### 练习与提交

| type | 产生者 | payload 要点 | 更新投影 |
| --- | --- | --- | --- |
| `ExerciseRunRecorded` | 练习运行时（无 LLM） | task_id, code_hash, status, cases[], judge_hit, duration_ms | exercise_runs |
| `EvidenceSubmitted` | 用户提交 | evidence_id, task_id, code_ref, exercise_run_ref, reflection, uncertainty, review_style | evidence（状态：草稿→待 Review） |

### Review 与画像

| type | 产生者 | payload 要点 | 更新投影 |
| --- | --- | --- | --- |
| `ReviewCompleted` | Review run | payload 契约见 `schemas/review-completed.schema.json`（did[], fix, next, cap_delta, misconception?, memo, next_task_seed；evidence_id 调用方注入） | profile（能力/误解/记忆）, evidence（→已 Review）, 明日任务种子 |
| `UserEditedProfile` | 用户编辑画像 / 删除记忆 | field, op, old, new | profile（用户改动权威最高）+ 派生物失效标记 |
| `ProfileSummaryRefreshed` | 摘要刷新（小模型） | profile_version, summaries{surface → text_ref} | prompt 前缀缓存；供 context_manifest 追溯 |

### 对话与偏好（交互面，事后记账）

| type | 产生者 | payload 要点 | 更新投影 |
| --- | --- | --- | --- |
| `RewriteApplied` | 选中重写 | task_id, paragraph_anchor, kind（simple / analogy）, delta_ref | user_content_delta |
| `PreferenceRecorded` | 重写 / 对话中的偏好信号 | kind, value, stage: candidate \| confirmed | profile（≥2 次同类才 confirmed） |
| `ChatTurnLogged` | Drawer 对话结束后 | conversation_id, role, text, quote?, surface_ctx | conversations |
| `CraftGenerated` | 作品化 | evidence_id, kind（resume / portfolio / interview / article）, artifact_ref | crafts, evidence（→可展示） |

### 节律与数据主权

| type | 产生者 | payload 要点 | 更新投影 |
| --- | --- | --- | --- |
| `DayCompleted` | 完成循环 | date, task_id | rhythm（streak，含宽容规则） |
| `ReentryTaskIssued` | 断更回归 run | gap_days, task | tasks, rhythm |
| `DataExported` | 用户导出 | format, scope | 审计 |
| `AccountDeletionRequested` | 用户删除 | — | 触发数据与派生物清除流程 |

## 投影清单

`runs`、`llm_ledger`、`missions`、`tasks`、`content_cache`、`user_content_delta`、`exercise_runs`、`evidence`、`profile`、`conversations`、`crafts`、`rhythm`。全部可由事件流重建；投影更新以 `event_id` 幂等（重放不二次生效）。

## Do / Don't

| 应该 | 不应该 |
| --- | --- |
| 新事件先改本文件再写代码 | 代码里随手发明事件名 |
| payload 变更升 schema_version | 原地改旧事件结构 |
| 用户改动标 source，权威最高 | 系统悄悄覆盖用户编辑 |
| LLM 用量跟着 Run 事件落账 | 单独一套计量与事件对不上 |
