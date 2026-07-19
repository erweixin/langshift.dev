# Lites Engineering Release Candidate 发布说明

该候选版本面向职业迁移垂直 SaaS，不提供通用 Cloud Agent PaaS、开发者 token、SDK、Agent Marketplace 或通用 Tool/Profile 托管。

候选版本覆盖匿名职业经历输入、路线预览、账号归属、Mission/Focus、任务、提交、评审、证据、能力声明修订、企业线下合同控制面，以及内部 Cloud Agent 的可恢复执行语义。企业商业权益通过线下合同、席位、额度和 usage ledger 管理，不包含在线支付 API。

Task → Submit → Review → Evidence 不再依赖 React 内存状态：任务查询会返回当前不可变 submission 与最新 evaluator generation/Run/Review/Evidence 的恢复快照。页面刷新或重新登录后会继续读取同一 generation；失败 generation 的显式重试会保留历史并创建新的 generation，但不会创建第二份 submission，数据库同时保证同一 submission/rubric 最多只有一个 active 或 succeeded generation。

本地完整产品拓扑现在包含独立 Realtime Gateway。Web 断线恢复使用公开的 `after_seq` 游标并处理命名 SSE `event` 帧；Review 生成状态只从职业产品的持久化 daily-task snapshot 恢复，不再让浏览器轮询内部 Agent Run 控制面。Docker Desktop 验收同时执行桌面/移动旅程和 PostgreSQL 自定义格式备份恢复。

数据库迁移链已扩展到版本 99：账号擦除可以写入内部非识别 locale tombstone `und`，而公开资料写入仍只接受英文与简体中文。聚合 snapshot 对时间窗口和维度参数执行显式 PostgreSQL 类型绑定；完整数据库测试使用全局唯一夹具命名空间，并给确定性故障注入包保留五分钟的 macOS 包级预算。

证据生成器现在区分执行结果和可发布证据：dirty worktree 上即使真实产品旅程执行成功，报告仍标记为 `failed/current_dirty`，只能用于开发诊断，不能进入 Engineering RC evidence manifest。

GitHub issues 已完全退出发布门禁：Engineering RC、Commercial GA 工程检查、CI、证据 manifest 和线下安装器都不查询或检查 issue 数量、内容与标签，也不打包 issue 快照。

生产契约门禁现在显式覆盖 Linux/Firecracker runtime host：严格 schema 绑定 systemd unit、host preflight、环境配置、root/mTLS/SPIFFE/asset digest 控制和 jailer adapter，并用拒绝样本验证 KVM、cgroup v2、不可变资产、fail-closed restart recovery 与 macOS 例外不能被删改。该结果始终是 `fixture_validated`，不会启动 microVM，也不代表已经实测隔离强度。

最终工程状态只能由当前干净提交上的 `npm run verify:macos` 产生，结果名称为 `engineering_rc_passed`。生产部署、KVM 隔离、真实容量/灾备、外部渗透测试、真实模型双语评审和用户 pilot 仍按 [已知限制](./engineering-rc-known-limitations.md) 进入 Commercial GA 前置验证。
