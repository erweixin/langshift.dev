# 产品 → 平台映射

> 这篇是“翻译表”：把产品里的 Mission、Task、Lesson、Review、Coach，翻译成平台里的 event、run、artifact、memory。写第一行业务代码之前先读这篇，可以避免产品代码绕开平台语义各写各的。

几个词先说清：

- **event**：已经发生过的事实，比如“用户提交了 Evidence”“Review 完成了”。
- **run**：需要后台慢慢执行的一次任务，比如诊断、生成课程、做 Review。
- **投影**：从 event 算出来的当前状态，比如当前任务列表、当前画像。
- **delta**：只属于某个用户的小改动，比如“这段课程给我讲浅一点”的改写结果。
- **artifact**：系统保存的产物，比如课程内容、用户提交、作品化后的简历 bullet。

## 问题、决策与风险

**问题**：产品层说的是用户能看懂的东西：目标、今日任务、课程、作品、Review、Coach、画像。平台层说的是工程要处理的东西：run、event、command、tool、artifact、memory。两边各自都对，但如果中间没有翻译，就会卡在很具体的问题上：生成今日任务算不算一个 run？Review 结论怎么写回画像？Coach 要秒回，怎么还保留事件记录？

**决策**：只要产品状态真的变了，就写成 event，保持“事实都能追溯”。LLM 参与的动作分两类：用户正在等回复的，先直连流式返回，结束后补记 event；后台任务和 Review 这种可以等的，先记账，再让 worker 执行，也就是标准 run 循环。

**忽略后果**：平台会越做越“通用”，但产品真正急需的内容生成、画像、练习运行时没人负责；或者业务代码为了快，直接改数据库，最后同一件事在事件、投影、页面上各有一个版本。

## 领域对象映射

| 产品对象 | 平台原语 | 说明 |
| --- | --- | --- |
| Mission（目标） | 聚合投影 + `MissionCreated / MissionAdjusted` event | 这是用户长期目标，不是一次性任务 |
| Roadmap（阶段路线） | Mission 投影的一部分，由诊断 run 写入 | 每次重排路线都追加一个新版本，别覆盖旧事实 |
| Task（今日任务） | `TaskGenerated` event + 投影 | `task_id` 用来串起提交、Review 和学习状态 |
| Lesson（课程） | ContentArtifact，内容寻址 | 大部分内容跨用户复用；个人化只叠加在用户自己的 delta 上 |
| 练习运行 | 不经 LLM，走 exercise runtime | 用户点“运行测试”时只跑代码和测试，结果记成 `ExerciseRunRecorded` |
| Evidence（产出） | artifact + `EvidenceSubmitted` event | 从草稿到已 Review，再到可展示作品 |
| Review | 一个标准 run（强模型 + 只读输入） | 读提交内容和测试结果，写出 `ReviewCompleted` |
| Coach 对话 | conversation + 直连流式调用 + 事后 event | 为了秒回，不排进 run 队列；但对话事实仍要补记 |
| 学习者画像 | 结构化投影，见 [learner-profile.md](./learner-profile.md) | 系统“记住了你什么”，只能由 event 推出来 |
| 「Coach 记住了你」 | 画像更新 event 的用户可见回显 | 完成态记忆卡展示的就是这些新增事实 |
| 作品化产出 | artifact（craft 变体），关联源 Evidence | 把学习产出改写成简历 bullet、面试讲述等形式 |

## 产品动作 → 执行链路

| # | 产品动作 | 触发 | 链路 | 模型档 | 读取 | 写回 |
| --- | --- | --- | --- | --- | --- | --- |
| 1 | 冷启动诊断 + 路线生成 | onboarding 提交 | run（异步，秒级可接受，生成屏遮盖延迟） | 强 | 用户输入 / 导入材料 | Mission + Roadmap + 画像初始化 |
| 2 | 每日任务生成 | 完成循环后 / 定时 | run（后台） | 中 | 画像 + roadmap + 昨日 Review | `TaskGenerated` |
| 3 | 课程生成 | 任务确定后预生成 | run（内容管线，可缓存命中则跳过） | 强 | TaskSpec + 画像摘要 | ContentArtifact |
| 4 | 选中重写（讲浅一点等） | 用户选中段落 | **直连**流式，小模型 | 小 | 选中段落 + 上下文 + 偏好 | 用户内容 delta + `PreferenceRecorded` |
| 5 | Coach Drawer 对话 | 用户提问 | **直连**流式 | 小 / 中 | 画像摘要 + 当前任务 + 选中引用 | `ChatTurnLogged`（异步）+ 候选记忆 |
| 6 | 练习运行 | 用户点运行 | exercise runtime，无 LLM | — | 用户代码 + 测试集 | `ExerciseRunRecorded` |
| 7 | 提交 + Review | 用户提交 | run（异步，UI 显示 Review 进行中） | 强 | 代码 + 测试结果 + 反思 + 画像 | `ReviewCompleted` → 能力 delta / 误解 / memo / 明日任务种子 |
| 8 | 作品化 | 用户点作品化 | 直连或轻 run | 中 | Evidence + 目标岗位上下文 | craft artifact |
| 9 | 断更回归 | 间隔后登录 | run（后台） | 中 | 画像 + 中断时长 | 轻重启任务 |
| 10 | 画像摘要刷新 | 画像变更后 | 直连，小模型 | 小 | 结构化画像 | prompt 用摘要（缓存前缀） |

## 两条链路的规则

**任务面（run 循环）**：动作 1、2、3、7、9。先把“要做什么”写进 event/command，Worker 再领取执行，完成后写回 event。这类动作可以等几秒到几分钟，UI 也有生成中、Review 进行中这些等待状态。

**交互面（直连 + 事后记账）**：动作 4、5、8、10。请求处理器直接调用 LLM，并用 SSE 流式返回，**不经过队列**；等响应结束后，再异步追加 event。这样用户感觉快，事实源也能补齐。但要守两条纪律：

1. 交互面产生的**状态变化**（偏好、记忆候选、craft artifact）必须落 event，不允许只改投影。
2. 交互面失败不重试副作用——对话调用失败直接把错误给用户，没有 `outcome_unknown` 问题（无外部副作用）。

## 上下文组装（谁进 prompt）

| 动作 | 稳定前缀（可缓存） | 变化部分 |
| --- | --- | --- |
| 课程生成 | 系统提示 + 路线模板 | TaskSpec + 画像摘要 |
| Review | 系统提示 + 评审规则 | 代码 + 测试 + 反思 + 画像摘要 |
| Drawer 对话 | 系统提示 + 画像摘要 + 当前任务 | 对话历史 + 选中引用 |
| 选中重写 | 系统提示 + 偏好 | 选中段落 + 前后文 |

画像摘要是预生成的短文本（动作 10），按面裁剪：对话面用偏好 + 误解，生成面用能力 + 偏好，Review 面用能力 + 误解。`context_manifest` 记录使用的画像版本（对齐 [memory.md](./memory.md) 的可追溯要求）。

## 与平台文档的分工

| 产品需要 | 平台文档覆盖 | 缺口（本组新文档补） |
| --- | --- | --- |
| run / event / 幂等 / 恢复 | architecture / execution-model / concurrency | 无 |
| Review 的工具与安全 | tool-system / guardrails | Review 是只读输入，v0 无需审批流 |
| 情景记忆检索 | memory.md | 结构化画像 schema → [learner-profile.md](./learner-profile.md) |
| 课程质量与成本 | 无 | [content-pipeline.md](./content-pipeline.md) |
| 用户代码执行 | runtime-and-sandbox（面向 agent 工具） | 学习者 playground → [exercise-runtime.md](./exercise-runtime.md) |
| 定价能否成立 | llm-provider（机制） | 单位经济 → [unit-economics.md](./unit-economics.md) |
| 首版交付边界 | mvp-scope（平台 v1） | 产品切片 → [v0-product-slice.md](./v0-product-slice.md) |

## Do / Don't

| 应该 | 不应该 |
| --- | --- |
| 交互面直连流式、事后记账 | 让 Drawer 回复排 run 队列 |
| 状态变化一律落 event | 交互面图省事直接改投影 |
| 按动作选模型档（见 unit-economics） | 所有面统一用强模型 |
| 画像摘要预生成、进缓存前缀 | 每次请求把完整画像塞进 prompt |
| 生成屏 / Review 等待态遮盖任务面延迟 | 为了"实时感"把生成类动作也做成直连 |
