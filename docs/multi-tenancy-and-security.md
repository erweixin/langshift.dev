# 多租户与安全

> 定位：生产边界文档。本文档解释多租户系统的安全底线：每层都要知道当前 tenant 是谁，权限只能来自可信上下文，人工修复也必须走可审计入口。

## 问题、决策与风险

**问题**：Agent 平台同时处理多个租户的对话、workspace、artifact、memory、runtime 和 secret。LLM 输出与检索文本都不可信，可能诱导系统越权执行工具。

**决策**：租户边界不能只靠一层代码判断。需要应用层显式 tenant 条件、PostgreSQL RLS、受限 DB role、可信策略上下文、artifact/workspace/memory ACL 和 Repair Command API 一起保护。

**为什么不只依赖业务代码里的 `WHERE tenant_id = ?`**：异步 Worker、后台 Sweeper、管理员修复、投影重建和临时脚本都会访问数据。只靠调用方自觉过滤，很容易在某条路径漏掉 tenant 条件。

**忽略后果**：跨租户数据泄漏、LLM prompt injection 越权执行工具、管理员直接改库破坏不变量、删除请求无法证明已处理。

| 应该 | 不应该 |
| --- | --- |
| 每层都校验租户归属 | 只在 API 层校验一次 |
| 权限只来自可信策略上下文 | 相信 LLM 或检索文本声称“用户已授权” |
| 删除敏感载荷并失效投影 | 只从主表删除一部分数据 |
| 通过 Repair Command API 修复 | 让 Admin 直接写 EventDB |

## 先用白话说

多租户隔离不能只靠“程序员记得加 `WHERE tenant_id = ?`”。API、Worker、Sweeper、Repair、投影重建和临时运维脚本都会碰数据，所以需要多层保护：

- 应用层显式带 `tenant_id`。
- 数据库 RLS 再挡一层。
- artifact、workspace、memory、secret 各自做 ACL。
- 管理员修复也通过 Repair Command API 留事件和审计。

## 多租户隔离

租户隔离采用三层数据库保护：

1. 应用层查询显式限定 `tenant_id`。
2. PostgreSQL Row-Level Security（RLS，数据库行级隔离）作为第二道防线。
3. 应用连接角色不是表 owner，不持有 `BYPASSRLS`；必要时使用 `FORCE ROW LEVEL SECURITY`，让表 owner 也受 RLS 限制。

此外：

- Gateway 删除客户端提交的内部身份 header。
- 下游服务只接收签名的短期可信上下文。
- 每次 append 再校验 `tenant_id` 与目标对象归属。
- artifact、workspace、memory retrieval 都执行 ACL。
- idempotency key 按 tenant + operation scope 隔离。
- 跨租户管理操作使用独立 admin role，并且只进入 Repair Command API。
- 租户级限制覆盖请求速率、队列深度、runtime session、token budget、存储和 artifact 大小。

## 权限模型

权限检查至少发生两次：

1. API 边界：用户是否能创建消息、run、审批、取消或查看 conversation。
2. Worker 执行前：工具是否能在当前 tenant、workspace、run、policy snapshot 和审批状态下执行。

检索内容、用户输入和 LLM 输出都是不可信输入。Permission Service 不能因为 prompt 里出现“用户已经授权”就放行。危险操作必须进入 `waiting_approval`，审批结果作为事件记录。

Tool 权限必须绑定到这些上下文，不能只看“用户能不能用这个工具”：

- tenant
- user / service actor
- conversation / run
- workspace revision
- tool name 和工具能力类型
- secret scope
- network egress policy
- approval id（如果需要）

## Secret 与敏感数据

- secrets 存放在应用配置之外。
- sandbox 优先通过 broker（受控代发服务）发送敏感请求，也就是由受控服务拿 secret 去访问外部系统。
- 必须注入 secret 时，只注入短期、最小权限、可撤销 token。
- 日志、metrics、trace、audit 不记录 token、credential、原始 secret 或敏感 payload。
- prompt、tool result 和 artifact 需要按敏感等级决定是否加密、脱敏或禁止进入 memory。
- 用户输入、模型输出、工具结果、审批材料、Realtime chunk、子 Run 摘要和 memory 正文统一使用 payload envelope：正文放加密 payload，事件和审计只保存 `payload_ref`、`payload_hmac`、敏感标签、保留策略和脱敏摘要。

## 数据保留与删除

事件溯源系统和“删除”天然有张力：事件希望长期保留，删除请求又要求敏感数据不可恢复。设计采用以下规则：

- 事件、审计、artifact、日志各自有 retention 和冷归档策略。
- 对必须删除的 PII，敏感载荷使用每租户或每主体密钥加密；删除密钥后密文不可还原，这就是 crypto-shredding（销毁密钥式删除）。
- 删除本身也是事件，例如 `SubjectErasureRequested` / `SubjectErasureCompleted`。
- memory、snapshot、检索索引、缓存、artifact 派生物都必须随源数据删除而失效或重建。
- 删除流程经 Repair Command API 或专门的 Erasure API 进入 EventService，不能直接 `DELETE` 绕过审计。

示例：用户请求被遗忘。系统追加删除请求事件，冻结相关投影更新，删除主体密钥，失效 memory chunk、snapshot 和 search index，最后追加完成事件。事件序列仍可证明“曾处理过删除”，但敏感载荷不可还原。

## Admin 与 Repair Command API

Admin 直连数据库写会绕过状态机、fence、租户策略、审计、幂等和终态约束。修复路径必须是：

```text
Admin UI
-> Repair Command API (权限检查 + 双人审批)
-> EventService
-> 管理事件 + outbox command
```

允许的修复动作示例：

- `RunRecoveryRequested`
- `ReplacementRunCreated`
- `DlqCommandRedriveRequested`
- `RuntimeTerminationRequested`
- `ToolCallManuallyResolved`（必须记录 `confirmed_occurred`、`confirmed_not_occurred` 或 `accepted_unknown`；最后一种进入 `resolved_unknown`，不能伪装成 cancelled）
- `SubjectErasureRequested`

终态 run 永不直接覆盖。修复只能创建新的执行尝试、替代 run 或管理事件，并引用原 run。只读检查可走只读副本或受限查询 API。
