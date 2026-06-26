# 多租户与安全

> 本文档是 [architecture.md](./architecture.md) 的子文档，定义租户隔离、权限模型、数据保留与删除策略。

## 多租户

租户隔离是核心设计属性，**不能只依赖代码里的 `WHERE tenant_id = ?`**，采用三层保护：

1. 应用层查询显式限定 tenant。
2. PostgreSQL Row-Level Security 作为第二道防线。
3. 应用连接角色不是表 owner、不持有 `BYPASSRLS`；必要时用 `FORCE ROW LEVEL SECURITY` 让 owner 也受限。

此外：Gateway 删除客户端提交的内部身份 header，向下游传签名短期可信上下文；每个 append 再校验 `tenant_id` 与 aggregate ownership；artifact/workspace/memory retrieval 同样执行 ACL；idempotency key 以 tenant + operation scope 隔离；跨租户管理操作使用完全独立的 admin role。

租户级限制覆盖请求速率、队列深度、runtime session、token budget、存储与 artifact 大小。

## 数据保留与删除

事件溯源系统天然与"删除"冲突：事件不可变，但租户注销、GDPR / 被遗忘权、保留期到期都要求数据可消除。需提前定义，否则生产期必然撞上：

- **保留策略**：事件、审计、artifact、日志各有独立 `retention` 与冷归档路径；`D_retention` 容量模型应反映归档而非无限增长。
- **删除手段**：对必须物理移除的 PII 采用 **crypto-shredding**——敏感载荷以每租户 / 每主体密钥加密存储，删除密钥即让密文不可还原，既满足删除诉求又保住事件序列与因果结构不破。
- **删除也是事件**：删除经 Repair Command API 触发（`SubjectErasureRequested`），留下"已删除"的审计事实，而不是直接 `DELETE` 绕过状态机与审计。
- **投影同步失效**：memory / 快照 / 检索索引作为可重建投影，在源数据删除后必须随之失效并重建。

## 安全与权限

- 默认拒绝。tenant/user 上下文来自认证结果，不信任客户端直接传入的 id。
- 在 API 边界检查权限，在 worker 执行敏感操作前再次检查；tool 权限限定在当前 tenant、workspace 和 run。
- **检索内容与 LLM 输出都是不可信输入**：Permission Service 不能因检索文本或 LLM "声称用户已授权"就放行工具操作，工具权限只来自独立的可信 policy context；危险操作走 human-in-the-loop（`waiting_approval`）。
- secrets 存放在应用配置之外、需要时临时获取；对运行不可信代码的 sandbox 优先用 **broker 代发请求**而非把原始凭证注入 sandbox；不记录 token/credential/原始 secret/敏感 payload。
- 对权限变更、管理员操作、runtime 执行、租户配置、取消、重试和修复记录审计事件。

### Admin 必须经 Repair Command API，不直接写 EventDB

Admin 直连数据库写会绕过状态机、fencing、tenant policy、审计、idempotency 与终态约束。修复路径应为：`Admin UI → Repair Command API（权限 + dual approval）→ EventService → 追加管理事件 + outbox`。例如不要直接 `run.status='running'`，而应追加 `RunRecoveryRequested` / `ReplacementRunCreated` / `DlqCommandRedriveRequested` / `RuntimeTerminationRequested`。终态 run 永不被直接覆盖，修复创建新的 attempt 或 replacement run 并引用旧 run。只读检查可走只读副本或受限查询 API。
