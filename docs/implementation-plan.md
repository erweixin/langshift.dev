# 实施计划：从工程初始化到完整 Cloud Agent 应用

> 全程 8 个阶段。每个阶段按固定结构展开：**设计工作**（数据库 / API / Prompt / UI，先设计后编码）→ **开发任务** → **测试任务** → **验收标准**。它和 [implementation-status.md](./implementation-status.md) 的分工：本文管"全程怎么走"，看板管"现在走到哪"。阶段 0–5 对应 v0 的 M0/M1/M2，阶段 6–7 由升级信号触发。
>
> 技术栈（已拍板，见看板决策表）：Go 单二进制 + PostgreSQL（pgx + goose）+ React TS（streamdown / CodeMirror 6 / 自建设计系统）+ 邮箱验证码登录 + anthropic-sdk-go。

## 总览

| 阶段 | 名称 | 里程碑 | 预估 | 核心设计工作 |
| --- | --- | --- | --- | --- |
| 0 | 工程初始化 | — | 2–3 天 | 工程规范、事件 / 幂等 / 队列表 |
| 1 | 执行内核 | M0 | 2 周 | 执行域数据库设计、内部接口设计、Review prompt v1 |
| 2 | 账户与登录 | — | 3–5 天 | 账户域数据库设计、Auth API 设计 |
| 3 | Daily loop 服务端 | M1 前半 | 3 周 | **业务域全量数据库设计、REST API 全量设计、全部业务 Prompt 设计** |
| 4 | 前端应用 | M1 后半 | 3 周 | 设计系统、页面 IA、组件清单 |
| 5 | 内测就绪 + 14 天实验 | M2 | 2 周 + 14 天 | 数据主权流程设计、成本面板设计、部署设计 |
| 6 | 平台加固 | v1 | 信号触发 | 多租户改造设计（RLS）、outbox / 修复审计表 |
| 7 | 完整 Cloud Agent 形态 | v1+ | 信号触发 | 沙箱基础设施、工具/审批域设计、多 provider 路由 |

### 数据库演进地图（哪张表在哪个阶段出现）

| 阶段 | migration | 新增/变更的表 |
| --- | --- | --- |
| 0 | 0001 | `events`、`event_cursors`（seq 游标）、`idempotency_keys`（作用域化幂等映射）、`jobs` |
| 1 | 0002 | `runs`、`llm_ledger`、`evidence`（最小版） |
| 2 | 0003 | `users`、`login_codes`、`sessions`、`invites` |
| 3 | 0004+ | `missions`、`tasks`、`content_cache`（artifact 本体，**事实源**）、`user_content_delta`、`exercise_runs`、`profile`（含 rhythm 段）、`conversations`、`crafts`；`evidence` 扩展 |
| 5 | 000N | 预算配置、`erasure_audit`（系统级删除审计：无用户内容、不随删除清除）；删除任务复用 jobs |
| 6 | 000N | 全表加 `tenant_id` + RLS policy、`outbox`、`repair_audit` |
| 7 | 000N | `tool_descriptors`、`tool_calls`、`approvals`、`sandbox_sessions`、`provider_registry` |

### Prompt 演进地图

| Prompt | 调用面 / 档位 | 设计于 | 备注 |
| --- | --- | --- | --- |
| review | Review / 强 | 阶段 1（v1）→ 阶段 3 迭代 | 模型输出锁 `schemas/review-output.schema.json`；事件 payload 锁 `schemas/review-completed.schema.json` |
| diagnosis | 冷启动诊断 / 强 | 阶段 3 | 产出 Mission + Roadmap 结构化输出 |
| lesson_plan / lesson_draft | 内容管线 / 中、强 | 阶段 3 | draft 输出锁 ContentArtifact 简化投影 |
| rewrite / chat | 交互面 / 小 | 阶段 3 | 流式 |
| craft / reentry / profile_summary | 作品化、断更、摘要 / 中、中、小 | 阶段 3 | |
| task_generation | 任务生成 / 中 | 阶段 3 | 消费 next_task_seed |
| validation_assist | 内容校验辅助 / 小 | 阶段 3 | 可选：结构 lint 辅助；硬校验以机器执行为主 |

所有 prompt 存 `backend/internal/llm/prompts/`（文件即版本，`prompt_version` 进内容缓存 key），配 golden set 用例。

---

## 阶段 0 · 工程初始化（2–3 天）

**目标**：一条命令起开发环境，一次 push 触发 CI，契约变成代码可消费的类型。

### 设计工作

- 工程规范：目录结构、错误处理约定（typed error + 分类）、日志字段约定（`run_id`/`user_id`/`surface` 必带）、配置格式。
- **数据库设计（migration 0001，四张基础表）**：
  - `events`：`event_id`(ULID, PK)、`seq`(bigint，per-user 连续)、`type`、`schema_version`、`user_id`、`mission_id?`、`task_id?`、`run_id?`、`command_id?`、`causation_id?`、`correlation_id?`、`created_at`、`payload`(jsonb)。约束：`UNIQUE(user_id, seq)`；`seq` 由事务内锁 `event_cursors` 行分配。
  - `event_cursors`：`user_id`(PK)、`current_seq`。**seq 分配不依赖阶段 2 的 users 表**——游标行按需创建（首次 append 时 upsert），单用户模式用固定 user_id。
  - `idempotency_keys`：`user_id`、`scope`（endpoint 标识）、`key`（客户端 Idempotency-Key）、`command_id`（映射到的服务端 ULID）、`request_hash`、`response_status`、`response_body`(jsonb)、`created_at`、`updated_at`；`UNIQUE(user_id, scope, key)`、`UNIQUE(command_id)`。**客户端 key 不直接当 command_id 用**——不同用户 / 不同端点的同名 key 互不冲突；API 成功但响应丢失时直接回放 `response_status/body`。
  - `jobs`：`job_id`、`command_id`(UNIQUE，服务端 ULID，去重)、`kind`、`subject_user_id?`（任务关联的用户；删除 job 完成前可临时指向目标用户，最终必须 scrub 为 NULL）、`payload`(jsonb)、`status`(queued/leased/done/failed/dead/cancelled)、`attempts`、`lease_until`、`lease_token`（**fence**：每次领取重新生成）、`leased_by`、`heartbeat_at`、`due_at`、`last_error`、`created_at`。Ack / Fail / Heartbeat / Append 必须携带 `JobFence{job_id, lease_token}` 且匹配，租约过期后旧 worker 的提交一律被拒——对齐 [state-machines.md](./state-machines.md) 的 fence 要求；worker 成功路径必须由 `EventService.Append` 在同一事务内追加事件并把当前 job 标记为 `done`，禁止"append 成功后再单独 ack"；账户删除按 `subject_user_id` 定位并清理所有状态的 payload。

### 开发任务

1. backend 目录：`cmd/lites/`、`internal/{event,job,run,llm,api,projection,auth}/`、`migrations/`。
2. `docker-compose.dev.yml`（Postgres 16）+ `Makefile`（`dev / test / migrate / typegen / lint`）。
3. goose 接入 + migration 0001。
4. 配置加载：`config/llm.yaml` + `.env.example`（`DATABASE_URL`、`ANTHROPIC_API_KEY`、发信服务、`LITES_SINGLE_USER`）。
5. `schemas/*.json` → TS 类型生成（json-schema-to-typescript），产物进 `frontend/src/contracts/`；Go struct 手写 + schema 一致性测试。
6. frontend 切 TypeScript，接入 TanStack Query / react-router，空壳可启动。
7. GitHub Actions：Go job（build+test+lint）、前端 job（tsc+build）。

### 验收标准

- [ ] 新机器 `git clone && make dev` 十分钟内起来。
- [ ] `make migrate` 建出 `events` / `event_cursors` / `idempotency_keys` / `jobs`；CI 在 PR 上全绿。
- [ ] 改 `schemas/*.json` → `make typegen` → 前端类型同步且 `tsc` 通过。

---

## 阶段 1 · 执行内核（2 周，= M0）

**目标**：一条真实链路端到端：提交 → Review run → 事件 → 投影 → SSE；崩溃后语义不破。M0 全程运行在单用户模式（`LITES_SINGLE_USER=1`，固定 user_id），登录到阶段 2 才引入。

### 设计工作

- **数据库设计（migration 0002）**：
  - `runs`（投影）：`run_id`、`user_id`、`run_type`(diagnosis/task_gen/lesson_gen/review/reentry)、`status`、`run_version`(int，**M0 起即做乐观锁校验**——worker 与 sweeper 天然并发，Run 级 CAS 只是一个带 WHERE 的 UPDATE)、`input_ref`、`error?`、`due_at`、时间戳。ToolCall 级 CAS 推迟到阶段 7 工具域引入时。
  - `llm_ledger`：`id`、`user_id`、`run_id?`、`surface`、`model`、`status`(pending / ok / failed_no_charge / provider_error / unknown / cancelled)、`tok_in`、`tok_out`、`max_tok_out`（pre-call 估算上限）、`cache_read`、`cost_usd`（实际或保守估算）、`estimated_cost_usd`、`cost_basis`(actual / estimated / zero)、`attempt_key`(UNIQUE)、**审计字段**（对齐 [execution-model.md](./execution-model.md) 的"输入清单、provider request id、结果可解释"要求）：`request_hash`（输入指纹）、`prompt_version`、`context_manifest`(jsonb，**直接内嵌**：prompt_version、profile_version、content_key、引用的事件 / artifact id——v0 不另设 manifest 表，这就是"解释调用为什么发生"的落地)、`provider_request_id?`、`error_code?`、`started_at`、`finished_at?`。账本不仅要能算钱，还要能解释一次调用为什么发生。**LLM 调用是花钱的外部副作用，记账遵循 pre-call 规则**：调用前先落 `pending` 行（独立事务，写入 `tok_in/max_tok_out/estimated_cost_usd/cost_basis=estimated`），返回后补 usage 置 `ok` 并改 `cost_basis=actual`；崩溃恢复时发现 `pending` 行 → 置 `unknown`，沿用 `estimated_cost_usd` 作为保守成本，重试用新 `attempt_key`。provider 无幂等键，无法保证绝不重复调用——保证的是**账本诚实：无重复落账、无静默丢账**。ledger 是 LLM client 在调用路径直接写入的**独立事实账本**（不是事件投影，不由事件驱动）；Run 事件 payload 只携带 `ledger_attempt_keys` 引用。状态语义：`failed_no_charge`＝请求未发出（本地校验 / 预算拦截，零成本，`cost_basis=zero`）；`provider_error`＝收到明确错误响应（默认零成本，按响应修正）；`unknown`＝已发出但无明确结局（保守计入估算成本）；`cancelled`＝发出前主动取消。**成本面板与告警只把 `ok + unknown` 计入花费**，工程失败不混入。
  - `evidence`（最小版）：`evidence_id`、`user_id`、`task_id?`、`status`(draft/reviewing/reviewed)、`payload`(jsonb)。
- **内部接口设计**（签名冻结再实现）：
  - `EventService.Append(ctx, AppendRequest) (AppendResult, error)`，其中 `AppendRequest{ Actor（user / worker / system）, CommandID?（system 写入必带；API / worker 禁止传）, Idempotency?{Scope, Key, RequestHash}（API 写入必带；重复 key 返回 `idempotency_keys.response_*`，key 同 body 异 → 409）, Aggregate?{RunID, ExpectedVersion}（Run CAS，可选）, JobFence?{JobID, LeaseToken}（worker 成功提交必带，对 jobs 行校验）, Events []EventDraft, Commands []CommandDraft{Kind, SubjectUserID, Payload} }`——幂等、CAS、fence 三个不变量**全部在 Append 边界内一次性执行**，不散落在 handler / worker 里。`command_id` 解析规则：API 写入来自 `idempotency_keys.command_id`；worker 写入来自当前 `job.command_id`；系统写入来自显式传入的 `CommandID`。单事务：校验幂等 / CAS / fence → 分配 seq → 插事件 → 更新投影 → 登记子 job（为每个 `CommandDraft` 生成新的服务端 `command_id`）→ 若带 `JobFence` 则把当前 job 标记为 `done` → 仅 API 幂等写入保存 idempotency response。
  - `JobQueue.Claim(ctx, kinds) (job, JobFence, error)`；`Heartbeat / Fail / Reschedule` 均要求携带 `JobFence{job_id, lease_token}`，不匹配即拒绝；成功 ack 通常由 `EventService.Append` 随事件提交原子完成，独立 `Ack` 只保留给确实不产出事件的 no-op / cancel job；`Worker` 接口 = `Handle(ctx, job, fence) ([]EventDraft, error)`，产出经 `EventService.Append`（带 fence）提交。
  - `LLMClient.Complete(ctx, surface, req) (resp, usage, error)`——内部完成分档路由、structured outputs、ledger 落账。
- **API 设计（本阶段最小集）**：`POST /api/evidence`、`GET /api/events?after_seq=`、`GET /api/stream`（SSE）。**写端点统一幂等约定（全程适用）**：所有写 event/job 的 POST/PATCH 必须携带 `Idempotency-Key` header，**作用域为 (user_id, endpoint)**——经 `idempotency_keys` 表映射到服务端生成的 `command_id`（ULID，全局唯一）；重复 key 返回首次响应，key 相同而 body 不同（`request_hash` 不符）返回 409。
- **Prompt 设计**：review v1（系统提示 + 用户产出模板 + structured output 锁 `schemas/review-output.schema.json`；写 `ReviewCompleted` 前注入 `evidence_id` 并按 `schemas/review-completed.schema.json` 校验事件 payload）。

### 开发任务

1. EventService（含 per-user seq 分配、投影分发器注册机制）。
2. JobQueue：`FOR UPDATE SKIP LOCKED` 领取、lease 心跳、超时回收、死信。
3. Run 状态机：转换表落码，**状态名**为 `accepted→queued→executing→succeeded/failed/expired`（对齐 [state-machines.md](./state-machines.md)；`RunStarted` 等是**事件名**，对齐 event-catalog，不作状态枚举），非法转换报错；**所有状态转换经 `run_version` 乐观锁提交，版本不匹配即放弃**；sweeper goroutine 按 `due_at` 收敛 → `RunExpired`。
4. LLM client + ledger（pre-call pending 行 → 返回补全 → 崩溃收敛 unknown）+ `attempt_key` 幂等。
5. Review worker：`EvidenceSubmitted` → 组装上下文（固定模板）→ 强档调用 → `ReviewCompleted`。
6. SSE hub：按 user 分发。重连遵循 [realtime.md](./realtime.md) 的**无竞态协议**：先订阅并缓冲 → 读当前 high-water seq（H）→ 补拉 `(last_seen, H]` → 应用缓冲（按 seq 去重）→ 接 live 流。禁止"先补拉后订阅"（补拉完成到订阅生效的间隙会丢事件）。
7. `lites replay`：清空**投影**→ 从 events 重建。事实类 / 操作类表不属于投影、replay 不清：`events`、`idempotency_keys`（请求重放响应）、`jobs`（队列历史）、`llm_ledger`（花费事实）、`content_cache` 的 artifact 本体（阶段 3 引入）。

### 测试任务

- 崩溃注入三点位（append 后 / claim 后 / LLM 返回后 kill -9），重启后断言：run 收敛；Append 成功的 job 已原子 `done`，不会再次执行；ledger 无重复落账、无静默丢账（LLM 返回后崩溃 → 存在被置为 `unknown` 的 pending 行，重试为新 attempt）。
- 同一 API `Idempotency-Key` 重放返回首次响应；同一 worker job command 因 lease/崩溃重试时不会二次推进投影。
- SSE 断连重连不丢不重。
- replay 结果与在线投影逐行 diff。
- Run CAS 竞态：worker 完成与 sweeper 过期同时提交同一 run，仅一方生效、终态不被覆盖。

### 验收标准

- [ ] curl 提交 Evidence → 数秒内 SSE 收到 `ReviewCompleted`，投影可查。
- [ ] 四组测试任务全部进 CI 并通过。

---

## 阶段 2 · 账户与登录（3–5 天）

**目标**：邮箱验证码 + 邀请码；一切数据带 user 边界；自部署单用户免登录。

### 设计工作

- **数据库设计（migration 0003）**：
  - `users`：`user_id`、`email`(UNIQUE)、`created_at`。
  - `login_codes`：`email`、`code_hash`、`expires_at`、`attempts`。
  - `sessions`：`token_hash`(PK)、`user_id`、`expires_at`、`created_at`。
  - `invites`：`code`(PK)、`used_by?`、`created_at`。
- **API 设计**：`POST /auth/request-code`、`POST /auth/verify`（携邀请码注册）、`POST /auth/logout`、`GET /api/me`。
- 发信接口设计：`Mailer` 接口 + 两个实现（dev=控制台打印、prod=所选服务）。

### 开发任务

1. 四张表 + auth handler + session cookie（HttpOnly/SameSite）。
2. 中间件：注入 `user_id`；`LITES_SINGLE_USER=1` 跳过登录固定用户。
3. 限流：每邮箱每小时 N 次验证码；验证码 6 位、10 分钟过期、5 次错误作废。

### 测试任务 / 验收标准

- [ ] 全流程集成测试：请求码 → 验证 → 会话生效；错码/过期码被拒。
- [ ] 越权测试：用户 A 的任何 API 读不到用户 B 的事件与投影。
- [ ] 单用户模式启动直接进应用。

---

## 阶段 3 · Daily loop 服务端（3 周，= M1 前半）

**目标**：[product-to-platform.md](./product-to-platform.md) 的 10 个产品动作全部有真实服务端实现。**这是业务域设计最重的阶段。**

### 设计工作

- **数据库设计（migration 0004+，业务域全量）**：
  - `missions`：`mission_id`、`user_id`、`name`、`current`、`target`、`roadmap`(jsonb)、`adv/gaps/bridge`(jsonb)、`status`。
  - `tasks`：`task_id`、`user_id`、`mission_id`、`task_template_id?`、`title`、`judge`、`minutes`、`stage`、`status`(pending/active/done/downgraded)、`seed_ref?`、`date`。
  - `content_cache`：`content_key`(PK)、`artifact`(jsonb，含 reference_solution，仅服务端)、`artifact_hash`、**cache key 全维度显式落列**（灰度 / 回滚 / 审计都要按它们查询）：`task_template_id`、`target_stack`、`level_band`、`content_version`、`prompt_version`；`review_status`(auto_ok / needs_review / human_ok / rejected——人审队列即 `needs_review` 行的列表)、`validation_attempts`、`created_at`。**事实源而非投影**：artifact 本体只存在于此，`LessonPublished.content_key` 指向它（对齐平台"EventStore 不保存文件本体"原则）；`lites replay` 不清此表，只重建各投影里对它的引用。
  - `user_content_delta`：`user_id`、`task_id`、`paragraph_anchor`、`kind`、`text`、`created_at`。
  - `exercise_runs`：`id`、`user_id`、`task_id`、`code_hash`、`result`(jsonb，锁 exercise-result schema)、`judge_hit`、`created_at`。
  - `evidence` 扩展：`code`（用户代码全文，来自 `EvidenceSubmitted` payload——事件即代码快照的事实源）、`code_hash`、`exercise_run_ref`、`reflection`、`uncertainty`、`review_style`、状态加 `showcase`。
  - `profile`：`user_id`(PK)、`version`、`capabilities`(jsonb)、`misconceptions`(jsonb)、`preferences`(jsonb)、`rhythm`(jsonb)、`summaries`(jsonb，分面摘要)。结构锁 [learner-profile.md](./learner-profile.md) schema。
  - `conversations`：`conversation_id`、`user_id`、`turns`(jsonb 数组)。
  - `crafts`：`craft_id`、`evidence_id`、`kind`、`content`、`created_at`。
- **API 设计（REST 全量，锁定后前端并行开发；全部写端点遵循阶段 1 的 `Idempotency-Key` 约定）**：

  | 端点 | 动作 | 链路 |
  | --- | --- | --- |
  | `POST /api/onboarding` | 冷启动诊断 | run，SSE 跟进度 |
  | `POST /api/missions/:id/adjust` | 确认屏纠正 | 直写 event |
  | `GET /api/today` | 今日视图（任务+完成态） | 投影读 |
  | `POST /api/tasks/:id/downgrade` | 太难了/没时间 | 直写 event |
  | `POST /api/tasks/:id/complete` | 完成今日循环：写 `DayCompleted`（task→done + rhythm）并预创建明日任务生成 run | 直写 event + run |
  | `GET /api/tasks/:id/lesson` | **只读**：课程内容或生成状态（已剥离参考解），无任何副作用 | 事实读 |
  | `POST /api/tasks/:id/lesson-runs` | 手动触发 / 重试生成。常规路径不走这里：任务生成完成时服务端**自动预创建** lesson run | run |
  | `POST /api/tasks/:id/rewrite` | 选中重写 | 直连流式 |
  | `POST /api/chat` | Drawer 对话 | 直连流式 |
  | `POST /api/exercise-runs` | 练习结果上报 | 直写 event |
  | `POST /api/evidence` / `GET /api/evidence` | 提交 / 列表 | event + 投影 |
  | `POST /api/evidence/:id/craft` | 作品化 | **直连流式**（内容小、用户在等结果；不占 run_type） |
  | `GET /api/profile` / `PATCH /api/profile` | 画像查看 / 用户编辑 | 投影 + event |
  | `GET /api/map` / `GET /api/week` | 能力地图 / 周报 | 投影读 |

- **Prompt 设计（以 Prompt 演进地图逐行为准：除阶段 1 已做的 review v1 外共 10 个，含可选的 validation_assist；另含 review 迭代）**：每个 prompt 定：系统提示、上下文组装清单（对齐 product-to-platform 的"谁进 prompt"表）、输出 schema、golden set 用例（≥3 例/prompt）。golden set 覆盖以地图行数为验收口径，不按记忆里的数字。
- **内容管线模块设计**：`taskspec → planner → drafter → validator → publisher` 五个接口；**validator 的 JS 执行选型在此拍板**（Node 子进程复用前端 harness vs Go 内嵌 goja），记入看板。

### 开发任务

1. 全部投影 reducer（消费 event 更新上述表；`ReviewCompleted` 的能力至多 +1、`UserEditedProfile` 优先级最高、幂等重放）。
2. 10 个动作的 handler / worker（对照 API 表逐个实现；交互面动作遵循 product-to-platform 的"stub 先行"三步协议）。
3. 内容管线五模块 + 失败原因回灌重试（≤2 次）+ 人审状态位；D1 人工课稿 seed 进 content_cache。
4. 分面摘要刷新 worker（画像变更触发）。
5. curriculum-14d 的 TaskSpec 数据落库（seed 脚本）。

### 测试任务

- 管线对 D1–D3 真实生成，通过率记入看板 R1；注入坏 draft（测试挂不掉起始代码）断言被 validator 拦下。
- 缓存命中测试：同 band 第二用户零生成调用。
- 画像不变量：Review +2 被钳到 +1；用户编辑后系统更新不覆盖。
- 重写 delta：写入 → 重取课程 → delta 已重放。

### 验收标准

- [ ] 用 API 脚本依次驱动 10 个动作，事件流 / 投影 / ledger 全部符合 [event-catalog.md](./event-catalog.md)。
- [ ] 上述四组测试进 CI；管线实测通过率已回填看板。

---

## 阶段 4 · 前端应用与设计系统（3 周，= M1 后半）

**目标**：v0 屏清单全部真实可用；交互对齐原型，视觉走自建设计系统。

### 设计工作

- **设计系统**（前置，准备清单项）：token（色板/字号/间距/圆角/阴影/动效时长）→ 基础组件规格：Button、Input、Card、Chip、Toast、Drawer、Progress、Modal、EmptyState。
- **页面 IA**：路由表 + 每屏数据依赖（哪个 API、哪些 SSE 事件）。
- 练习运行时协议已冻结（schemas），无需再设计。

### 开发任务

1. 组件库（上述 9 个组件 + 主题变量）。
2. 页面（对照 [product-ux-blueprint.md](./product-ux-blueprint.md) v0 屏清单）：onboarding → 生成屏（订阅 run 进度）→ 目标确认（内联纠正）→ 今日一步 → 课程页（streamdown + CodeMirror + 练习运行）→ 提交（自动带入）→ Review 等待态 + 结果 → 完成态（记忆卡真数据）→ 能力地图 / Evidence / 作品化 → Coach Drawer（停靠、流式）→ 选中即问/即改。
3. 练习运行时 v0：Web Worker + 2s 看门狗 + API 屏蔽 + 结果上报。
4. SSE hook：实现阶段 1 定义的无竞态重连协议（订阅缓冲 → high-water → 补拉 → 去重合流）+ TanStack Query 失效联动。
5. 学习状态按 task 隔离；重写 delta 重渲染重放。

### 测试任务 / 验收标准

- [ ] 创始人本人在正式前端连续跑通 3 天完整 loop（M1 验证项）。
- [ ] 死循环练习代码 2s 被终止、页面不卡、可重跑。
- [ ] 断网 10s 恢复后界面状态正确、无重复渲染。
- [ ] Drawer 提问自动带当前任务上下文；重写 delta 离开再回仍在。
- [ ] 全站无原型样式残留；键盘可完成主流程。

---

## 阶段 5 · 内测就绪与 14 天实验（2 周 + 14 天，= M2）

**目标**：v0 DoD 全绿，拿到留存与成本的真实数据。

### 设计工作

- 数据主权流程设计：导出内容清单（事件流 + 投影 + 人读 Markdown）。**删除策略（已拍板）：v0 硬删除**——按 `user_id` 物理删除 events 与全部表行，这是 append-only 语义的唯一特权例外（规则见 [event-catalog.md](./event-catalog.md)）；执行顺序：先冻结该用户写入入口并取消 / 跳过该用户所有非 erasure job → 投影 / delta / 摘要 → 事实类行（ledger、evidence 代码快照、idempotency response）→ `sessions` / `login_codes` / `invites` 关联行 → `jobs` 中 `subject_user_id` 命中的所有 payload（queued/leased/done/failed/dead/cancelled 全状态）→ `event_cursors` → events → users。**执行边界**：删除 job 本身以系统身份运行；执行期间可临时持有目标 `user_id` 用于重启恢复，但完成事务必须把该 job 的 `subject_user_id`、payload、last_error 中的目标标识全部 scrub，只保留无内容的 job 终态；`erasure_audit` 是系统级证据表——两者都不在删除范围内，避免"删用户数据把删除任务和审计证据也删没"。最后写入审计（**标识一律哈希化**：邮箱、user_id 都不存明文，否则"全库按 user 检索无残留"的 DoD 会被审计行自己打破）。v1 演进为 per-user 加密 + 销毁密钥。
- 成本面板设计：四条曲线（cost/DAU、缓存命中率、面占比、Top 用户）的 SQL 视图 + 简单 admin 页；降级阶梯的触发与 UI 告知文案。
- 部署设计：目标环境（看板待拍板）、部署脚本、`pg_dump` 备份 cron、密钥管理。

### 开发任务

1. 导出拆读写：`POST /api/exports`（创建导出、写 `DataExported` 审计事件，遵循幂等约定）→ `GET /api/exports/:id/download`（纯下载，无副作用）；`DELETE /api/account`（删除任务走 jobs，逐项清派生物）。
2. 预算护栏：每用户日预算检查 + 降级阶梯（Review 永不降档）+ UI 告知。
3. streak 宽容规则 + 断更回归全流程接通。
4. 部署脚本 + 发信服务接入 + 备份 cron + 隐私说明一页。
5. 实验运行：3–5 名内测者接入；埋点漏斗（开始任务 → 跑通测试 → 提交 → 看完 Review → 次日回访）。

### 验收标准

- [ ] [v0-product-slice.md](./v0-product-slice.md) DoD 七项全绿。
- [ ] 删除账户后全库按 user 检索无残留；导出包可独立阅读。
- [ ] 面板与 ledger 对账一致；超预算触发降级且 UI 有告知。
- [ ] 14 天实验复盘文档：留存漏斗、实测成本（回填 R2）、管线通过率、Top3 产品问题；开源 public 决策完成。

---

## 阶段 6 · 平台加固（v1，信号触发）

**触发信号**：内测 >50 或付费出现；Review 排队变慢；数据不可承受丢失。

### 设计工作

- **多租户改造设计**：全表加 `tenant_id`（envelope 预留位落列）、RLS policy 清单、受限 DB role。
- `outbox` 表（事务内登记、after-commit 发布）与独立 publisher；`repair_audit` 表 + Repair Command API 动作清单（cancel / redrive / 终态裁定）。
- 可观测性设计：trace 关联链（request → run → command → attempt → llm_attempt）、低基数指标清单、SLO。

### 开发任务

1. worker 拆独立进程、outbox publisher（Run 级 CAS 自 M0 已启用；ToolCall 级 CAS 随阶段 7 工具域引入）。
2. RLS 上线 + 越权回归测试集。
3. Repair Command API + admin 操作全走审计。
4. trace/metrics 接入；PITR 演练 + `store_epoch` 落列与校验。
5. （若付费信号）订阅管理 + 免费层限额执行。

### 验收标准

- [ ] 双 worker 并行：取消竞态 / 并行完成竞态测试通过，终态不被覆盖。
- [ ] 应用层校验故意关闭时，RLS 仍拦住跨租户访问。
- [ ] PITR 恢复后旧 command 被 `store_epoch` 拒绝并留审计。
- [ ] 任一 run 可从 API 请求追到 LLM attempt 的完整 trace。

---

## 阶段 7 · 完整 Cloud Agent 形态（v1+，信号触发）

**触发信号**：课程进入真实 Go 阶段；第二条路线；Coach 需要真实工具执行。

### 设计工作

- **沙箱基础设施**：exercise 信任档规格（rootless + seccomp + 限额）、预热池容量模型、Go/Python harness 协议（对齐 [exercise-runtime.md](./exercise-runtime.md) v1）。
- **工具/审批域数据库**：`tool_descriptors`（schema/权限/副作用能力/版本）、`tool_calls`（状态机 + `tool_call_version`）、`approvals`、`sandbox_sessions`、effect ledger 启用。
- 多 provider 路由设计（[llm-provider.md](./llm-provider.md)：别名、降级链、熔断）。

### 开发任务

1. 服务端练习沙箱：容器池 + Go/Python harness + 结果统一为 ExerciseResult。
2. ToolWorker + guardrails 检查点（输入/工具调用/输出）+ 高危工具审批流 + `outcome_unknown` 对账。
3. LLM Gateway 多 provider：注册、路由、降级、熔断、租户策略。
4. 第二条路线：curriculum 模板化 + level_band 分档；记忆深化（向量情景记忆接入 Drawer 召回、画像衰减 → 复习探针）。

### 验收标准

- [ ] Go 练习服务端运行 p50 ≤ 1.5s；网络/文件/资源逃逸测试全部被拦。
- [ ] 带真实副作用的工具：审批生效、超时进对账不盲重试。
- [ ] 主 provider 故障注入自动降级，用户侧仅表现为变慢。
- [ ] 第二条路线从模板到可学习，内容管线零代码改动。
- [ ] 对照 [mvp-scope.md](./mvp-scope.md) v1 DoD 逐项核验，未覆盖项记入看板。

---

## 全程纪律

- 每阶段先做完"设计工作"再动编码；设计产物（DDL、API 表、prompt 文件）即评审对象。
- 新事件先改 [event-catalog.md](./event-catalog.md)、新契约先改 `schemas/`，然后才写代码。
- 不变量测试（崩溃注入 / 重复投递 / 重放一致性）每阶段结束全量跑一次。
- `llm_ledger` 从阶段 1 起是硬要求；任何阶段不得跳过成本落账。
- 好主意进看板 backlog，不插队。合并 PR 更新看板。
