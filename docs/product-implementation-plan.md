# Lites 成熟商业版完整实施计划（精简身份与收款依赖）

## 一、目标与已锁定边界

以 [README.md](../README.md) 的能力迁移理念、[architecture.md](./architecture.md) 的生产级 Cloud Agent 内核和 [ux-prototype.html](./ux-prototype.html) 的用户旅程为基准，一次性交付完整 GA，不发布中间 MVP。

文中的阶段只是内部工程门禁，不是对外版本。每个阶段都必须留下可重复检查的证据并通过固定门槛，才可以进入下一阶段；最终只发布一个完整 GA 版本。

已确定：

- 对外品牌统一为 **Lites**。
- 产品聚焦科技与 AI 职业迁移；Cloud Agent 是内部执行引擎。
- 海外优先，英文默认，完整支持简体中文。
- AGPL-3.0 + 商业双许可。
- 身份系统只实现内建的邮箱、密码、邮箱验证和密码重置。
- 不接入外部身份服务、社交登录、Passkey、MFA、SSO 或 SCIM。
- 不接入在线收款系统，也不实现在线购买、自动扣款、优惠券、税务和支付回调。
- 企业合同、席位和额度由管理后台人工配置，线下签约、结算。
- 官方云提供免费基础额度和 BYOK；额度用完后使用 BYOK 或联系销售。
- 保留清晰的身份和结算边界，未来可以扩展，但本轮不实现外部身份或支付适配器。

完整用户闭环为：

`Goal → Map → Learn → Practice → Create → Evidence → 下一轮校准`

## 二、完整产品能力

### 1. 匿名获客与账号转换

- 用户匿名填写当前角色、目标、经验和时间投入，确认由职业标签推断的能力。
- 匿名 Route Preview 只能访问角色目录和低风险模型，不使用长期 Memory、Workspace、BYOK、外部工具或企业数据。
- 匿名会话使用签名 HttpOnly Cookie 和独立 `anonymous_subject_id`，默认保留 24 小时。
- Identity Service 为每个 `anonymous_subject_id` 分配独立、不可猜且不可复用的 `ephemeral_user_id`。它没有密码、邮箱、membership 或登录能力，只用于受限系统租户内的 EventStore `(tenant_id, user_id)` 分区、幂等 scope 和 realtime cursor；不能借用任何已登录用户的 `user_id`，也不能访问其他匿名主体的数据。
- 匿名事件、outbox、idempotency response 和 `last_seen_seq` 都按 `(anonymous_system_tenant_id, ephemeral_user_id)` 隔离。Cookie 只保存签名后的匿名会话句柄，不暴露内部 principal；删除时清除正文 payload、preview projection 和 principal 映射，EventStore 只保留不含正文的最小审计事实与删除凭据。
- 用户可以先看到迁移判断、迁移桥、能力缺口、阶段路线和第一项任务。
- 点击“保存路线”或“开始任务”时进入邮箱密码注册。
- 邮箱验证成功后调用幂等 `claim` durable saga，将匿名路线的不可变 snapshot 复制到目标个人租户，创建或合并 Mission，最后删除匿名正文载荷。这个流程不依赖跨用户事务。
- saga 使用稳定 `claim_key = hash(anonymous_subject_id, route_revision_id, target_tenant_id)`；目标个人租户在 `mission_imports` 上对 `claim_key` 建立唯一约束，并记录最终 `mission_id`。已有账户登录时也先幂等插入该 import，再创建或合并 Mission，因此一个既有 Mission 可以接收多个不同来源的预览，但同一个 claim 只能导入一次。
- `AnonymousClaimStatus = available | reserved | destination_committed | erasing | claimed | expired | manual_review`。合法主路径是 `available → reserved → destination_committed → erasing → claimed`；只有 `available` 可以进入 `expired`。中间步骤失败时保留当前状态并通过 outbox 重试，结果未知时进入 `manual_review`，由对账或 Repair Command 回到已确认的步骤。
- `destination_committed` 必须记录目标 tenant、Mission、route revision 和 commit event；`erasing` 必须生成各存储面的 deletion receipt。只有全部 receipt 持久化后才能进入 `claimed`，清理器不得删除 `reserved` 之后的载荷。
- claim 与 24 小时清理并发时，以 EventStore 对匿名 claim version 的 CAS 为准；一旦 reserve 成功，过期命令必须无操作结束。Worker 在每个 saga 边界崩溃后都必须能够从已提交事件恢复，最终只留下一个目标 Mission 和一套完整删除凭据。

### 2. 正式账号体系

实现独立 Identity Service，但认证数据仍存储在 PostgreSQL 的隔离 schema 中。

支持流程：

- 邮箱密码注册、邮箱验证、登录和退出。
- 忘记密码、一次性重置链接、修改密码。
- 修改邮箱并重新验证。
- 查看当前会话、单独撤销会话、撤销其他全部会话。
- 邀请成员加入企业租户、接受/拒绝邀请、退出组织。
- 管理员单个邀请和 CSV 批量邀请/停用。
- 删除账号、导出数据和取消删除请求。

安全规则：

- 邮箱规范化后建立唯一索引，但登录/重置接口返回统一文案，避免账号枚举。
- 密码使用 Argon2id + 每用户随机 salt；可选 pepper 存于 Vault，不保存或加密可还原密码。[OWASP 密码存储建议](https://cheatsheetseries.owasp.org/cheatsheets/Password_Storage_Cheat_Sheet.html)
- 单因素密码最少 15 个字符，至少允许 64 个字符和 Unicode，不强制大小写/符号组合，不定期强制改密；拒绝常见及已泄漏密码。[NIST SP 800-63B](https://pages.nist.gov/800-63-4/sp800-63b.html)
- 登录按 IP、邮箱摘要和设备会话限速，连续失败采用递增等待；超过风险阈值再触发 CAPTCHA。
- 浏览器使用高熵 opaque session token，Cookie 设置 `Secure`、`HttpOnly`、`SameSite=Lax`；数据库只存 session token hash。
- 写操作使用 CSRF 防护；登录、改密、重置和权限变化后旋转 session。
- 邮箱验证 token 24 小时有效；密码重置 token 30 分钟有效；邀请 token 7 天有效；全部单次使用且只保存 hash。
- 密码重置后撤销其他所有会话，并发送安全通知。
- 登录、重置、邀请和邮件发送均记录安全审计，但不记录密码、token 或邮件正文。

账号和匿名转换领域对象：

```text
users
password_credentials
email_verifications
password_reset_requests
sessions
tenants
memberships
invitations
security_events
consent_records
account_erasure_requests
anonymous_subjects
onboarding_sessions
onboarding_claims
anonymous_erasure_receipts
```

每个用户自动获得个人租户，并可加入多个企业租户。Gateway 根据 session 和 membership 解析当前 tenant，再签发内部短期可信上下文；客户端不能自行声明租户或角色。

### 3. 成长产品

- Onboarding：角色选择/自然语言描述、能力证据确认、目标澄清。
- Route：迁移判断、可迁移经验、缺口、迁移桥、阶段路线和第一项任务。
- Today：今日唯一任务、选择原因、预计时间、本周节奏和 Coach 观察。
- Task：讲解、示例、代码/文字/设计实践、确定性测试、难度调整。
- Submit/Review：自动带入任务产出，记录用户理解，生成一项重点反馈和下一任务。
- Map：展示能力来源、迁移关系、验证状态和路线影响。
- Evidence：保存代码、文章、设计、复盘、项目和用户主动分享记录。
- Goal：多 Mission 管理。可执行 Mission 指 `MissionStatus=active`。Focus 作用域为 `(tenant_id, user_id)`；有可执行 Mission 时必须恰好一个 Focus，没有可执行 Mission 时允许为空。Focus 存在独立 `mission_focuses` 指针和 version，切换、暂停、完成与归档都通过 CAS 原子更新，不能在多条 Mission 上分别写布尔值。
- Coach：全局抽屉、选中即问、讲浅一点、换类比、检查理解。
- Create：项目 Workspace、里程碑、测试、制品、完整复盘和作品集导出。
- Settings：账户、安全、语言、时区、提醒、BYOK、Memory、数据导出和删除。

能力画像使用可解释的证据等级，不使用虚假精确百分数。能力声明和证据是两类不同对象：

- `capability_claim` 表示“系统或用户目前如何判断一项能力”，可以被确认、纠正、反驳、替代或撤回。
- `evidence` 表示“实际发生过什么”，例如用户确认、任务提交、测试结果、作品或 reviewer 记录；已经提交的证据不原地改写。
- 一项能力可以关联多条支持或反驳证据。路线只读取当前有效的 claim revision，同时保留过去为什么改变的完整历史。

能力声明的验证等级为：

```text
inferred
→ user_confirmed
→ demonstrated
→ applied
→ reviewer_verified
```

任何能力升级必须引用用户确认、任务、测试、作品或 reviewer 记录。用户删除推断、指出迁移关系不准确或新证据与旧判断冲突时，必须创建新的 claim/route revision，不能悄悄覆盖旧结论。

### 4. 企业能力与默认隐私

企业仍通过邮箱邀请和后台人工管理，不依赖 SSO/SCIM。

角色：

```text
owner
admin
contract_admin
program_manager
reviewer
member
```

- 企业可以创建 Program、Cohort、角色包、任务包和共享额度池。
- 成员导入、停用和离职通过单个操作或 CSV 批量处理。
- 管理员默认只能查看席位、资源用量和预先定义的聚合趋势。聚合查询只允许固定指标、allowlist 维度和粗粒度时间桶，不提供任意 SQL、任意交叉筛选或逐成员 drill-down。
- 每个 aggregate snapshot 对结果 cell 和相邻补集都执行 `k >= 5`，使用跨查询稳定 suppression；同一 snapshot 的重复查询返回同一结果，并按 tenant 记录 query budget。若某个指标必须开放更灵活的切片，则必须在阶段 1 冻结差分隐私机制及 `epsilon/delta`，不能只依赖人数阈值。
- 对话、反思、能力证据和作品默认 `private`。
- reviewer 只能查看用户显式创建的 `share_grant`。
- 企业不能使用 Lites 进行招聘筛选、晋升、解雇、员工排名、自动任务分配或其他影响职业权益的决策。
- 离开企业租户不会影响个人租户；跨租户内容通过显式导出/导入复制，不共享可变对象。
- 本轮没有 MFA，因此合同变更、BYOK 管理、数据导出和 Repair Command 不能只依赖普通登录态：操作前必须重新输入密码，重新认证结果最多有效 5 分钟。官方云的运维入口另外受独立网络入口和机器身份保护。
- Repair Command 和合同生效实行 two-person rule：发起人不得审批；必须由两个不同、仍处于 active 状态且具备对应权限的账号分别重新认证并批准。审批绑定不可变 proposal hash、目标资源 version 和过期时间；proposal、权限、账号状态或目标 version 变化后全部旧批准失效，不能用同一主体、同一 session 或重复请求凑够两票。

## 三、商业模式与线下合同

交付形态：

| 形态 | 能力 |
| --- | --- |
| Community Self-hosted | 完整 AGPL 功能、BYOK、Docker Compose、社区支持、默认无遥测 |
| Lites Cloud Personal | 官方托管、免费基础额度、BYOK、个人成长空间 |
| Lites Enterprise | 年度线下合同、席位、共享额度、Program/Cohort、审计、区域固定、私有化、商业许可、SLA 和支持 |

本轮不实现付款，只实现商业产品运行所需的合同与权益台账：

```text
contracts
contract_entitlements
seat_allocations
credit_buckets
usage_reservations
usage_ledger
provider_costs
manual_adjustments
contract_audit_events
```

规则：

- 合同由授权运维人员在 Admin Console 创建、续期、暂停或终止。
- 所有合同变更要求 reason、操作者和审批记录，不能直接改数据库。
- LLM 和工具调用前执行 `reserve`，完成后 `settle/release`。
- BYOK 不记平台模型费用，但仍记录计算、存储和工具资源用量。
- 免费额度、企业共享额度、硬上限、软告警和到期时间全部配置化。
- 不实现 checkout、payment intent、card、invoice collection、tax、refund、coupon 或支付 webhook。
- 将来支付接入只能消费本地合同和 usage ledger，不能成为 Agent 配额的事实源。

双许可同时需要 CLA、商标政策、贡献指南、安全披露、SBOM、签名镜像、构建来源证明，以及 UI 中的 AGPL Appropriate Legal Notice。

## 四、技术实现

### 1. 技术基线

- Web：TypeScript + Next.js、设计系统、国际化、PWA、OpenAPI 生成客户端。
- 服务端：Go。
- 数据库：HA PostgreSQL；身份、产品、Agent、合同按 schema 和 DB role 隔离。
- 队列：NATS JetStream durable pull consumers；业务仍按 outbox/inbox 去重。
- 缓存与限流：Valkey，只保存可丢弃缓存、限流计数和临时挑战。
- Payload/Artifact：S3-compatible 对象存储，内容寻址、加密、扫描和 ACL。
- Retrieval：独立 PostgreSQL + pgvector + 全文检索投影。
- Secret：Vault 或兼容云 Secrets Manager，用于 BYOK、session pepper 和服务凭证。
- 邮件：标准 SMTP 接口；官方云和自托管仅需配置 SMTP，不绑定邮件供应商。
- Runtime：可信任务用受限容器，用户代码使用专用 Linux 节点上的 Firecracker microVM。
- 部署：Docker Compose 单租户发行版；Helm + OpenTofu 生产部署。
- 可观测：OpenTelemetry、Prometheus、Grafana、Tempo、Loki 和独立审计存储。
- 官方云使用厂商中立的 US/EU 独立 Cell，单区域单写、多可用区部署。

### 2. Agent 执行内核

严格实现现有专题架构：

- PostgreSQL EventStore 是事实源；事件、projection、outbox 和 idempotency response 同事务提交。
- Run、ToolCall、Command、Attempt、Approval 和 Workspace Revision 使用显式状态机。
- Worker 提交必须匹配 version、command、attempt、精确 fence、lease token 和有效期。
- NATS 只做至少一次投递；Worker 使用 inbox 去重，提交事务后才 ack。
- 外部副作用按能力分类，`outcome_unknown` 必须对账或人工裁定。
- Workspace 写入执行 prepared revision → durable authorization → CAS publish → confirmed。
- SSE 使用 `last_seen_seq` 补拉，流连接不作为事实源。
- Provider Gateway 支持 OpenAI、Anthropic、Google 和 OpenAI-compatible endpoint，并允许租户 BYOK。自定义 endpoint 必须经过目的地址 allowlist、DNS/IP 解析、redirect 和私网地址检查；禁止访问 loopback、link-local、云 metadata 和平台内网。BYOK secret 只能发给它绑定的 Provider host。
- Agent Profile 固定为 `route_planner`、`daily_planner`、`coach`、`evaluator`、`artifact_builder`。
- 所有模型输入记录 `context_manifest`；Memory 写入必须通过受治理的 `memory_write` ToolCall。
- 内置工具包括角色/内容检索、Memory、路线更新、Workspace 读写与测试、Artifact 导出和 GitHub 只读导入；所有外部写入要求审批。

### 3. 成长与用户设置领域数据

```text
role_profiles
capabilities
role_capability_requirements
transition_templates
content_revisions
task_templates
rubric_versions
missions
mission_imports
mission_focuses
route_revisions
capability_claims
evidence
daily_tasks
submissions
reviews
projects
project_milestones
project_workspace_bindings
project_test_runs
artifacts
artifact_revisions
portfolio_exports
share_grants
programs
cohorts
enrollments
role_packs
aggregate_metrics
aggregate_snapshots
aggregate_query_budgets
aggregate_suppressions
claim_evidence_links
user_preferences
reminder_schedules
reminder_deliveries
byok_credentials
memory_policies
data_export_requests
```

核心状态：

```text
MissionStatus       = draft | active | paused | completed | archived
RouteRevisionStatus = generating | proposed | accepted | stale | superseded | failed
AnonymousClaimStatus = available | reserved | destination_committed | erasing | claimed | expired | manual_review
ClaimStatus         = active | disputed | superseded | withdrawn | rejected
ClaimOrigin         = inferred | user_asserted | system_derived | reviewer_asserted
VerificationLevel   = inferred | user_confirmed | demonstrated | applied | reviewer_verified
EvidenceStatus      = recorded | verified | disputed | invalidated
DailyTaskStatus     = scheduled | in_progress | submitted | reviewing | completed | skipped | rescheduled
ProjectStatus       = draft | active | blocked | completed | archived
MilestoneStatus     = planned | in_progress | submitted | verified | rework | completed
ReminderStatus      = active | paused | cancelled | completed
ShareScope          = private | explicit_grant | tenant_aggregate
```

每个 `route_revision` 都是不可变对象，必须记录 `input_manifest`、精确的 claim/evidence revision 列表、`claim_set_hash`、`base_route_version`、职业本体/内容 snapshot 和 Agent Profile snapshot。route planner 完成时必须同时 CAS 匹配 Mission 的 `route_version` 和当前 `claim_set_hash`；任一不匹配时结果只能进入 `stale`，不能成为 proposed 或 accepted。

接受新路线时，在一个 Mission 聚合事务中把旧 current revision（`accepted` 或 `stale`）改为 `superseded`、写入新 `current_route_revision_id` 并递增 `route_version`。用户纠正、反驳或撤回 claim 时，同一事务提交新的 claim revision 和 `claim_set_hash`、递增 `route_version` 并把当前 accepted route 标记为 `stale`，再排队生成新路线；旧路线在新路线接受前可以只读展示，但不能继续生成新的 Daily Task。

`mission_focuses` 以 `(tenant_id, user_id)` 为主键，保存 `mission_id` 和 `focus_version`。切换 Focus 必须提供 `expected_focus_version`；暂停、完成或归档当前 Focus 时，如果仍有其他可执行 Mission，请求必须同时提供明确的 `replacement_mission_id`，否则返回 `409`；没有其他可执行 Mission 时才把指针置空。Today、Coach 和任务生成只读取该指针，不能自行猜测“最近更新的 Mission”。

每个 Project 必须绑定一个 Mission、一个 accepted route revision 和一个版本化 Workspace 分支；里程碑、测试结果、Artifact revision 与完整复盘都形成 Evidence。未完成所有 required milestone、验证规则和复盘时不能把 Project 标记为 completed；作品集导出必须引用精确的 project、workspace、artifact 和 evidence revision manifest。

路线修改必须创建 revision；能力变化必须引用 evidence；反驳和撤回也保留原记录与原因；敏感正文统一进入 payload envelope。提醒使用 `reminder_schedules` 记录时区与下一次触发，`reminder_deliveries` 用稳定 delivery key 去重，不能只存为用户偏好字段。

### 4. 公共 API

统一采用 REST/JSON、OpenAPI 3.1、`application/problem+json`、cursor pagination、`Idempotency-Key` 和 `If-Match/expected_version`。

身份：

```text
POST /v1/auth/register
POST /v1/auth/verify-email
POST /v1/auth/resend-verification
POST /v1/auth/login
POST /v1/auth/logout
POST /v1/auth/password/forgot
POST /v1/auth/password/reset
POST /v1/auth/password/change
POST /v1/auth/email/change
POST /v1/auth/email/confirm-change
GET  /v1/auth/sessions
DELETE /v1/auth/sessions/{id}
DELETE /v1/auth/sessions?scope=others
GET  /v1/account
PATCH /v1/account
POST /v1/account/export-requests
POST /v1/account/erasure-requests
DELETE /v1/account/erasure-requests/{id}
```

产品与 Agent：

```text
/v1/onboarding-sessions
/v1/onboarding-sessions/{id}/claim
/v1/missions
/v1/missions/{id}/focus
/v1/route-revisions
/v1/route-revisions/{id}/accept
/v1/capability-claims
/v1/capability-claims/{id}/revisions
/v1/capability-evidence
/v1/daily-tasks
/v1/submissions
/v1/reviews
/v1/projects
/v1/projects/{id}/milestones
/v1/projects/{id}/workspace
/v1/projects/{id}/test-runs
/v1/projects/{id}/completion
/v1/artifacts
/v1/artifacts/{id}/revisions
/v1/portfolio-exports
/v1/share-grants
/v1/preferences
/v1/reminder-schedules
/v1/byok-credentials
/v1/memory-policy
/v1/conversations
/v1/messages
/v1/runs
/v1/runs/{id}/cancel
/v1/approvals
/v1/events?after_seq=
/v1/realtime
```

企业与合同：

```text
/v1/tenants
/v1/memberships
/v1/invitations
/v1/admin/programs
/v1/admin/cohorts
/v1/admin/contracts
/v1/admin/entitlements
/v1/admin/usage
/v1/admin/aggregate-snapshots
/v1/admin/audit
/v1/admin/approval-requests
/v1/admin/repair-commands
```

Focus 切换、route accept、claim correction、Project/Milestone 推进和 reminder 修改都必须在 OpenAPI 中声明具体 HTTP method、`Idempotency-Key`、`If-Match/expected_version`、授权、审计和 `409` 冲突语义。`/v1/capability-evidence` 只负责证据的记录与读取，不能代替 `capability_claim` 的确认、反驳、撤回和 revision API。

启动 Agent 工作的写操作返回 `202 + run_id`；普通查询和同步配置操作返回对应资源，不额外创建 Run。

当 conversation 已有前台 active Run 时，发送消息必须显式提供 `mode`：

```text
enqueue              默认；排队到当前 Run 结束后执行
interrupt            取消当前 Run，收敛后创建 replacement Run
feedback             只用于 waiting_approval 或 checkpoint
parallel_background  只用于明确允许的只读后台任务，不能写同一 Workspace
```

状态不允许时返回 `409 application/problem+json`，不能把新消息静默塞进正在执行的 Run。本轮不提供 OAuth/OIDC、SAML、SCIM 或支付 API。

## 五、内部实施阶段和固定门禁

下面六个阶段只是内部工程顺序，不是六个产品版本。开发过程中可以持续集成和内部试用，但不会发布 MVP、Alpha 或功能不完整的商业版本；最终只发布通过第六阶段的完整 GA。

### 统一门禁规则

每个阶段都按同一套规则验收：

1. **用同一份代码验收**：报告必须记录 commit、容器镜像 digest、数据库 schema 版本、配置 snapshot 和测试数据版本。
2. **结果可以重复**：自动化测试必须进入 CI；人工评审必须使用固定样本和 rubric，不能只凭演示通过。
3. **关键问题不能豁免**：跨租户访问、secret 泄漏、未授权副作用、重复副作用、事实丢失和核心路线无依据都是零容忍；出现一次就算阶段失败。
4. **变更会让门禁失效**：已经通过的 API、状态机、权限、数据 schema、模型、prompt、tool、policy 或基础设施配置发生相关变更后，必须重跑受影响门禁。
5. **不能带病进入下一阶段**：固定门槛没有全部满足时，只能修复并重新验收，不能用“后面再补”代替通过。

每个阶段都要生成一份 `gate-report`，至少包含：验收对象、测试环境、样本或负载版本、逐项结果、失败记录、修复 commit、最终报告 hash 和批准人。

为了方便阅读，本节使用几个固定说法：`rubric` 是“所有评审员使用同一份评分表”，`snapshot` 是“冻结后不可改的一版配置”，`gate-report` 是“阶段验收报告”。P0 指会造成安全事故、数据损坏或系统不可用的问题；P1 指会让核心用户旅程无法正确完成、且没有可接受替代方案的问题。

| 阶段 | 用白话说 | 通过标志 |
| --- | --- | --- |
| 1. 契约冻结 | 大家对“做什么、怎么做、怎么算成功”理解一致 | 契约完整、主旅程可追踪、P0/P1 为 0 |
| 2. 基础平台 | 账号、租户、权限和数据底座安全可用 | 身份流程全过、跨租户访问为 0、匿名 claim 只成功一次 |
| 3. Agent 内核 | 崩溃、重试和断线都不会把任务做乱 | 故障注入全过、重复副作用和状态倒退为 0 |
| 4. 成长产品 | 产品确实围绕已有能力生成可执行迁移路径 | 核心旅程、分 Profile 双语 eval 和封闭真实用户 pilot 全过 |
| 5. 企业与合同 | 企业功能不侵犯个人隐私，账目不会算错 | 权限矩阵全过、隐私泄漏和账差为 0 |
| 6. 统一 GA | 同一个发布候选版本通过所有最终检查 | 安全、AI、容量、恢复、删除和安装全部达标 |

### 阶段 1：契约冻结

**目标**：先把“产品要做什么、系统如何表达、如何判断做对了”说清楚，避免不同团队按各自理解开发。

**必须交付**：

- 统一的品牌、术语表和 Goal → Map → Learn → Practice → Create → Evidence 状态说明。
- 产品能力与页面、API、事件、状态机、数据表、权限和测试用例之间的追踪矩阵。
- 完整 OpenAPI、事件 schema、状态机、错误码、数据分类、保留和删除规则。
- 五个 Agent Profile 的输入输出 schema、版本规则、各自独立的固定 eval slice、rubric、质量门槛、单次成本与延迟预算和回滚方式；共享端到端样本不能替代 Profile 专项验收。
- 匿名 principal/EventStore 分区契约、claim durable saga、Mission Focus CAS、Route stale-result CAS、Project/Milestone 和 reminder delivery 状态机。
- 企业聚合隐私 threat model、允许的指标与维度 catalog、suppression/query-budget 或差分隐私参数，以及 two-person rule 的主体分离策略。
- 封闭真实用户 pilot protocol，预先冻结招募分层、任务、成功指标、退出规则、同意书、数据最小化方式和报告模板；pilot 开始后不得为通过门槛而改指标。
- `product_behavior_manifest` schema 和影响矩阵：列出 pilot 涉及的前端 chunk/共享依赖、API/事件契约、模型、prompt、tool、Profile、policy、职业本体、内容与 feature flag hash，并规定哪类变化使自动门禁或真实用户 pilot 失效。
- 至少 `500` 个固定安全样本，覆盖 prompt injection、越权工具、secret 外发、恶意检索内容、Memory 污染、危险 Artifact、审批绕过、路径逃逸、网络出口和跨租户访问，每类不少于 `50` 个。
- “原型与正式产品差异清单”，明确能力百分比等原型表达如何替换为证据等级。

**验收方法和固定门槛**：

- OpenAPI、事件 schema 和数据库 schema lint 必须 `100%` 通过，不能存在未解析引用或重复标识。
- 追踪矩阵必须覆盖本文第二、三、四章的 `100%` 能力；每个写操作都必须写明认证、授权、幂等、并发、审计和错误响应。
- 匿名预览并 claim、完整每日闭环、用户纠正迁移关系、完整 Create 项目、企业显式分享这五条主旅程，必须逐步追踪到 UI → API → event/state → data → test，覆盖率 `100%`。完整 Create 必须覆盖 Project、Milestone、Workspace revision、测试/评审、Evidence、Artifact revision、复盘和作品集导出，不能用单一导出动作代替。
- 文档中不得存在影响实现的 `TBD/TODO`；契约评审未解决的 P0/P1 问题必须为 `0`。
- 产品、前端、后端、安全和 QA 负责人必须共同批准同一份 contract snapshot。

**阶段通过证据**：schema lint 报告、追踪矩阵、五条主旅程报告、评审问题清单和签名后的 contract snapshot。以上五项缺一不可。

### 阶段 2：基础平台

**目标**：完成所有后续能力共同依赖的身份、租户、权限、数据和基础设施底座。

**必须交付**：

- monorepo、CI/CD、数据库迁移、依赖与镜像扫描、SBOM、签名构建和环境配置。
- Identity Service、邮件、session、tenant、membership、RBAC、RLS、安全审计、匿名 ephemeral principal、claim durable saga，以及 claim 所需的最小 EventService append/outbox/idempotency 能力。Agent Run/ToolCall 等完整执行内核仍在阶段 3 交付。
- PostgreSQL、NATS、Valkey、S3-compatible storage、Vault 和基础可观测设施。

**验收方法和固定门槛**：

- 注册、验证、登录、退出、改密、重置、改邮箱、session 撤销、邀请、导出和删除请求的正常、重放、并发、过期和撤销用例必须 `100%` 通过。
- 对同一个匿名路线执行 `10,000` 次包含并发、重放以及与过期清理竞争的 claim；最终必须只有一个归属 Mission、一个成功 claim 和完整删除凭据。
- 在 `reserved` 前后、目标 Mission commit 前后、进入 `erasing` 前后以及每一种存储 deletion receipt 前后分别注入崩溃，每个边界至少连续 `100` 次，并覆盖 outbox 重投、未知提交结果和自动对账；恢复后不得重复 Mission、不得把已 reserve 的 claim 过期，也不得在 receipt 不完整时删除剩余恢复依据。故障注入结束时 `manual_review` 必须为 `0`；真正无法自动判定的损坏案例留待阶段 3 的 Repair Command 演练。
- 自动生成的 tenant × role × resource × action 权限矩阵必须 `100%` 通过；跨租户读取或写入成功次数必须为 `0`。
- 数据库、对象存储、日志和 trace 扫描中，密码、原始 session token、验证 token、重置 token 和 BYOK 明文出现次数必须为 `0`。
- CSRF、session fixation、邮箱枚举、密码截断和未授权内部身份 header 用例中，绕过次数必须为 `0`。
- 从空环境安装、迁移到最新版、回滚一版再重新升级，必须连续 `3` 次成功，且迁移前后数据校验无差异。

**阶段通过证据**：身份 E2E 报告、claim 并发报告、claim saga 故障与对账报告、权限矩阵、secret 扫描报告、迁移报告和基础设施 smoke report。任一零容忍项失败即退回本阶段。

### 阶段 3：Agent 执行内核

**目标**：证明 Agent 即使遇到重复消息、进程崩溃和网络不确定性，仍然只沿合法状态前进。

**必须交付**：

- EventService、outbox/inbox、Scheduler、Worker claim、CAS/fence/lease、effect ledger、approval、reconciliation、Sweeper 和 Repair API；Repair API 包含对 `AnonymousClaimStatus=manual_review` 的受审计处理器，禁止直接改 claim 或目标 Mission。
- LLM Gateway、BYOK、Memory/Retrieval、Realtime、Workspace、Artifact 和 Firecracker Runtime。
- 模型、prompt、tool、profile、policy 的不可变 snapshot、eval 和回滚流水线。

**验收方法和固定门槛**：

- Run、ToolCall、Command、Attempt、Approval 和 Workspace Revision 的合法转换必须 `100%` 可达；非法转换拒绝率必须为 `100%`。
- 构造匿名 claim 的目标提交未知、删除 receipt 冲突和 source/destination hash 不一致案例各至少 `100` 个；Repair Command 必须依据现有事件和唯一 `claim_key` 收敛到一个目标 Mission 或保持 `manual_review`，不得猜测性复制、删除或直接改库，错误收敛次数必须为 `0`。
- 重复投递、Worker 崩溃、lease 过期、取消竞态、并行 join、审批失效、`outcome_unknown`、Workspace 半提交、Realtime 丢失、队列丢失和 PITR 旧 command，每个场景至少连续注入 `100` 次。
- 上述故障中，重复外部副作用、终态倒退、绕过审批、第二个 continuation、跨租户访问和事件事实丢失次数都必须为 `0`。
- 每次真实模型请求都必须有 `provider_attempt_id`、模型版本、用量、成本和 `context_manifest`；抽查和自动核对覆盖率必须为 `100%`。
- 自定义 Provider endpoint 的 loopback、私网、link-local、metadata、DNS rebinding 和 redirect 绕过测试必须 `100%` 被阻止，BYOK 发往非绑定 host 的次数必须为 `0`。
- 阶段 1 冻结的 `500` 个安全样本必须 `100%` 执行；越权工具调用、secret 外发、审批绕过、路径逃逸和跨租户访问成功次数必须为 `0`。
- 使用 [capacity-and-scaling.md](./capacity-and-scaling.md) 的参考生产组合运行 `30` 分钟时，Agent 相关指标必须满足 [operations.md](./operations.md) 的 SLO；本阶段先不要求最终的双倍突发和区域恢复演练。

**阶段通过证据**：状态机模型报告、故障注入报告、effect ledger 对账、context manifest 完整性报告、Provider egress 安全报告和基线负载报告。

### 阶段 4：成长产品

**目标**：把可靠的 Agent 内核变成真正有效的能力迁移产品，而不是普通课程推荐器。

**必须交付**：

- 科技与 AI 职业本体、能力要求、transition template、任务模板、rubric、内容来源和更新流程。
- Onboarding、Route、Today、Task、Submit/Review、Map、Evidence、Goal、Coach、Create 和 Settings 的正式前端。
- 快速选择和自然语言两种起点、多 Mission、难度调整、选中即问、路线纠正、项目 Workspace、Milestone/Test/Artifact revision、作品导出、提醒和完整中英文内容。
- 版本化端到端产品 eval 集：至少 `30` 组职业迁移、每组至少 `10` 个情景，共 `300` 个英文样本，并提供语义相同的 `300` 个简体中文样本。
- 五个 Agent Profile 各自的专项 eval slice：每个 Profile 每种语言至少 `100` 个样本；可以与端到端样本重叠，但必须按 Profile 独立记录输入、期望、评分和结果。
- 不对外发布的真实用户 design-partner pilot：至少连续 `28` 天、`60` 名完成同意流程的目标用户，英文和简体中文各不少于 `30` 人，覆盖至少 `6` 组代表性职业迁移且每组每种语言不少于 `5` 人。pilot 使用独立测试租户，不接触其他租户数据；行为数据只在明示同意后按 protocol 最小化采集。

**验收方法和固定门槛**：

- 以下核心旅程的浏览器 E2E 必须 `100%` 通过：匿名快速选择、匿名自然语言输入、能力确认、路线预览、注册 claim、第一项任务、代码/文字/设计实践、提交与回顾、难度降低、Coach 追问、迁移关系纠正、多 Mission 切换、完整 Create 项目，以及 Settings 中的语言、时区、提醒、BYOK、Memory、数据导出和删除。
- 完整 Create E2E 必须分别覆盖代码、文字和设计三类项目：从 Mission/accepted route 创建 Project，绑定 Workspace，推进至少两个 required Milestone，提交测试或 rubric 评审，形成 Evidence 和 Artifact revision，完成综合复盘，再从精确 revision manifest 导出作品集。缺少 required Milestone、验证结果或复盘时，Project completion 必须被 `100%` 拒绝。
- 路线不得把职业标签推断直接写成已确认能力；无用户确认或 evidence 的能力升级次数必须为 `0`。
- 用户纠正或反驳一条迁移关系后，`100%` 的测试样本都必须产生新的 claim/route revision；旧 claim 不再作为当前路线依据，但历史仍可审计。
- 将“route planner 读取 claim revision N → 用户提交 revision N+1 → 旧 planner 结果后到达”以及 accept/纠正并发的交错各执行至少 `1,000` 次；旧结果进入 `stale` 的比例必须为 `100%`，成为 current route 或生成新任务的次数必须为 `0`。
- 对 Focus 切换、暂停、完成和归档执行至少 `10,000` 次基于模型的并发随机操作；任意提交点最多一个 Focus，有可执行 Mission 时必须恰好一个，Today/Coach/任务生成读取错误 Mission 的次数必须为 `0`。
- 两名独立评审员使用每个 Profile 自己的固定五分 rubric；所有人工评分维度平均必须 `>= 4.0/5`，单个样本维度达到 `4/5` 才算“可接受”，每个维度可接受样本比例必须 `>= 85%`，任一职业组或语言不得低于 `80%`。

| Agent Profile | 必须独立验收的维度 | 专项固定门槛 |
| --- | --- | --- |
| `route_planner` | 目标理解、迁移桥可信度、缺口准确性、路线依据可追溯 | 人工评分满足上述 `4.0/5` 与通过率门槛；无依据的 confirmed claim 为 `0` |
| `daily_planner` | 与当前 Goal/Route/Evidence 的因果关系、任务可执行性、难度和时间预算 | 因果引用覆盖率 `100%`；人工评分满足上述门槛 |
| `coach` | 上下文 groundedness、教学帮助、对用户偏好的适配、何时让用户动手或升级人工处理 | 引用不存在上下文的次数为 `0`；人工评分满足上述门槛；越权读写和替用户完成受限动作均为 `0` |
| `evaluator` | 确定性判定、rubric 结论、能力升级依据和不确定性处理 | 确定性测试准确率 `100%`；与专家基准一致率 `>= 85%`；模型自评直接升级能力次数为 `0` |
| `artifact_builder` | 输出忠实度、revision/evidence manifest、危险内容处理和外部写入审批 | manifest 完整率 `100%`；人工评分满足上述门槛；危险 Artifact 未标记或未审批外部写入次数为 `0` |

- 中英文各维度通过率差值不得超过 `5` 个百分点；评审员对“通过/不通过”的一致率必须 `>= 85%`。
- 语言与时区切换、跨夏令时提醒、提醒去重和取消提醒使用固定 `200` 个时间场景验收；错误时区、重复提醒和已取消后仍发送的次数必须为 `0`。
- PWA 必须通过安装检查；离线时只能展示已缓存内容和明确的离线状态，不能把未提交写操作显示为成功；断网重连后事件补拉和草稿恢复 E2E 必须 `100%` 通过。
- 核心旅程键盘可操作率必须为 `100%`，自动与人工无障碍检查中的 Critical/Serious 问题必须为 `0`。
- 正式 UI 中不得显示精确能力百分比，必须改用证据等级和文字说明；全量页面扫描与人工抽查中的违规数必须为 `0`。
- design-partner pilot 的指标和分母在 pilot protocol 中预先冻结：开始匿名 onboarding 的用户中至少 `75%` 到达路线预览；完成路线预览的用户中，至少 `80%` 能在无协助下确认或纠正后接受路线，并正确复述一条迁移桥和一项能力缺口；接受路线的用户中，至少 `70%` 在 `48` 小时内完成首个 Task → Submit → Review 闭环，至少 `60%` 在首周完成三次完整每日闭环，至少 `40%` 在 `28` 天内完成一个含两个以上 Milestone 的有界 Create 项目。任一语言或职业迁移组的核心指标不得比总体低超过 `15` 个百分点。
- pilot 中跨租户访问、私人内容误分享、无依据能力升级和未审批副作用必须为 `0`；导致核心旅程无法完成且无替代路径的未解决问题必须为 `0`。未达到任一产品指标时阶段失败，必须修复后用新的冻结 cohort 重跑，不能只解释原因后放行。

**阶段通过证据**：浏览器 E2E 报告、Create revision/evidence 报告、Route/Focus 并发报告、五个 Profile 的双语 eval 报告、评审原始记录、一致率计算、能力证据审计、无障碍报告、原型差异核对表、pilot cohort manifest、同意记录摘要、原始指标 hash 和签名 pilot report。

### 阶段 5：企业、合同与交付能力

**目标**：证明企业功能不会破坏个人隐私，合同和额度台账在并发与故障下仍然准确。

**必须交付**：

- 邮箱邀请、CSV 批量成员管理、Program/Cohort、角色包、任务包、聚合分析和显式分享。
- 合同、席位、额度、用量、Provider 成本、人工调整和审计后台。
- 支持后台、Trust Center、状态页、审计导出、SLA、Docker Compose 和私有化交付包。

**验收方法和固定门槛**：

- 创建 Program/Cohort、发布角色包和任务包、邀请成员、加入 Cohort、显式分享、撤销分享和成员离职这条企业主旅程，浏览器与 API E2E 必须 `100%` 通过。
- 企业角色 × 资源 × 操作权限矩阵必须 `100%` 通过；管理员读取私人对话、反思、证据、Memory、Workspace 或作品的成功次数必须为 `0`。
- share grant 创建前不能读取、只允许读取授权范围、撤销提交后新请求立即拒绝；固定权限用例必须 `100%` 通过。
- 聚合 API 只能返回阶段 1 catalog 中的 snapshot、指标、allowlist 维度和时间桶；任意维度、任意交叉筛选、逐成员 drill-down、绕过 query budget 或读取被 suppression cell 的请求必须 `100%` 拒绝。
- 固定的 `500` 个小样本、补集、交叉筛选和差分查询用例必须全部执行；另外运行至少 `100` 条自适应攻击序列、每条最多 `100` 次依赖前一结果的查询。结果 cell 或补集少于 `5` 人时必须稳定 suppression；从允许结果中还原或区分个人信息的成功次数必须为 `0`。若使用差分隐私，实际累计 privacy budget 不得超过阶段 1 冻结值。
- 同一份成员 CSV 重放 `100` 次，最终 membership、席位占用和审计结果必须与执行一次完全相同；无效行不能污染有效行。
- 对合同、预留、结算、释放、过期回收和人工调整执行至少 `100,000` 次基于模型的随机操作；负余额、超发席位、重复扣费和不可解释账差必须为 `0`。
- 合同与 Repair Command 必须通过 5 分钟内的重新认证和 two-person rule；对同一主体重复投票、发起人自批、无权限审批人、审批后角色被撤销、proposal hash/version 被修改、目标版本变化和批准过期逐项测试，执行成功次数必须为 `0`。只有两个不同的有效审批主体绑定同一不可变 proposal 时才能执行一次。
- 所有管理操作必须带操作者、reason、前后版本和审批事件；审计字段完整率必须为 `100%`，直接改库不能成为受支持操作。

**阶段通过证据**：企业权限报告、分享与聚合隐私报告、CSV 重放报告、台账模型测试与对账报告、管理面安全报告和交付包安装说明。

### 阶段 6：统一 GA 验收

**目标**：在一个不可变 Release Candidate 上重新验证完整系统，确认可以一次性发布成熟商业版。

**必须交付**：

- 带 commit、镜像 digest、SBOM、签名和构建来源证明的唯一 GA Release Candidate。
- 前五阶段的最终 gate-report、用户与管理员文档、运维 runbook、支持流程、开源与商业许可材料。
- 安全、AI、容量、灾难恢复、删除、安装、升级和回滚的最终报告。

**验收方法和固定门槛**：

- 前五阶段所有自动化门禁必须在 Release Candidate 上重跑并仍为通过，失败项必须为 `0`。阶段 4 pilot 可以沿用已批准报告，但 RC 的 `product_behavior_manifest` 必须与 pilot snapshot 精确一致：pilot 涉及的前端 route chunk、共享组件与依赖、相关 OpenAPI/事件行为、模型、prompt、tool、Profile、policy、职业本体、内容 revision 和 feature flag 的 hash 都不得变化；admin-only 隔离 chunk 只有在依赖图证明不影响 pilot scope 时才可排除。任一相关 hash 不同就必须使用新的冻结 cohort 重跑受影响的 pilot 门禁。
- 外部渗透测试中未解决的 Critical 和 High 问题必须为 `0`；涉及跨租户、secret、未授权副作用、数据损坏或审批绕过的问题不允许风险接受。
- 五个 Agent Profile 必须分别通过阶段 4 对应的专项双语 eval 和共享端到端 eval；相对已批准基线的任一核心维度不得下降超过 `2` 个百分点，secret/PII 泄漏和越权工具调用必须为 `0`；每个 Profile 的单次成功任务成本和 p95 延迟不得超过阶段 1 冻结的预算。
- 完整参考负载必须同时持续 `30` 分钟，再承受 `2` 倍入口和事件突发 `5` 分钟；全程满足 [operations.md](./operations.md) 的全部 SLO，突发结束后 outbox、queue 和 Sweeper backlog 必须在 `15` 分钟内恢复稳态。
- 单实例/可用区故障必须达到 `RPO = 0、RTO <= 5 分钟`；整区域恢复必须达到 `RPO <= 5 分钟、RTO <= 60 分钟`；队列数据丢失恢复必须 `<= 15 分钟`。
- 随机选择 `100` 个测试账号执行删除：在线 payload、Memory、索引、Workspace、Artifact 和 cache 均不可再读取，并生成完整 deletion receipt；不可变备份中的数据必须按保留期到期，并在任何恢复流程中由待删除清单再次清除。
- Docker Compose、Helm + OpenTofu 和官方云环境的全新安装、升级、备份、恢复和回滚必须各连续 `3` 次成功。
- GA 发布清单中的 P0/P1 未解决问题必须为 `0`；自动化 gate-report 必须基于当前 Release Candidate，且生成时间不早于最终发布前 `30` 天。pilot report 可以早于 `30` 天但不得早于 `90` 天，并且必须附带当前 RC 与 pilot snapshot 的精确 `product_behavior_manifest` 对比和五方签核；不能证明行为等价时一律视为 pilot 尚未通过。

**最终发布证据**：签名 Release Candidate、总 gate-report、安全报告、分 Profile 双语 AI eval、签名 pilot report 与 behavior manifest 对比、容量报告、恢复演练、删除报告、三种部署报告、许可材料和最终批准事件。只有全部满足时才能标记 GA。

## 六、验收资产与持续发布规则

### 1. 固定测试资产

以下资产必须和代码一样版本化，不能只保存在个人电脑或临时文档中：

```text
contracts/            OpenAPI、事件 schema、状态机和错误码
test-fixtures/         身份、权限、并发、删除和故障测试数据
product-evals/         双语端到端职业迁移样本、专家基准和 rubric
profile-evals/         五个 Agent Profile 的独立双语样本、期望和 rubric
security-tests/        prompt injection、越权、SSRF、egress、secret 与租户隔离样本
load-profiles/         容量组合、硬件、Provider quota 和数据规模
pilot-protocols/       招募分层、同意、任务、指标、退出与数据最小化规则
pilot-reports/         去标识化 cohort manifest、指标 hash、behavior manifest 和签名报告
behavior-manifests/    pilot/RC 行为组成、依赖图、hash 与影响矩阵结果
runbooks/              发布、回滚、PITR、区域恢复、对账和删除流程
gate-reports/          每个阶段的机器可读结果与签名摘要
```

测试资产变更必须经过和代码相同的 review。不能为了让失败版本通过而删除困难样本；样本修正必须保留原因和前后差异。

### 2. AI 行为变更门禁

模型、prompt、tool descriptor、Agent Profile、guardrail policy 和路由策略的任何变更，都必须：

1. 创建不可变 `change_snapshot_id` 和 hash。
2. 在固定 eval 和 red-team 集上与当前生产基线对比。
3. 满足阶段 4 和阶段 6 的质量、安全、成本与延迟门槛。
4. 记录风险负责人、批准事件、灰度范围和明确的自动回滚阈值。
5. 演练恢复旧 snapshot；历史 Run 继续引用创建时的旧 snapshot。
6. 若已完成阶段 4 pilot，比较 `product_behavior_manifest`；命中 pilot protocol 定义的用户可见或判断语义变更时，旧 pilot 立即失效并按影响范围重跑，不能只用自动 eval 替代真实用户证据。

命中回滚阈值时先恢复旧版本，再分析原因。不能边扩大影响范围边等待指标自行恢复。

### 3. 始终为零的红线

以下指标不使用误差预算，任何阶段发现一次都必须阻断：

- 跨租户或跨匿名主体访问成功。
- secret、密码、原始 token 或私人正文进入未授权日志、prompt、Artifact 或外部请求。
- 未审批的高风险副作用执行成功。
- 同一个 effect 因重试产生两次外部副作用。
- 已结束 Run 被普通流程改回非终态，或同一 join 产生两个 continuation。
- 已被用户纠正或撤回的能力声明继续作为当前路线依据。
- 基于旧 `claim_set_hash` 的 Route 成为 current，或同一 `(tenant_id, user_id)` 同时存在两个 Focus Mission。
- 企业管理员绕过 share grant 读取个人内容。
- 企业聚合结果允许区分个人，或 same-principal/self-approval 绕过 two-person rule。

### 4. 可追溯的验收结论

每条 GA 要求必须能从“需求”追到“测试”，再追到“报告和构建”：

```text
requirement_id
→ contract / state / policy
→ automated_test_id 或 manual_rubric_id
→ test_result + evidence_hash
→ commit + image_digest + config_snapshot
→ gate-report + approval_event
```

只提供截图、口头演示或“测试没有发现问题”不算通过。缺少测试结果、环境版本或证据 hash 的要求一律视为尚未验收。

## 七、明确不实现的内容

- ZITADEL、Auth0、Clerk 或其他外部身份服务。
- Google/GitHub/Apple 社交登录。
- Passkey、MFA、OIDC、OAuth、SAML、SSO、SCIM。
- Stripe 或其他在线支付、信用卡、自动发票、税务和退款。
- 原生 iOS/Android 客户端。
- 面向招聘、晋升或员工管理的自动决策。
- 通用 Agent Builder 或独立 Cloud Agent 平台商品。

这些能力只能在未来通过新的产品决策进入范围；本轮只保证现有数据和服务边界不会阻碍后续扩展。
