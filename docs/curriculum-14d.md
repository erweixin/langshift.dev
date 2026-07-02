# 14 天路线草案：前端 → Cloud Agent

> 这是 v0 的内容输入：第一条路线的 14 个任务（TaskSpec 层）。它同时是内容管线的 golden set 和 M2 实验的脚本。这是**草案**——由创始人按真实学习体感修订后冻结；每个任务的课程正文由管线生成，这里只定"学什么、怎么算学会"。

设计原则：

- 每天一个判断点，且判断点之间有递进关系（后一天的任务从前一天的卡点长出来）。
- 三个阶段项目日（D4 / D9 / D14）产出可展示 Evidence，是作品化的素材。
- v0 练习全部为浏览器 JS 模拟（见 [exercise-runtime.md](./exercise-runtime.md)），涉及 Go 语义的地方在 UI 诚实标注"正式版为 Go 沙箱"。
- 命名与概念对齐平台文档——学的就是这套系统自己的架构（[state-machines.md](./state-machines.md)、[concurrency-and-durability.md](./concurrency-and-durability.md)），dogfooding。**状态机语义与平台完全一致**：`failed` / `expired` 是终态，重试发生在新 attempt / 新 run 层，不是把状态改回 `running`。早期原型课稿里「给 failed 加一条回 running 的重试转移」的讲法作废，以本表为准。

## 阶段 1 · 建立心智模型（D1–D4）

| 日 | 任务 | 判断点（能说清…） | 练习形态 | 分钟 |
| --- | --- | --- | --- | --- |
| D1 | run 的一生：5 个状态与合法转移 | 为什么 completed→running 必须被拒绝（副作用重放） | 写 `transition(from,to)` 校验，测试含判断点用例 | 35 |
| D2 | failed 之后怎么办：重试放在哪一层 | 为什么 failed 是终态——重试是创建新 attempt / 新 run，而不是把状态改回 running；以及为什么必须保证已完成 step 不重复执行 | `retry(run)` 创建新 run、复用 `executed_steps` 保证幂等；测试断言旧 run 状态不变 | 40 |
| D3 | state 是事件的投影 | 为什么存"发生过什么"而不是"现在是什么" | 写 `replay(events) → state`，删一条事件看投影变化 | 35 |
| D4 | **阶段项目**：内存版 run 状态机 + 事件日志 | 能向别人完整讲一遍 run 生命周期 | 组装 D1–D3：一个可回放的迷你 runtime | 60 |

## 阶段 2 · 持久化与崩溃恢复（D5–D9）

| 日 | 任务 | 判断点 | 练习形态 | 分钟 |
| --- | --- | --- | --- | --- |
| D5 | 进程死在第几步：checkpoint | 恢复点为什么选"事件提交后"而不是"内存更新后" | 模拟 crash：从事件日志恢复继续执行 | 40 |
| D6 | 同一条 command 来了两次：幂等与去重 | 去重放在消费者边界（inbox）而不是传输层的理由 | 实现 `command_id` inbox，重复投递只生效一次 | 35 |
| D7 | 超时之后世界未知：outcome_unknown | 为什么外部副作用超时不能盲目重试 | effect ledger 模拟：幂等键 vs 对账两条路 | 40 |
| D8 | 没有新事件也要自愈：sweeper | 谁来收敛"卡住"的 run；为什么超时进 `expired` 终态而不是原地复活 | 写定时扫描：超时 run 转 expired；需要继续就走 D2 的新 run 路径 | 35 |
| D9 | **阶段项目**：kill 掉也能恢复的执行器 | 能画出"崩溃点 × 恢复行为"矩阵并解释每格 | 组装 D5–D8：随机 crash 注入下跑通任务 | 60 |

## 阶段 3 · Agent loop、工具与记忆（D10–D13）

| 日 | 任务 | 判断点 | 练习形态 | 分钟 |
| --- | --- | --- | --- | --- |
| D10 | plan → act → observe：agent 循环 | 为什么 LLM 提议下一步、但不做最终授权 | 写循环骨架：mock LLM 返回工具请求，平台侧校验 | 40 |
| D11 | 工具调用的边界：schema 与权限 | 工具结果未知时 run 状态应该怎么走（接 D7） | 给工具加 input schema 校验 + 失败分类 | 40 |
| D12 | 上下文预算：什么进 prompt | 窗口装不下时，裁剪和检索各自负责什么 | 实现简版 context builder：预算内挑选消息 | 35 |
| D13 | 并行工具的 join | 最后完成的那个工具怎么"唤醒" run 且只唤醒一次 | 并行 mock 工具 + join 计数 + 单次恢复 | 40 |

## 阶段 4 · 收束与作品化（D14）

| 日 | 任务 | 判断点 | 练习形态 | 分钟 |
| --- | --- | --- | --- | --- |
| D14 | **阶段项目**：把 14 天讲成作品 | 能向面试官讲清 durable execution 的三个核心取舍 | 无代码：整理 D4/D9 Evidence → 作品集页 + 简历 bullet + 面试讲述（走作品化面） | 45 |

## 与系统的对接

- 每行即一个 `task_template_id`（`fe2agent-d01` … `fe2agent-d14`），是内容缓存 key 的一部分（见 [content-pipeline.md](./content-pipeline.md)）。
- "判断点"列进入 TaskSpec，管线必须生成至少一条 `judge: true` 的测试与之对应。
- 修订本表 = 升 `content_version`，旧缓存自然失效。
- 断更回归（`ReentryTaskIssued`）不占用 D 序号：生成的是当日任务的 20 分钟轻量变体。

## 待创始人确认的三个问题

1. D10–D13 用 mock LLM 是否足够，还是希望内测期就让练习真调一次小模型（成本与稳定性权衡）？
2. D4/D9 阶段项目一小时是否现实？超时策略是拆两天还是允许"周末补"？
3. 阶段 3 之后是否需要 D15+ 的"生产级"阶段（可观测、部署）作为付费深水区，还是留给第二条路线？
