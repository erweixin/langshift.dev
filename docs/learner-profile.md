# 学习者画像：结构化的长期记忆

> 这篇讲系统到底“记住了你什么”。它不是聊天记录的简单堆叠，而是一份可查询、可展示、可被用户纠正的学习画像：你哪些概念会了，哪些误解还没消掉，喜欢怎样的解释方式，最近节奏怎样。更泛化的对话记忆见 [memory.md](./memory.md)；这里讲的是结构化、可编辑的那部分。

几个词先说清：

- **画像**：系统对学习者当前状态的结构化理解，不是聊天记录全文。
- **投影**：从事件历史算出来的当前画像；需要时可以重建。
- **provenance**：每条画像字段的来源，也就是“这条判断从哪次 Review / 对话来”。
- **分面摘要**：给不同场景用的短版画像，比如 Coach 对话用一份，Review 用另一份。

## 问题、决策与风险

**问题**：产品很多地方都在承诺“我懂你”：任务难度要合适，课程讲解要贴近用户，Review 要盯住他的卡点，完成态要告诉用户今天系统记住了什么。如果这些只藏在零散的向量记忆里，就很难回答“他对崩溃恢复到底到什么水平了”“他是不是更喜欢 Redux 类比”这类明确问题，也没法让用户检查和纠正。

**决策**：画像从 event 推出来，不让 LLM 直接随手改。LLM 可以产出 Review 结论、偏好信号、记忆候选，这些先变成 event；投影逻辑再按固定规则更新画像。每个字段都要知道来源，未来才能解释和回滚。

**忽略后果**：画像会变成一段模型随手写的自由文本，查不了、改不了、也不知道从哪来的。用户一旦发现“系统记住的我”是错的却不能纠正，信任会直接崩掉。

## Schema

```yaml
learner_profile:
  version: 42                      # 每次更新递增，context_manifest 引用它
  capabilities:                    # 能力项（对应 UI 能力地图）
    - id: durable-exec
      name: 崩溃恢复 / durable exec
      level: 2                     # 0 未接触 / 1 看过 / 2 能解释 / 3 能实现 / 4 能教
      confidence: 0.8              # 随时间衰减，触发复习探针
      evidence_refs: [ev_123]      # 支撑该等级的 Evidence
      updated_by: run_456          # 来源：哪次 Review 更新的
  misconceptions:                  # 误解记录（Review 的核心产出）
    - statement: 把 failed 当成终态
      status: active               # active / probing / resolved
      source: run_456
      planned_probe: task_789     # 用哪个任务验证它已被纠正
  preferences:
    explanation_depth: shallow-first   # 讲解偏好（来自选中重写）
    analogy_anchors: [redux, react]    # 类比锚点（用户已有经验）
    session_minutes: 35                # 单次时长偏好
    review_style: strict               # 提交页选择的沉淀
  rhythm:
    streak: 3
    grace_used: false              # 宽容机制：断一天不清零，用掉标记
    weekly_pattern: [1,1,1,0,1,0,0]
  missions: [...]                  # 目标状态引用（另有投影，此处只留指针）
```

## 更新规则

**只有 event 能更新画像**。每类 event 都有明确规则，避免同一件事今天被模型解释成 A，明天又解释成 B：

| Event | 更新 | 备注 |
| --- | --- | --- |
| `ReviewCompleted` | capability level/confidence、misconception 增改、evidence_refs | 一次 Review 最多让一个能力升一级，避免跳太快 |
| `PreferenceRecorded`（选中重写等） | preferences | 同类信号出现至少 2 次才固化为偏好；一次只算候选 |
| `ExerciseRunRecorded` | capability confidence 微调 | 通过判断点测试不等于马上升级，只提高置信度 |
| `TaskSkipped / TooHard` | rhythm、难度信号 | |
| `DayCompleted` | streak（含 grace 逻辑） | 惩罚性 streak 是留存毒药，宽容规则写死在投影里 |
| `UserEditedProfile` | 任意字段 | 用户改动权威性最高，带用户来源记录 |

投影必须幂等：同一条 event 重放一次，不应该把能力加两遍。这和平台“消息可能至少投递一次”的语义对齐。

**矛盾处理**：新 event 与现状冲突时（Review 说能力升了、用户手动降过级），**用户改动优先，系统改动只能提出**——生成一条待确认项出现在完成态记忆卡（"我观察到 X，对吗？"），确认后才落画像。这与 [memory.md](./memory.md) 的矛盾处理原则一致，但阈值更保守。

## 召回：画像怎么进 prompt

画像不整体塞进 prompt。我们提前生成几份短摘要（profile summary），画像变化后刷新；不同调用面只拿自己需要的那份：

| 面 | 摘要内容 | 预算 |
| --- | --- | --- |
| Coach 对话 | preferences + active misconceptions + 当前任务上下文 | ~300 token |
| 课程生成 / 重写 | capabilities（相关子集）+ preferences | ~200 token |
| Review | capabilities + misconceptions（判断"这次该盯什么"） | ~300 token |
| 任务生成 | capabilities + rhythm + 上次 Review 的 next | ~250 token |

摘要文本进缓存前缀（画像不变则缓存命中），`context_manifest` 记录 `profile_version`——事后能回答"当时系统以为用户是什么水平"。情景细节（某次对话里说过什么）走 [memory.md](./memory.md) 的向量召回，不进画像。

## 用户可见、可编辑、可带走

这不是附加功能，而是“数据属于用户”的兑现：

- **可见**：完成态记忆卡展示当天新增；Coach 页提供完整画像视图。系统记住的每一条，都要带上“来自哪次 Review / 对话”的出处。
- **可编辑**：每条都能删除、纠正。删除会产生 `UserEditedProfile` event，**同时让引用它的派生物失效**，例如摘要重刷、相关向量记忆标记。
- **可导出**：整个画像 + 事件历史导出为 JSON + Markdown（人可读版）。自部署用户迁移到托管（或反向）靠这个格式。

## 衰减与复习探针

capability 的 `confidence` 随未使用时间衰减（level 不自动降）。衰减过阈值 → 任务生成器收到"该复习"信号 → 自然地把复习探针织进后续任务（间隔重复的产品化形态，不做独立的刷题模式）。misconception 的 `planned_probe` 同理：声称已纠正的误解要在真实任务里再验证一次才置 resolved。

## Do / Don't

| 应该 | 不应该 |
| --- | --- |
| event → 投影，字段带来源 | 让 LLM 直接改画像 |
| 分面摘要进缓存前缀 | 全量画像塞进每个 prompt |
| 用户改动优先，系统矛盾只提出 | 系统悄悄覆盖用户的自我认知 |
| 删除联动派生物失效 | 删了画像留着旧摘要继续用 |
| streak 带宽容规则 | Duolingo 式断签清零 |
