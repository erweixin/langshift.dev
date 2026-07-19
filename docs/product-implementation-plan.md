# Lites 职业迁移垂直 SaaS 商业化实施计划

## 一、目标与边界

Lites 的产品定位是职业迁移垂直 SaaS。Cloud Agent 只作为路线规划、每日任务、辅导、评估和作品构建等产品能力的内部执行引擎，不在本轮建设通用 Agent PaaS。

本计划以 [README.md](../README.md) 的职业迁移理念、[architecture.md](./architecture.md) 的执行内核和 [ux-prototype.html](./ux-prototype.html) 的用户旅程为基础，面向现有实现逐阶段补齐真实产品闭环、工程质量和商业交付能力。

已锁定的范围如下：

- 不建设开发者平台、公开 SDK、开发者 token、Agent Marketplace、通用 Tool/Profile 托管或第三方 Agent 运行平台。
- 企业版通过线下合同开通席位、额度和权益；不实现 checkout、银行卡、自动扣款、税务、退款、优惠券或支付 webhook，在线支付不是本轮阻断项。
- 身份系统保留邮箱密码；敏感操作要求五分钟内重新认证，合同生效和 Repair Command 使用双人审批。本轮不建设 MFA、SSO 或 SCIM。
- 英文为默认语言，完整支持简体中文。
- 所有工程阶段必须具备 macOS 可执行的验收路径；允许使用 Docker Desktop 提供 PostgreSQL、NATS、Valkey、对象存储等本地依赖。
- Firecracker/KVM、真实多可用区、容量、灾难恢复和官方云交付只验证接口、配置与报告契约，不作为本轮工程阶段门禁。
- 本轮最终状态是 `Engineering Release Candidate`。模拟或契约验证不能作为 `Commercial GA` 的生产实证。

完整产品闭环仍然是：

`Goal → Map → Learn → Practice → Create → Evidence → 下一轮校准`

## 二、状态与证据规则

### 1. 实现状态

追踪矩阵中的每项能力只能使用以下状态：

| 状态 | 含义 |
| --- | --- |
| `implemented` | 生产代码和本阶段要求的测试均已完成 |
| `partial` | 已有实现，但缺少必要行为、接线或验收 |
| `missing` | 尚无可用的生产实现 |
| `deferred` | 已明确排除在本轮 Engineering RC 之外 |

### 2. 验收结果

所有阶段报告只能使用以下结果：

| 结果 | 含义 |
| --- | --- |
| `passed` | 已在报告绑定的当前提交上实际执行并通过 |
| `deferred` | 不属于本轮 macOS 工程验收，且有明确的后续验证入口 |
| `fixture_validated` | 只证明验证器、报告 schema 或契约有效，不代表生产实证 |
| `failed` | 实现或测试没有满足阶段要求 |

`skipped`、历史报告、测试 fixture、dirty worktree 报告或与当前提交不一致的报告不得自动折算为 `passed`。

### 3. 统一工程门禁

本计划最终建立两类统一命令：

- `verify:macos`：本轮阶段通过的强制工程门禁。执行 Go、Web、契约、数据库集成和无 Demo 的浏览器 E2E；模型和代码 runtime 使用确定性测试适配器。
- `verify:production-contracts`：验证 Helm、OpenTofu、Linux runtime host、容量、恢复和签名报告的 schema、配置与拒绝逻辑；结果只能是 `fixture_validated`，不能代表生产环境已经通过。

Go 门禁必须显式扫描项目包，例如 `./cmd/...`、`./internal/...` 和 `./services/...`，不能使用会把 `node_modules` 中 Go 源码纳入范围的无边界 `go test ./...`。

## 三、分阶段实施

阶段按顺序推进。一个阶段只有在交付物、macOS 验收和证据全部满足后才能标记完成；后续阶段可以提前开发，但不能提前宣称通过。

### 阶段 0：恢复项目事实与验收可信度

**目标**：建立当前实现的可信基线，消除文档、OpenAPI、实际路由、前端调用和 gate-report 之间的状态漂移。

**必须交付**：

- 建立“产品能力 → 页面 → API → 事件/状态机 → 数据表 → 测试”追踪矩阵。
- 将每项能力标记为 `implemented`、`partial`、`missing` 或 `deferred`。
- 为每个 OpenAPI operation 记录实际生产 handler，或标记计划完成阶段。
- 区分历史 gate-report、测试 fixture 和当前提交的真实证据。
- 修正文档中与仓库现状矛盾的实现数量、服务数量、部署形态和阶段状态。

**macOS 验收**：

- README、架构、OpenAPI 和实现状态不存在互相矛盾的描述。
- OpenAPI operation 到生产 handler 或计划阶段的映射覆盖率为 `100%`。
- 历史、dirty、fixture 或 commit 不匹配的报告不能被 readiness 检查识别为当前通过证据。
- 阶段报告包含 source commit、工作树状态、工具版本和执行时间。

**阶段证据**：追踪矩阵、OpenAPI 覆盖报告、文档一致性报告和证据分类报告。

### 阶段 1：打通真实职业迁移主干

**目标**：完成不依赖 Demo mode 的首条真实用户价值链。

主旅程为：

`匿名 Onboarding → Route Preview → 注册/登录 → Claim → Focus Mission → Today → Task → Submit → Review → Evidence`

**必须交付**：

- 自然语言经历输入进入真实结构化流程，不能只替换前端本地标签。
- 用户确认、纠正或反驳迁移关系时，创建新的 capability claim 和 route revision。
- Claim durable saga 在重放、并发和中断恢复后只产生一个目标 Mission。
- 补齐账户、租户、当前租户、重发验证邮件和 capability claim 等主旅程所需接口。
- 生产旅程不得依赖 `NEXT_PUBLIC_LITES_DEMO_MODE` 或关键 API mock。

**macOS 验收**：

- 使用本地服务、测试 Provider 和测试 runtime 完成整条 Playwright E2E。
- 页面刷新或重新登录后，路线纠正、任务、提交、Review 和 Evidence 均能从持久化状态恢复。
- 同一 claim 的重放和并发执行不会产生重复 Mission。
- UI 中不存在硬编码用户资料、没有处理器的业务按钮或只保存在 React state 的业务写操作。

**阶段证据**：真实主旅程 E2E、claim 并发与恢复报告、route revision 报告和 API 接线报告。

### 阶段 2：前端与 API 生产化闭环

**目标**：让所有核心页面使用真实契约和持久化状态，并具备生产级错误与可访问性体验。

**必须交付**：

- 完成 Onboarding、Route、Today、Task、Review、Map、Evidence、Goal、Coach、Create、Settings 和企业管理页面的真实数据接入。
- 使用 OpenAPI 生成类型和客户端，减少手写重复资源类型。
- 拆分超大页面组件，隔离数据访问、业务状态、表单和展示职责。
- 补齐 loading、empty、error、retry、offline 和权限拒绝状态。
- 完成英文默认、简体中文、时区和基础无障碍支持。

**macOS 验收**：

- lint、TypeScript 和组件测试全部通过，AdminConsole 测试 timeout 为零。
- 核心旅程键盘可操作，自动无障碍检查无 Critical 或 Serious 问题。
- 失败请求均有明确反馈，未持久化操作不得显示为成功。
- Playwright 覆盖桌面和移动视口，并运行真实应用路由与服务 handler。

**阶段证据**：Web 质量报告、OpenAPI 客户端覆盖报告、无障碍报告和桌面/移动 E2E 报告。

### 阶段 3：内部 Cloud Agent 内核加固

**目标**：证明内部 Agent 在重复消息、进程中断和不确定外部结果下仍只沿合法状态前进。

**必须交付**：

- 固化 EventStore、projection、outbox/inbox 和幂等提交语义。
- 完成 Run、ToolCall、Command、Attempt、Approval 和 Workspace Revision 状态机。
- 完成 lease/fence/CAS、effect ledger、`outcome_unknown` 对账、取消和 Sweeper。
- 加固 Provider Gateway、BYOK host 绑定、出站网络策略、Memory 写入治理和 SSE 补拉。
- 保留正式 Firecracker runtime adapter 和 Linux host 契约；macOS 使用行为等价的测试 adapter。

**macOS 验收**：

- 状态机属性测试覆盖全部合法和非法转换。
- 对重复消息、过期 lease、Worker 中断、取消竞态、旧 fence、审批失效、未知副作用和 Workspace 半提交执行确定性故障注入。
- 重复副作用、终态倒退、审批绕过、跨租户访问和事件事实丢失次数均为零。
- loopback、私网、link-local、metadata、redirect 和错误 BYOK host 请求全部被拒绝。
- 不要求本机 KVM、cgroup v2、Linux namespace 或真实 Firecracker 启动。

**阶段证据**：状态机报告、故障注入报告、effect ledger 对账、Provider 安全报告和 realtime 恢复报告。

### 阶段 4：职业内容与 AI 质量

**目标**：使路线、任务和反馈具有可追溯的职业依据，并能离线复现质量回归。

**必须交付**：

- 版本化职业角色、能力、迁移模板、任务、rubric 和来源依据。
- 为 `route_planner`、`daily_planner`、`coach`、`evaluator` 和 `artifact_builder` 建立独立输入输出契约。
- 建立中英文固定 eval 数据集、评分 rubric、行为 manifest、成本和延迟记录格式。
- 路线结论引用当前 claim/evidence revision，模型不能自行把推断升级为已验证能力。

**macOS 验收**：

- 固定数据集能够离线重放，结果绑定模型、prompt、tool、policy、内容和代码 hash。
- 路线纠正后，旧 planner 结果进入 `stale` 的比例为 `100%`。
- 无证据能力升级、虚构上下文引用、越权工具请求和未审批副作用均为零。
- 实际模型输出可以另行捕获，但本地评分、回归比较和报告生成不依赖特定云环境。
- 真实用户 pilot 不作为本轮工程阶段门禁，列入 Commercial GA 前置条件。

**阶段证据**：内容版本报告、五个 Profile 的离线 eval 报告、行为 manifest 和路线证据审计。

### 阶段 5：企业与线下合同能力

**目标**：支持线下企业合同交付，同时保护成员私人数据并保证权益台账可解释。

**必须交付**：

- 企业邀请、CSV 成员管理、Program、Cohort、角色包、任务包和显式分享。
- 固定指标的聚合分析、稳定 suppression 和查询预算。
- 合同、权益、席位、额度、用量预留/结算/释放、Provider 成本和人工调整台账。
- Admin Console 支持线下合同创建、续期、暂停和终止。
- 敏感合同、Repair、导出和 BYOK 操作执行密码重新认证；合同生效和 Repair 使用双人审批。

**macOS 验收**：

- 企业角色权限矩阵和 RLS 测试全部通过，管理员读取成员私人内容的成功次数为零。
- share grant 撤销后，新请求立即失效。
- 相同 CSV 重放后，成员、席位和审计结果保持幂等。
- 台账模型测试中的负余额、重复扣费、超发席位和不可解释账差均为零。
- 公开契约中不存在 checkout、银行卡、税务、退款、优惠券或支付 webhook 接口。

**阶段证据**：企业权限报告、分享与聚合隐私报告、CSV 重放报告、台账模型报告和敏感操作审批报告。

### 阶段 6：本地交付、运维与安全契约

**目标**：提供可在 macOS 开发环境运行的完整本地交付物，并固化未来生产验收接口。

**必须交付**：

- Docker Compose 补齐应用服务，支持 macOS Docker Desktop 启动完整本地产品。
- 补齐 Prometheus 记录规则、告警规则、Grafana dashboard 和主要事故 runbook。
- Helm/OpenTofu、Linux runtime host、容量、恢复和删除报告保留严格 schema 与验证器。
- 完成 SBOM、依赖扫描、镜像定义、数据导出和跨存储删除编排。

**macOS 验收**：

- Compose 可以完成安装、迁移、应用启动、基本旅程、备份和恢复 smoke。
- Helm/OpenTofu 只执行 render、lint、策略和契约检查，不要求部署真实集群。
- Linux runtime、HA、容量和灾备使用 fixture 验证报告生成器能拒绝缺失或不合格证据。
- 账号删除测试覆盖数据库、对象、Memory、索引、缓存和恢复后重删逻辑。
- 模拟结果不能被标记为真实 SLO、RPO、RTO 或隔离强度已经达标。

**阶段证据**：Compose smoke、可观测配置报告、生产契约 fixture 报告、SBOM 和账号删除报告。

### 阶段 7：Engineering Release Candidate

**目标**：在同一个不可变提交上重新验证本轮全部工程门禁，并输出可进入生产验证的候选版本。

**验收标准**：

- `verify:macos` 执行 Go、Web、契约、数据库集成和无 Demo E2E，并全部通过。
- Go 测试只扫描项目包，不把 `node_modules` 中的 Go 代码纳入测试范围。
- 所有通过报告绑定同一个干净 commit；陈旧、dirty 或 fixture 报告不能计为通过。
- 当前提交的全部必需工程检查失败为零；GitHub issue 的数量、内容和标签不作为门禁输入。所有 `deferred` 项都有原因、负责人类别和后续生产验证入口。
- 输出 SBOM、行为 manifest、数据库迁移清单、接口覆盖报告、已知限制和发布说明。
- 最终状态只能标记为 `engineering_rc_passed`，不能标记为 `commercial_ga_passed`。

**阶段证据**：统一 Engineering RC 报告、证据 manifest、发布说明和 deferred 清单。

## 四、公共接口与类型变化

计划明确补齐并接通以下接口：

- `/v1/account`、`/v1/tenants`、当前租户切换和重发验证邮件。
- `/v1/capability-claims` 及不可变 revision、纠正和反驳命令。
- `/v1/byok-credentials` 与 `/v1/memory-policy`。
- 企业 aggregate snapshot、合同权益、席位和 usage ledger 接口。

所有业务写接口统一具备：

- 认证和资源级授权。
- 幂等键。
- CAS/version 并发保护。
- 审计事件。
- 结构化错误响应。

Agent 控制面仍为内部可信接口，不发布开发者 token、SDK、自定义 Agent/Profile 或工具市场。本轮不增加任何在线支付 API。

## 五、macOS 测试边界

### 1. 必须真实执行

- Go 和 TypeScript 的单元、属性、状态机和契约测试。
- PostgreSQL、NATS、Valkey、对象存储等可由 Docker Desktop 提供的集成测试。
- 运行真实 Web、Gateway 和业务 handler 的浏览器 E2E。
- 使用确定性 Provider 和 runtime adapter 的 Agent 故障与恢复测试。
- Helm/OpenTofu render、schema、策略和报告验证器测试。

### 2. 只验证契约

- Firecracker/KVM、cgroup v2 和 Linux namespace 隔离。
- 真实多可用区、区域恢复和队列灾难恢复。
- 生产参考负载、双倍突发和长期 SLO。
- 云 IAM、网络隔离和官方云安装。
- 外部签名身份、渗透测试机构和正式发布审批。

这些项目的 fixture 必须同时包含通过和拒绝样本，以证明验证器能够拒绝缺失字段、错误 hash、错误提交、不合格阈值和伪造签名，但 fixture 结果只能标记为 `fixture_validated`。

## 六、Commercial GA 前置条件

以下项目不在本轮实施范围内，但正式对外宣称成熟 Commercial GA 前仍必须完成：

- Linux KVM/Firecracker 隔离实测与运行主机交付。
- 官方云多可用区部署、容量、故障恢复和区域灾备演练。
- 外部渗透测试，并清零未解决的 Critical 和 High 问题。
- 当前模型和 Profile 的真实双语质量评审。
- 真实 design-partner pilot 与产品指标验证。
- 法律主体、商业许可、DPA、隐私政策和签署权限正式激活。
- 首个客户环境的安装、升级、备份、恢复和支持演练。

上述项目不能被模拟报告、历史报告或契约 fixture 替代。它们完成之前，项目只能标记为 Engineering Release Candidate。
