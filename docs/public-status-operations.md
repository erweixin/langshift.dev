# Public Status 发布与故障边界

公开状态页不是营销用静态“全绿”页面。它只展示由生产监控聚合器生成、使用独立 Ed25519 密钥签名且仍在有效期内的短时快照。任何文件缺失、签名错误、密钥过期、状态自相矛盾或快照过期都会由 API 返回 `503 status_snapshot_unavailable`；Web 必须显示 `Unknown`，不能推断为正常。

## 信任边界

- Browser 只能访问精确的 `GET /v1/public/status`；Gateway 为该请求签发 `public_request` 上下文。其他方法和相似路径仍默认要求登录。
- Product Service 从 External Secret 挂载的 `PUBLIC_STATUS_DOCUMENT_FILE` 与 `PUBLIC_STATUS_KEYRING_FILE` 读取数据。签名私钥不进入 Product Service Pod。
- 签名覆盖规范化后的完整 payload。`generated_at` 必须位于对应 key 的签发窗口内，`valid_until - generated_at` 必须在 30 秒到 5 分钟之间。
- `overall` 必须等于所有组件中的最严重状态。发布器和读取器都会拒绝把异常组件汇总成更绿色的 overall。
- API 最多公开缓存 15 秒并强制 revalidate；这显著短于快照有效期。

## 发布器

构建 `cmd/lites-status-sign` 后，用未签名状态文档、目的隔离的私钥 bundle 和目标路径发布：

```sh
lites-status-sign \
  --input /secure/status-input.json \
  --key-file /secure/status-signing-key.json \
  --output /secure/public-status.json \
  --valid-for 2m
```

发布器用当前 UTC 时间覆盖 `generated_at`，按 `--valid-for` 生成 `valid_until`，并通过同目录临时文件、`fsync` 与原子 rename 发布。输入和私钥 JSON 都拒绝未知字段或尾随值。

私钥 bundle 格式：

```json
{
  "version": "1.0.0",
  "key_id": "status-2026-07",
  "private_key": "BASE64URL_RAW_ED25519_PRIVATE_KEY"
}
```

Product Service 使用的公钥 keyring 与 trusted-context keyring 相同的封闭格式，但必须是独立文件、独立 key id 和独立密钥材料：

```json
{
  "version": "1.0.0",
  "keys": [{
    "id": "status-2026-07",
    "public_key": "BASE64URL_RAW_ED25519_PUBLIC_KEY",
    "not_before": "2026-07-01T00:00:00Z",
    "not_after": "2026-08-01T00:00:00Z"
  }]
}
```

## 轮换和事故操作

1. 先把新公钥加入 keyring，并等待 External Secret 刷新和 Product Service readiness 稳定。
2. 让发布器切换到新私钥/key id；确认 API ETag 和 `generated_at` 持续更新。
3. 至少等待旧快照最大有效期、缓存上限和部署传播窗口都过去，再移除旧公钥。
4. 若聚合器或发布器故障，保持 fail-closed 的 `Unknown`，通过 Support Center 和独立事故通信渠道发布人工说明；不要手工写无签名“operational”文件。
5. 若私钥疑似泄露，立即停止发布、移除受影响公钥、轮换密钥，并把状态完整性问题作为安全事故审计。恢复前页面应保持 `Unknown`。

状态契约位于 `contracts/openapi/amendments/v1.27.0/public-status.json`；自动门禁报告写入 `gate-reports/stage-5/public-status-control-plane-contract.json`。
