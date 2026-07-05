# 练习运行时：面向学习者的代码执行

> 这篇只讨论课程里的代码练习怎么运行。它和 [runtime-and-sandbox.md](./runtime-and-sandbox.md) 不是一回事：那篇保护的是 Agent 工具执行，代码可能碰到用户文件、secret 或外部系统；这里保护的是学习页的 playground，用户只是跑自己的练习代码，最重要的是反馈快、页面不卡。

几个词先说清：

- **harness**：包住用户代码的测试外壳，负责调用函数、断言结果、收集日志。
- **reference_solution**：内容管线用来验题的参考解，只在后台校验用，不能下发给用户。
- **信任档**：按风险给运行环境分级。练习代码不可信，但能碰到的数据很少，所以它的隔离要求和 Agent 工具不同。

## 问题、决策与风险

**问题**：课程的核心交互是“写代码 → 运行测试 → 看自己有没有想明白”。这个反馈要很快，否则学习心流会断。短期只需要 JS，长期要支持 Go / Python / Rust 等语言；测试也必须和课程里的判断点对齐。

**决策**：分两级做。v0 先在浏览器里跑 JS：没有服务端成本，反馈最快，也能覆盖第一条路线。v1 再做服务端语言沙箱：用预热容器跑 Go / Python 等语言。两级使用同一份测试协议，这样以后换运行时，不用改课程内容格式。

**忽略后果**：v0 一上来就做服务端沙箱，会把成本、运维、延迟都提前拉高；但如果长期只在主线程用 `new Function` 裸跑，死循环会卡死页面，也永远教不了 Go。

## 测试协议（两级统一）

ContentArtifact 里的练习部分：

```json
{
  "language": "javascript",
  "starter_code": "...",
  "reference_solution": "...",       // 只用于管线校验，永不下发
  "harness_version": 1,
  "tests": [
    {"id": "t5", "call": "transition('completed','running')", "expect": false,
     "judge": true, "label": "终态不可回退"}
  ]
}
```

运行结果（无论哪级运行时）：

```json
{
  "status": "ok | error | timeout",
  "cases": [{"id": "t5", "pass": true, "actual": "false"}],
  "judge_hit": true,
  "logs": ["..."],
  "duration_ms": 42
}
```

结果会记成 `ExerciseRunRecorded` event，并关联 task_id 和代码快照 hash。提交页自动带入的测试结果、Review 看到的事实，都来自这里。**Review 不重新跑测试，只引用已经记账的结果。**

## v0：浏览器内 JS

- **用 Web Worker 执行，不在主线程裸跑 `new Function`**：worker 里跑用户代码和测试；主线程用 `setTimeout` 做看门狗。超过默认 2 秒就 `terminate()` 并重建 worker。这样死循环只会损失这一次运行，不会把页面卡死。
- Worker 内屏蔽 `fetch / XMLHttpRequest / importScripts / indexedDB`（置为 undefined），console 捕获转发主线程。
- 无服务端往返：p50 反馈 < 100ms；离线也能练（自部署 / 弱网友好）。
- 安全定位：用户是在自己的浏览器里跑自己的练习代码，主要风险是页面稳定性，不是服务端数据泄露。看门狗 + API 屏蔽在 v0 足够，不需要一开始就套很多层 iframe。
- 局限要在 UI 上诚实说明：v0 只能教 JS 语义。第一条路线的阶段 1 可以用 JS 讲清概念；进入真实 Go 持久化前，必须补 v1。

## v1：服务端语言沙箱

触发条件：课程开始需要真实目标语言，或者新增 Go / Python 路线时，再上服务端沙箱（对应 [v0-product-slice.md](./v0-product-slice.md) 的升级信号）。

- **信任档**：在 [runtime-and-sandbox.md](./runtime-and-sandbox.md) 的分级表中新增 `exercise` 档。代码不可信，但能碰到的东西很少：没有租户数据、没有 secret、没有网络，只有练习文件。所以 rootless 容器 + seccomp + 资源限额就够，不需要一开始上 microVM。
- **无状态执行**：每次运行都从干净容器开始，写入用户代码和测试，执行完就回收。容器可以按语言复用镜像，但不能跨用户保留文件系统。
- **限额**：CPU 2s（可按练习放宽到 10s）、内存 256MB、无网络、输出 64KB 截断、进程数限制。
- **延迟预算**：p50 < 1.5s（含排队）。预热池按 `并发运行峰值 × 冷启动时间 / 目标等待` 估算；学习产品的运行峰值集中在晚间，池子按时段伸缩。
- 每种语言都有自己的 harness（Go 用 `go test` 包装、Python 用 pytest 子集），内容管线生成测试时选择对应的 harness_version。

## 与 Agent 沙箱的差异对照

| 维度 | Agent 工具沙箱（runtime-and-sandbox.md） | 练习运行时（本文档） |
| --- | --- | --- |
| 代码来源 | 模型生成，可能被注入 | 学习者本人 |
| 可触达数据 | workspace、artifact、可能的 secret | 仅练习文件 |
| 网络 | 按策略 allowlist | 一律禁止 |
| 延迟要求 | 秒~分钟级可接受 | 亚秒（v0）/ 1.5s（v1） |
| 状态 | workspace revision 单写者语义 | 无状态，代码快照记账即可 |
| 失败处置 | outcome_unknown 对账 | 直接把错误给用户（教学的一部分） |

## Do / Don't

| 应该 | 不应该 |
| --- | --- |
| v0 用 Web Worker + 看门狗 | 主线程 `new Function` 裸跑 |
| 测试协议统一，运行时可替换 | 内容格式绑死某个运行时 |
| 参考解永不下发客户端 | 把 reference_solution 放进 ContentArtifact 下发 |
| 运行结果记账、Review 引用 | Review 时重跑或轻信用户自报 |
| v1 定义 exercise 专用信任档 | 直接套用 untrusted 档的 microVM 要求 |
