# Engineering Release Candidate 已知限制

本文件是 Engineering RC 的正式限制清单，不是待办掩码。以下项目完成并取得当前候选版本的真实证据之前，Lites 不得宣称 Commercial GA：

- 尚未在 Linux KVM/Firecracker 运行主机上完成隔离、逃逸和资源限制实测；macOS 只验证 runtime adapter 的行为契约。
- 尚未在官方云完成多可用区容量、故障恢复、区域灾备和队列丢失演练；fixture 只证明报告验证器会拒绝不合格证据。
- 尚未取得独立外部渗透测试且清零 Critical/High 问题。
- 尚未对候选版本绑定的真实模型与五个 Profile 完成双语人工质量评审。
- 尚未完成 design-partner pilot 与预先冻结的产品指标验证。
- 法律主体、DPA、隐私政策、商业许可和签署权限仍需在实际经营主体下激活。
- 尚未在首个客户环境完成安装、升级、备份、恢复与支持演练。
- 在线支付不在本轮范围；企业席位、额度和权益只能通过线下合同控制面开通。
- MFA、SSO 和 SCIM 不在本轮范围；敏感操作依赖五分钟密码重新认证与双人审批。
- 账户归档会直接包含结构化的职业迁移数据，但不重复嵌入项目制品二进制（制品通过 Portfolio Export 单独导出）；Memory 正文仍受 subject-key 运行时边界保护，账户归档只提供其治理元数据。若 Commercial GA 要求单文件完整可移植性，必须补齐跨存储流式归档、体积预算和恢复验证。

任何 `fixture_validated`、历史报告、dirty worktree 报告或模拟结果都不能消除此清单中的限制。

## Engineering RC capability exclusions

以下能力保留在长期能力目录中，但不属于本轮锁定范围，也不得被当前接口或测试暗示为可用：

- 不集成风险触发式第三方 CAPTCHA。当前边界保留 IP、邮箱、设备维度限流与反枚举；若后续启用 challenge provider，必须补齐可访问性、隐私、失败降级和服务端 token 验证。
- 不提供 Google 专用模型 wire adapter。当前 Profile 只能引用已经过契约与出站策略测试的 OpenAI、Anthropic 或 OpenAI-compatible adapter；新增 provider 必须绑定 host、凭证版本、计费与故障分类测试。
- 不宣称已有独立于主数据平台管理的外部审计归档。Engineering RC 只提供数据库内 append-only 审计与签名导出；客户或生产部署需要另行指定独立存储、保留策略、访问主体和恢复验证。

这些排除项不能用 `skipped` 计为通过；若产品范围变化，应先更新能力矩阵、威胁模型和相应验收入口。

## API compatibility

Engineering RC 的职业迁移产品页面和生成客户端使用由冻结基础规范与全部有序 amendments 合并生成的 `contracts/openapi/lites.current.openapi.json`。基础 `1.0.0` 文档里被同运行时路径 v2 operation 取代的 `application/json` 写接口属于尚未启用的兼容面，不能视为可用 API；启用前必须逐项完成同等认证、幂等、CAS、审计、数据库集成和客户端契约测试，或者通过明确的版本化破坏性变更从公开规范删除。合并器会把 `{id}` 与 `{project_id}` 这样的等价模板收敛为一个当前路径，避免生成客户端同时暴露新旧歧义操作。

`tasks.generate` 不作为外部调用能力启用。每日任务由 Route 接受后的内部 `daily_planner` 命令和 durable worker 生成；任何后续契约变更都必须继续保持 Cloud Agent 为内部执行能力，不能借兼容名义发布通用 Agent 触发接口。
