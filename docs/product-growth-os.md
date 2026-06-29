# Personal Growth OS 产品设计

> 本文档沉淀 Lites Cloud Agent 在产品层面的方向：以职业跃迁为核心入口，但不局限于转行；底层能力是帮助用户把已有知识、经验和认知方式迁移、重组、扩展成目标能力。

## 一句话定位

Lites 是一个 AI-native Personal Growth Platform，帮助互联网从业者从当前能力出发，构建目标能力，并通过行动、反馈和长期跟踪完成真实成长。

更短的表达：

```text
From where you are to who you need to become.

从你已经会的，长出你接下来需要的。
```

产品不应该被定义成普通学习平台，也不只是转行工具。职业跃迁是最清晰、最强烈的入口，但平台真正解决的是：

```text
Current Self -> Target Self
```

## 核心判断

前 AI 时代的学习产品通常围绕“课程”和“知识点”组织：

```text
目标岗位 -> 课程大纲 -> 逐章学习 -> 测验 -> 完课
```

Lites 应该围绕“成长”和“迁移”组织：

```text
用户背景
-> 目标能力
-> 已有经验可迁移资产
-> 能力缺口
-> 个性化知识网络
-> 项目行动
-> 反馈复盘
-> 成长证据
```

知识只是开始，成长才是目的。

```text
Goal
+ Knowledge
+ Action
+ Reflection
= Growth
```

## 目标用户

首批用户聚焦在想要完成能力跃迁的互联网从业者，尤其是已经有一定经验、但需要面向 AI 时代重新扩展能力的人。

典型用户：

- 产品经理想转向 AI 产品经理。
- 前端工程师想转向全栈工程师。
- 前端工程师想学习 iOS 开发。
- 初级工程师想成长为高级工程师或 Tech Lead。
- 前端工程师想快速掌握 ProseMirror、WebRTC、Three.js、LLM App 等复杂技能。
- 设计师、运营、产品想获得更强的 AI-native 创作与交付能力。

这类用户通常不缺资料，而是缺少：

- 对目标能力的结构化理解。
- 从已有经验到新领域的迁移路径。
- 能够持续推进的行动计划。
- 对学习产出的反馈和纠偏。
- 可以用于求职、转岗、晋升或作品展示的成长证据。

## Mission 类型

产品主入口可以是 Mission。Mission 不是课程，而是一段有目标、有时间、有证据的成长旅程。

### 1. Career Transition

从 A 职业到 B 职业，是最强主场景。

示例：

- 产品经理 -> AI 产品经理。
- 前端工程师 -> 全栈工程师。
- 前端工程师 -> iOS 工程师。
- 运营 -> 产品经理。
- 设计师 -> 独立产品创造者。

这类 Mission 的重点是跨领域知识迁移、能力缺口识别、项目作品积累和目标角色表达能力。

### 2. Role Upgrade

同职业内升级。

示例：

- 初级前端 -> 高级前端。
- 执行型产品 -> 业务负责人。
- 普通工程师 -> Tech Lead。
- CRUD 后端 -> 架构型后端。

这类 Mission 的重点是系统思维、技术判断、业务理解、沟通表达、项目 ownership 和决策能力。

### 3. Skill Expansion

同职业内学习陌生复杂技能。

示例：

- 前端工程师学习 ProseMirror。
- 前端工程师学习 Three.js。
- 后端工程师学习 Kubernetes。
- 产品经理学习数据分析。
- 设计师学习 AI 工作流。

这类 Mission 的重点是用已有知识加速理解陌生系统，而不是从零开始线性刷文档。

### 4. Project-Based Growth

以项目倒逼成长。

示例：

- 做一个 Notion-like editor。
- 做一个 AI Agent 应用。
- 做一个 SaaS MVP。
- 做一个作品集项目。
- 做一个可面试展示的系统设计案例。

这类 Mission 的重点是让知识落到可展示、可复盘、可被评价的真实输出。

## 产品方法论

### 1. 从用户已有背景出发

用户进入平台后，不应该先被要求选课，而应该先建立 Personal Context。

可输入的信息包括：

- 简历。
- 过往项目。
- GitHub、作品集、博客。
- 当前技能栈。
- 目标岗位 JD。
- 收藏的学习资料。
- 当前困惑。
- 每周可投入时间。

Agent 基于这些信息生成：

- 当前能力画像。
- 可迁移资产。
- 目标能力画像。
- 能力缺口。
- 风险点。
- 第一阶段行动建议。

### 2. 生成个人化知识网络

平台不生成线性课程表，而生成 Knowledge Graph。

这个图谱回答：

- 哪些新知识可以用已有经验理解？
- 哪些概念只是相似，不能完全类比？
- 哪些是目标领域的核心枢纽概念？
- 哪些知识先学会后，会让其他知识变简单？
- 哪些缺口会阻碍用户完成真实项目？

例如前端工程师转 iOS：

| 已有前端知识 | 可迁移到 iOS 的理解 |
| --- | --- |
| React Component | SwiftUI View |
| State Management | App State / View State |
| DOM Layout | Native Layout System |
| Browser Runtime | iOS App Lifecycle |
| API 调用 | URLSession / async data flow |
| Web 性能 | 启动速度、渲染性能、内存管理 |

Agent 需要提醒用户：类比是桥，不是答案。相似但不相同的部分，才是学习中的关键转折点。

### 3. 用行动替代完课

每个知识节点都应该连接到行动，而不是只连接到阅读材料。

行动可以包括：

- 写一份分析。
- 访谈真实用户。
- 做一个代码实验。
- 完成一个最小可运行 demo。
- 复盘一个案例。
- 提交一个作品集 artifact。
- 用自己的话讲清楚一个概念。

判断成长的关键不是“看完了什么”，而是“能做出什么、解释什么、迁移什么”。

### 4. 持续跟踪和动态调整

Agent 持续跟踪：

- 用户完成了什么。
- 哪些只是看过，尚未形成能力。
- 哪些知识开始遗忘。
- 哪些输出质量不够。
- 下一步应该补知识、做项目，还是复盘。
- 当前 Mission 是否需要调整节奏或目标。

平台可以穿插多种学习机制：

- Anki：自动生成关键卡片，用于记忆高杠杆概念。
- 费曼学习法：要求用户用自己的话解释，再由 AI 判断理解质量。
- 项目 Review：对用户产出给出结构化反馈。
- 周复盘：更新能力画像、风险和下一步。
- 面试模拟：把成长转成可表达、可展示的竞争力。

## 核心产品模块

### Mission

Mission 是用户当前成长目标的容器。

它包含：

- 目标描述。
- 时间范围。
- 成功定义。
- 当前能力画像。
- 目标能力画像。
- 阶段计划。
- 每周节奏。
- 关键证据。
- 风险和阻塞。

示例：

```text
目标：6 个月成为 AI 产品经理

阶段 1：理解 AI 产品工作的核心差异
阶段 2：掌握需求分析、模型能力边界和评估方法
阶段 3：完成一个 AI 产品原型与完整 PRD
阶段 4：形成作品集、面试表达和复盘材料
```

### Map

Map 展示个人化知识网络。

它不是百科图谱，而是面向目标能力的作战地图：

- 已掌握节点。
- 可迁移节点。
- 高杠杆节点。
- 缺口节点。
- 项目依赖节点。
- 复习节点。

### Today

Today 展示今天最应该做的事。

它避免把用户扔进一堆课程资料里，而是给出少量可执行任务：

- 今日阅读。
- 今日代码实验。
- 今日输出。
- 今日复盘。
- 今日卡片。

### Evidence

Evidence 保存成长证据。

示例：

- PRD。
- 竞品分析。
- 用户访谈记录。
- Demo。
- GitHub commit。
- 技术文章。
- 架构图。
- 项目复盘。
- 面试回答稿。

这是平台区别于学习产品的关键：用户最后得到的不只是“完成率”，而是一组可用于转岗、求职、晋升和自我复盘的证据。

### Coach

Coach 是长期陪跑的 Agent。

它负责：

- 解释概念。
- 诊断卡点。
- 追踪进度。
- 调整计划。
- Review 输出。
- 生成复习。
- 做周复盘。
- 把用户的成长过程沉淀进长期记忆。

## Skill Expansion 示例：前端学习 ProseMirror

ProseMirror 是典型的 Skill Expansion Mission。它不只是新 API，而是一套和普通前端开发不同的编辑器心智模型。

普通学习路径会让用户从官方文档第一页开始读。Lites 应该先生成 Known-to-New Map：

| 前端已有知识 | ProseMirror 中的新概念 |
| --- | --- |
| DOM tree | ProseMirror document tree |
| HTML tag | schema node / mark |
| React state | EditorState |
| Redux action / reducer | transaction / step |
| event handler | plugin props / commands |
| controlled component | editor view + state update loop |
| custom component | NodeView |
| CSS highlight | decoration |
| undo / redo | history plugin |
| collaborative editing | step mapping / collab model |

同时需要标注误区：

- ProseMirror document 不是 DOM。
- EditorState 不是普通 React state。
- 不应该把 DOM 当成 source of truth。
- 不应该在 React render 中反复重建 EditorView。
- schema 约束会决定节点和 mark 是否能插入。
- selection、transaction、plugin 顺序经常是复杂 bug 的来源。
- NodeView 生命周期和 React 组件生命周期不能简单等同。

### 示例 Mission

```text
Mission：14 天掌握 ProseMirror，并完成一个可扩展编辑器原型
```

阶段 1：建立心智模型。

- 为什么不能直接操作 DOM？
- schema 是什么？
- document、selection、transaction、plugin 分别管什么？
- EditorState 和 EditorView 的边界是什么？

阶段 2：跑通最小编辑器。

- 创建最小 schema。
- 初始化 EditorState。
- 挂载 EditorView。
- 实现一次 transaction 更新。
- 看懂编辑前后的 document JSON。

阶段 3：掌握扩展机制。

- 写一个 command。
- 写一个 keymap。
- 写一个 input rule。
- 写一个 plugin。
- 用 decoration 做高亮。
- 用 NodeView 做自定义块。

阶段 4：完成真实项目。

- slash command 编辑器。
- 简化版 Notion block editor。
- Markdown shortcut editor。
- 评论 / 批注编辑器。
- 富文本表单编辑器。

### 验证方式

Agent 不只问“看了几页文档”，而是检查：

- 用户能否解释 transaction 为什么存在。
- 用户能否从 JSON 反推文档结构。
- 用户能否判断 bug 是 schema、selection、plugin 顺序还是 view 生命周期问题。
- 用户能否独立添加一个新 block 类型。
- 用户能否用 React / Redux 类比 ProseMirror，同时指出类比的误导之处。

## MVP 建议

首版不需要覆盖所有成长场景。建议先选择一个高频、高痛点、容易产出作品的切口。

推荐 MVP 场景：

```text
前端工程师 -> 全栈工程师 / AI 应用工程师
```

原因：

- 用户群体明确。
- AI 时代转型需求强。
- 前端经验有大量可迁移资产。
- 目标能力可以通过项目验证。
- 容易沉淀作品集和面试表达。

MVP 必须包含：

- 用户输入当前背景和目标。
- Agent 生成当前能力画像。
- Agent 生成目标能力画像。
- Agent 生成知识迁移图谱。
- Agent 生成 4 到 12 周行动计划。
- 用户提交每日或每周进展。
- Agent 更新缺口、下一步、复习卡片和项目建议。
- Evidence 区沉淀用户产出。

MVP 可以暂不做：

- 完整课程市场。
- 大而全的职业库。
- 完整社群。
- 复杂证书系统。
- 多 Agent 自治编排。
- 对所有技能的深度模板化支持。

## 和 Cloud Agent 架构的关系

产品层的 Personal Growth OS 依赖底层 Cloud Agent 能力：

- 长期任务：Mission 会持续数周到数月，需要可恢复 run 和状态机。
- 记忆：用户背景、历史输出、误区、偏好和成长证据需要长期保存。
- 工具：需要读取文档、分析简历、生成卡片、Review 代码或产出。
- 实时：用户需要看到 Agent 正在分析、计划、反馈和更新。
- 审计：成长计划、能力画像和 AI 反馈应可追踪、可解释。
- 安全：用户资料、简历、作品和职业目标都属于敏感个人数据。

因此，底层不是简单聊天机器人，而是一个可持续运行、可追踪、可恢复的成长工作流系统。

## 产品边界

主入口：职业跃迁。

核心能力：知识迁移。

最终形态：个人成长操作系统。

平台应该避免落入三个误区：

- 不做普通课程聚合器。
- 不做只会生成计划的 AI 助手。
- 不做只追踪打卡的习惯工具。

真正要交付的是：

```text
基于用户已有背景的目标能力构建系统。
```

用户最后获得的不只是知识，而是新的能力结构、行动记录、真实作品和可以面向未来机会表达的成长证据。
