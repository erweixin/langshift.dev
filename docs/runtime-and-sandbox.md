# Runtime 与 Sandbox

> 本文档是 [architecture.md](./architecture.md) 的子文档，定义代码执行环境、workspace 修改和 sandbox 安全边界。

## 问题、决策与风险

**问题**：ToolWorker 可能执行平台工具、租户脚本或用户任意代码。它可能读写 workspace、访问网络、生成 artifact，并接触敏感凭证。

**决策**：RuntimeManager 统一管理代码怎么跑、workspace 怎么改。runtime session 可以随时丢弃，真正需要长期保存的是 workspace revision 和 artifact。执行环境按信任等级隔离，默认不允许访问网络和 secret。

**为什么不只依赖 K8s/Docker 默认隔离**：容器不是完整安全边界。默认权限、云 metadata endpoint、宿主挂载、网络出口、镜像来源和 secret 注入都可能造成跨租户泄漏。

**忽略后果**：不可信代码可能读取其他租户文件、窃取云凭证、扫描内网、写爆磁盘、污染 workspace 或上传恶意 artifact。

| 应该 | 不应该 |
| --- | --- |
| 按信任等级选择隔离强度 | 因为跑在容器里就默认安全 |
| 通过 broker（受控代发服务）发送敏感请求 | 把长期原始 secret 注入 sandbox |
| 每次工具输出 workspace revision 和 diff hash | 只记录“工具成功” |
| 单写者修改 workspace | 让多个工具同时写同一 revision |

## 威胁模型与边界

| 边界 | 主要风险 | 控制措施 |
| --- | --- | --- |
| 租户之间 | A 租户读到 B 租户的文件、artifact、memory 或 runtime session | tenant-scoped ACL、RLS、独立 workspace path、artifact namespace |
| Sandbox 到控制面 | 不可信代码调用内部管理接口 | 默认拒绝出口、服务网格/防火墙、metadata endpoint 阻断 |
| Sandbox 到密钥 | 凭证泄漏、日志泄漏 | broker（受控代发）、短期 token、最小权限、不记录 secret |
| Sandbox 到宿主机 | 容器逃逸、资源耗尽 | rootless、drop capabilities、seccomp/AppArmor、CPU/内存/磁盘/inode 限制 |
| Artifact 到用户 | 恶意文件、超大文件、伪造 MIME 类型 | 大小/类型限制、扫描、隔离存储、下载鉴权 |
| 多个 workspace 写入者 | 并发覆盖、merge 冲突 | 单写者 lease、copy-on-write branch、显式 merge |

## 信任等级与隔离

| 信任等级 | 典型负载 | 隔离要求 |
| --- | --- | --- |
| Trusted（可信） | 平台自有工具 | 容器 + 普通 namespace；仍需资源限制和审计 |
| Semi-trusted（半可信） | 租户配置脚本 | rootless、严格 syscall、网络/文件 allowlist |
| Untrusted（不可信） | 用户任意代码 | microVM、隔离节点或等价强边界；默认无网络 |
| Privileged（高权限） | 需敏感网络/密钥 | 专用 pool、审批、broker（受控代发）、强化审计 |

选择隔离等级时，以工具 capability、tenant policy、数据敏感度和网络需求共同决定，不能只看工具名称。

## 最低安全基线

所有 runtime 至少明确：

- 禁止 privileged container。
- 去掉不需要的 Linux capabilities。
- 启用 seccomp、AppArmor 或 SELinux。
- 只读根文件系统，workspace 作为受控挂载。
- PID、CPU、内存、磁盘、inode、进程数限制。
- 默认拒绝网络出口。
- 阻止访问云 metadata endpoint。
- DNS 和目标域名 allowlist（只允许访问明确批准的域名）。
- artifact 大小、类型和扫描策略。
- session TTL、idle timeout、kill deadline。
- 审计镜像、命令、网络目的地、文件变更、artifact 引用。

## Secret Broker

敏感凭证优先不进入 sandbox。更安全的做法是让 broker（受控代发服务）代替 sandbox 访问敏感系统：

1. ToolWorker 根据可信策略上下文请求一次能力。
2. Secrets Broker 检查 tenant、user、run、tool、目标资源和审批状态。
3. Broker 返回短期能力，或直接代替 sandbox 发起请求。
4. Sandbox 只拿到最小化 token，或完全不拿 secret。
5. Broker 和 ToolWorker 记录审计事件，但不记录原始 secret。

只有在工具必须本地持有凭证时，才注入短期、最小权限、可撤销的 token，并限制网络出口。

## 镜像与 Artifact

- 镜像来源必须受信任或经过扫描。
- 镜像 tag 不能作为唯一身份，需记录 digest。
- 高风险工具使用固定 allowlist 镜像。
- Artifact 写入必须记录 `artifact_ref`、hash、size、content type、producer tool_call_id。
- 可执行 artifact、压缩包和未知 MIME 需要扫描或隔离下载。
- Artifact 访问要重新检查 tenant/user ACL，不能只靠不可猜 URL。

## Workspace 并发

Workspace 采用单写者语义：

- 每个 workspace 同时最多一个可写 lease。
- 并行读允许。
- 并行写必须创建 copy-on-write branch。
- merge 必须显式，不能自动覆盖。
- 冲突文件进入 `merge_required`，由 Agent、用户或 Repair API 决定。

每次工具执行都要产出：

```text
workspace_change
- base_workspace_revision
- result_workspace_revision
- file_change_manifest
- git_diff_hash
- artifact_refs
- merge_status: clean | merge_required | abandoned
```

Run 的 `context_manifest` 引用 workspace revision。Replay 是“重放历史状态”，不是重新执行文件修改；它只读取已记录 revision 和 artifact。

## Sandbox Session 生命周期

```text
requested -> provisioning -> ready -> running -> idle -> terminated
                         \-> failed
```

规则：

- `requested/provisioning` 超时由 Sweeper 回收。
- `ready` session 有 idle TTL。
- `running` session 受 run/tool lease 约束。
- cancel、deadline、quota 失败或 policy revoke 会触发 `RuntimeTerminationRequested`。
- kill deadline 到达后强制终止。
- terminated session 不保存业务事实，只保留审计、资源用量和清理结果。

RuntimeManager 可以复用 session，但复用前必须确认 tenant、trust tier、workspace mount、network policy 和 secret scope 完全匹配。
