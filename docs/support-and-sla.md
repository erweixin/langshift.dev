# 支持流程与 SLA 证据

Lites 的支持承诺来自生效合同中的 `support_tier` entitlement，不来自前端文案或人工口头承诺。每个工单创建时，Product Service 在事务中解析当前有效合同与 entitlement，并把 tier、首次响应时限和解决目标冻结到工单。合同后续变更不会改写既有工单的 SLA 证据。

## 工单边界

- 任意 active member 可创建和查看自己的工单；`owner`、`admin`、`contract_admin` 可在当前租户内处理工单。每次读写都会在事务内重新检查当前 membership，撤权后立即拒绝新请求。
- subject 与所有 message 通过 envelope encryption 写入受限 payload store。PostgreSQL、event 和 outbox 只保存内容引用与哈希。
- 创建和回复都要求 CSRF、加密持久幂等响应；回复还要求 `If-Match` 与 body 中的 case version 完全一致。
- message 是 append-only 证据。closed 工单不可修改；状态转换由数据库 trigger 限制。
- 浏览器提交成功后清空明文输入；失败时保留本地表单供用户修正，不会把失败显示为已提交。

## SLA 配置

`support_tier` entitlement 的配置格式如下：

```json
{
  "tier": "enterprise",
  "response_minutes": {
    "low": 480,
    "normal": 240,
    "high": 60,
    "urgent": 30
  },
  "resolution_minutes": {
    "low": 10080,
    "normal": 4320,
    "high": 1440,
    "urgent": 480
  }
}
```

允许的 tier 是 `community`、`standard`、`enterprise`、`premium`。首次响应可配置范围为 5 分钟到 7 天；解决目标可配置范围为 30 分钟到 30 天。缺失或越界配置 fail-safe 到 community：首次响应 72 小时，解决目标不作合同声明。只有已执行的双人审批 entitlement proposal 能改变配置。

`response_due_at` 和 `resolution_due_at` 是目标时间，不代表问题被自动标记已响应或已解决。只有实际 support reply 才能设置 `first_responded_at`，只有合法状态转换才能设置 `resolved_at`。

## 支持处理流程

1. Triage 根据 category、priority、tenant tier 和冻结的 due time 排队；不能通过修改 priority 或合同倒写既有时钟。
2. 处理人员回复后，状态进入 `waiting_on_customer`；客户补充信息后进入 `waiting_on_support`。
3. 已验证修复后进入 `resolved`。客户复现可重新打开；确认结束后进入不可变 `closed`。
4. `security` 工单只用于非敏感协调。可利用细节必须通过正式安全披露通道；在该通道与公钥正式发布前，Trust Center 不声称存在公开 bug bounty。
5. 涉及跨租户、secret、未授权副作用、数据损坏或审批绕过的事件立即按安全事故处理，不得以 SLA 风险接受替代修复。

## 监控与证据

生产监控至少按 tier/priority（低基数）统计待首次响应、即将超时、已超时、待解决和实际耗时。tenant、case 或 user ID 只能进入日志/trace/审计，不能成为 metrics label。

支持验收证据包括：创建/回复 API 测试、真实 PostgreSQL 的 RLS/加密引用/SLA 冻结测试、事件 schema、OpenAPI amendment、浏览器 desktop/mobile E2E，以及 `gate-reports/stage-5/support-case-control-plane-contract.json`。公开状态页故障边界见 [public-status-operations.md](./public-status-operations.md)。
