# Runtime 与 Sandbox

> 本文档是 [architecture.md](./architecture.md) 的子文档，定义代码执行环境的隔离模型与 workspace 并发语义。

RuntimeManager 是代码执行和 workspace 修改的控制平面，runtime session 可丢弃、workspace revision 才是持久状态。

## 信任等级与隔离

按**信任等级**选择隔离（不能因为"跑在 K8s 里"就认为已有强隔离，seccomp/AppArmor 等需显式配置）：

| 信任等级 | 典型负载 | 隔离要求 |
| --- | --- | --- |
| Trusted | 平台自有工具 | 容器 + 普通 namespace |
| Semi-trusted | 租户配置脚本 | rootless、严格 syscall、网络/文件限制 |
| Untrusted | 用户任意代码 | 更强边界、独立节点或 microVM |
| Privileged | 需敏感网络/密钥 | 专用 pool、审批、强化审计 |

## 最低安全基线

无论哪种 runtime 至少明确：禁止 privileged、drop capabilities、seccomp/AppArmor/SELinux、只读根文件系统、PID/CPU/内存/磁盘/inode 限制、默认拒绝出口、阻止访问云 metadata endpoint、DNS/目标域名策略、workspace mount 权限、artifact 大小/类型、session TTL、kill deadline、审计命令/镜像/网络目的地/文件变更。

## Workspace 并发

采用单写者语义：每个 workspace 同时最多一个可写 lease，并行读允许，并行写用 copy-on-write branch 后显式 merge。每次工具执行产出 `base_workspace_revision`、`result_workspace_revision`、`file_change_manifest`、`git_diff_hash`、`artifact_refs`。
