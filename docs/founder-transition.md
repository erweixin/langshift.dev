# Founder Transition：前端 → Cloud Agent 工程师

> 这是一份 dogfooding 文档：用 Lites 自己的方法论（[product-growth-os.md](./product-growth-os.md)）来管理创始人本人的能力转型。
>
> 当前优先级：**A（个人能力转型）优先**。B（生意验证）和 C（infra 产品化）先让路。但因为 Cloud Agent 的核心架构已经按“可剥离、可复用”的方向设计，做 A 的过程中会自然积累 C 的技术期权和可展示 Evidence。
>
> 你是这个产品的 Evidence #0。如果 Lites 的方法连你自己的转型都帮不了，它也很难真正帮到用户。

## 这份文档要解决什么问题

你现在的起点是前端工程师。目标不是“学一点 Go”或“写几个后端接口”，而是完成一次更深的角色升级：

```text
从会做前端产品
到能独立设计并实现一个 Cloud Agent runtime
```

换成人话说：你要把 `backend/` 从一个只能证明“服务活着”的 20 行 health server，逐步做成一个能长期运行、能恢复、能追踪、能调用工具、能服务真实 Agent 工作流的后端核心。

## Mission

```text
Mission：把 backend/ 从 20 行 health server，长成一个可恢复、有状态、可观测的 Cloud Agent runtime
路径类型：Skill Expansion + Role Upgrade（前端 → Cloud Agent / 分布式后端）
技术栈：Go 1.22（已选定，backend/ 已是 Go）
```

这不是为了“造一个很炫的 infra”。它的第一目标是帮助你补上前端转 Cloud Agent 工程师最关键的能力：

- 长时间运行的任务如何管理。
- 进程崩溃后如何恢复。
- 工具调用失败后如何安全重试。
- 多个任务并发时如何不乱。
- 出问题时如何通过日志和 trace 找回现场。

## 先把几个词说清楚

这些词后面会反复出现，先用直白语言解释一下：

| 词 | 通俗解释 |
| --- | --- |
| Cloud Agent runtime | 让 Agent 在云端持续运行的底座。它负责保存状态、调用工具、处理失败、恢复任务、记录过程。 |
| run | 一次 Agent 任务。比如“分析一份简历并生成 7 天成长计划”。 |
| durable execution | 任务不能因为进程崩溃就丢了。服务挂了再起来，还能接着之前的位置继续。 |
| checkpoint | 任务执行过程中的存档点。像游戏存档一样，出事后从最近的安全位置继续。 |
| idempotency / 幂等 | 同一个操作重复执行多次，结果仍然安全。比如重试支付时不会重复扣钱。 |
| observability / 可观测性 | 系统出问题时，你能通过日志、trace、metrics 看懂发生了什么。 |

这次转型的核心，就是从“写页面和交互”进入“管理长期任务、失败、恢复和并发”的世界。

## 成功是什么

A 的验收标准必须独立于生意是否跑通。也就是说，就算产品还没有商业成功，你也要能证明自己完成了能力升级。

成功的标志是：你能独立设计并实现一个小而完整的 Cloud Agent runtime。它至少要做到：

- 有清晰的 run 生命周期，比如 `pending → running → paused → completed → failed`。
- 进程崩溃后能从 checkpoint 恢复。
- 恢复后不会重复执行已经完成的副作用，比如重复写数据库、重复扣费、重复发请求。
- 工具调用可以安全重试。
- 长期记忆重启后不丢。
- 有并发控制，比如限制同时跑多少任务，并能正确取消任务。
- 有结构化日志和 trace，能从日志里还原一次 run 的过程。

同时，你还要能讲清这些取舍：

- 为什么分布式系统里很难做到真正的 exactly-once。
- at-least-once 为什么常见，以及它为什么要求幂等。
- checkpoint 和 event sourcing 分别适合什么场景。
- 为什么“把数据存进数据库”不等于“任务能可靠恢复”。

每个里程碑都要留下公开 Evidence：文章、架构图、commit、demo 或复盘。

## Current Self：你已经有什么优势

前端不是从零开始。很多前端经验可以迁移过来，但要记住一句话：

```text
类比是桥，不是答案。
```

前端概念能帮你快速理解后端概念，但真正要学会的是“相似处背后的差异”。

| 前端已有知识 | 可以帮你理解什么 | 容易误解的地方 |
| --- | --- | --- |
| Redux / React state | run 的状态 | 前端 state 多在内存里，刷新后可以重建；run state 必须活过进程崩溃。 |
| Promise / async-await | goroutine / channel / 并发 | JS 主要是单线程并发；Go 会真的并行，需要处理竞态、锁和取消。 |
| `Promise.all` | worker pool / 有上限的并行 | 后端不能无限并发，要控制数量、背压和资源占用。 |
| try/catch | 失败处理 / 重试 | 网络失败不能只 catch。重试前必须保证操作幂等。 |
| component lifecycle | run / task 生命周期 | 组件可以重新 render；run 恢复时不能重跑已经完成的副作用。 |
| fetch / API 调用 | tool 调用 / 模型编排 | tool 调用可能部分成功，所以要设计成可重试。 |
| SSE / WebSocket 消费端 | 服务端流式输出 | 你过去主要消费流；现在要生产流，还要处理断线和重连。 |
| TS 类型 / 接口 | Go 类型 / interface | 这是迁移最顺的部分，是你的优势区。 |
| CI / 构建工具链 | 部署 / 可观测性 | 思路相通，但要补 trace、metrics、结构化日志。 |

## 你最需要补的能力

这次转型的主战场不是“会不会写 Go 语法”，而是下面这些后端和分布式系统能力：

```text
- 持久化：状态如何在进程崩溃后还存在
- 数据库和事务：哪些状态必须一起写成功
- 幂等：重复执行为什么不能造成重复副作用
- 重试和去重：失败后如何安全再试一次
- Go 并发：goroutine、channel、mutex、context
- 长时任务状态机：一个任务从开始到结束要经历哪些状态
- 崩溃恢复：checkpointing / event log / event sourcing
- 可观测性：trace、结构化日志、metrics
- 背压和队列：任务太多时如何不压垮系统
```

最重要的枢纽概念是：

```text
durable execution（可持久化执行）
```

简单说，就是让一个任务“即使系统中途挂掉，也能安全地继续”。Temporal 这类系统解决的核心问题就在这里。你亲手做一个 mini 版，就是最好的学习方式。

## Lite Challenges：用小项目把能力练出来

每个挑战都要小、真实、可验证。做完以后，它就是一块 Evidence。不要只证明“我看懂了”，要证明“我能做出来，并能解释为什么这样做”。

| # | 要做什么 | 为什么重要 | 怎么证明学会了 |
| --- | --- | --- | --- |
| 1 | 用显式状态机建模 run：`pending → running → paused → completed → failed`，并把状态存起来 | 这是所有长期任务的骨架 | 能画出状态转移图，并说清哪些状态跳转必须被拒绝 |
| 2 | 做崩溃恢复：run 跑到一半 `kill -9`，重启后从最后 checkpoint 继续 | 这是 durable execution 的核心 | 能证明重启后没有重复执行已经完成的 step |
| 3 | 做幂等工具调用：同一个写操作重复触发也不会造成重复副作用 | 后端重试很常见，不幂等就会出事故 | 能解释幂等 key / 去重表，并演示重复触发也安全 |
| 4 | 做长期记忆：重启不丢的 memory store + 简单检索 | Agent 需要记住用户背景和历史输出 | 能说清 memory 和 run state 的边界 |
| 5 | 做并发控制：用 worker pool 有上限地跑 N 个 step，并支持 context 取消 | 真正的服务不能无限开任务 | 能用 race detector 证明无竞态，并演示取消后 goroutine 干净退出 |
| 6 | 做流式进度：把 run 进度通过 SSE 推给前端 | 这是你的前端优势和后端能力的交汇点 | 能处理客户端断连后的重连和续传 |
| 7 | 做可观测性：每次 run 都有结构化日志和 trace id | 出问题时必须能找回现场 | 能只凭日志还原“这次 run 做了什么、在哪失败” |
| 8 | 做 Agent loop：3 个工具的注册表 + `plan → call → observe → continue` 循环 | 这是 Cloud Agent runtime 的最小工作闭环 | 能说明工具边界、失败路径，以及什么时候必须停下来等人 |

建议顺序：

```text
1 → 2 → 3
```

这三步是 durable execution 的核心三连。先把“状态、恢复、幂等”打通，再做记忆、并发、流式和可观测。

## 常见误区

这些误区是前端转后端时最容易踩的坑：

```text
- Redux state ≠ 持久化的 run state
  一个主要在内存里重建，一个必须活过进程崩溃。

- Promise.all ≠ goroutine
  Go 是真并行，要处理竞态，也要用 context 正确取消。

- try/catch ≠ 分布式重试
  网络失败不能只接住异常，还要保证重试不会造成重复副作用。

- React re-render ≠ run resume
  页面重渲染可以重复计算，但任务恢复绝不能重复执行已完成的副作用。

- 一次 tool 调用 ≠ 一次普通 fetch
  tool 可能部分成功，必须按“可安全重试”的方式设计。

- 存到数据库 ≠ durable execution
  关键不是有没有存，而是崩溃后能不能恢复到正确的执行进度。
```

## 阶段计划：约 12 周，A 优先

### 阶段 1：Go + 并发基本功

目标：把 `main.go` 从 health server 扩展成有 run 生命周期的服务。

要学：

- Go 语法、interface、error 处理。
- goroutine / channel / context / mutex。
- run 状态机建模。

产出：

- Lite Challenge 1。
- 一张 run 状态转移图。

### 阶段 2：持久化 + 崩溃恢复

目标：让 run 不怕服务重启。

要学：

- SQLite / 嵌入式存储。
- 事务。
- checkpoint。
- event log。
- 幂等 key 和去重表。

产出：

- Lite Challenge 2、3。
- 一篇崩溃恢复复盘。

### 阶段 3：Agent loop + 工具 + 记忆

目标：跑通一个能真正干活的最小 Agent。

要学：

- 工具注册表。
- `plan → act → observe` 循环。
- 模型 API 编排。
- 长期记忆层。

产出：

- Lite Challenge 4、8。
- 一个可演示的最小 Cloud Agent runtime。

### 阶段 4：生产级能力

目标：让 runtime 更接近真实产品可用状态。

要学：

- 结构化日志。
- trace。
- metrics。
- SSE 流式输出。
- 部署。

产出：

- Lite Challenge 5、6、7。
- 一份完整架构图。
- 一份可公开讲述的转型案例。

## Evidence 计划：边学边攒可展示资产

这些 Evidence 既证明你完成了 A（个人转型），也会自然积累 C（infra 产品化）的种子。

```text
- backend/ 仓库本身
  从 20 行 health server 长成 Cloud Agent runtime。
  git history 就是你的转型轨迹。

- 每个阶段一篇 build-in-public 文章
  例如：
  "前端工程师如何用 Go 给 Agent runtime 做崩溃恢复"
  "我踩过的第一个 goroutine 泄漏，和 context 救了我什么"

- 架构图
  run 状态机、恢复流程、agent loop、SSE 流式进度。

- Demo
  展示一次 run 如何开始、暂停、崩溃、恢复、继续、完成。

- 面试 / 公开表达稿
  讲清楚：从前端到 Cloud Agent，我造了什么，学会了什么判断。
```

当有人开始问“我能不能单独用你这个 runtime”时，说明 C 的势能开始出现。那时再考虑把垂直 app 的技术积累释放成 infra 产品，而不是现在提前分心。

## 给自己的诚实约束

```text
- A 和 B 分开亮灯
  A 成功 = 你能独立造出 runtime，并有公开 Evidence。
  不要用“我还在学习”掩盖“生意没有跑通”，也不要用“生意很忙”掩盖“核心能力没有钻深”。

- commodity 不要自己造
  auth、支付、向量库、模型 API、前端基础设施，尽量拼现成。
  时间要留给 run 生命周期、恢复、幂等、并发、可观测这些核心能力。

- 核心要钻深
  这次转型最值钱的部分不是多写几个接口，而是学会管理长任务、失败、恢复和并发。

- 每周复盘
  问自己：这周写的代码是在帮助 A 钻深，还是不小心提前为 B/C 花了太多时间？
```
