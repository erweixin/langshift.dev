---
title: LeetCode 题解模块编写规范
description: LangShift.dev 的 LeetCode 题解内容规范（按题单顺序、分组组织、折叠区块、UniversalEditor、测试用例要求）
---

本文用于规范 `content/docs/leetcode/` 下的 LeetCode 题解内容，确保**样式一致**、**可运行**、**可扩展**，并且方便后续继续新增分组/题目。

## 目标与原则

- **按题单顺序**：内容顺序以 `content/docs/leetcode/list.json` 为准。
- **按分组组织**：每个题单分组单独一个 MDX 文件（例如 `01-hash.mdx`）。
- **每题三大块默认折叠**：只在点击后展开，提升阅读效率。
- **最优解统一用 JavaScript**：不要使用 TypeScript（不写类型、不写泛型）。
- **最优解必须可运行**：每道题最优解至少提供 **5 个测试用例**（`console.log(...)`）。
- **包含原题内容**：通过组件动态拉取并渲染题面（避免仓库内复制粘贴整段题面）。

## 目录结构与命名

推荐结构：

- `content/docs/leetcode/index.mdx`：模块介绍页 + 分组入口
- `content/docs/leetcode/list.json`：题单与分组（数据源）
- `content/docs/leetcode/meta.json`：侧边栏/导航配置（按顺序列出各分组页面）
- `content/docs/leetcode/01-xxx.mdx`：分组页（按题单顺序）
- `components/leetcode/LeetCodeProblem.tsx`：原题内容渲染组件

分组文件命名建议：

- 以两位序号开头：`01-...`, `02-...`，便于直观排序
- 文件内标题与分组名对应：例如「哈希」「双指针」等

## 分组页（MDX）模板

每个分组页至少包含：

- Frontmatter：`title`、`description`
- 引入原题组件：`LeetCodeProblem`
- 分组说明（强调“顺序与 list.json 一致”）
- 题目列表：每题一个小节，顺序严格一致

示例骨架（按需修改 title/slug/题号）：

```mdx
---
title: 哈希（题单顺序）
description: 热题 100 - 哈希分组（按题单顺序）：1 / 49 / 128
---

import { LeetCodeProblem } from "@/components/leetcode/LeetCodeProblem"

本页包含题单「哈希」分组的全部题目，顺序与 `list.json` 保持一致。原题正文来自力扣官方页面动态拉取。

## 1. 两数之和
<LeetCodeProblem titleSlug="two-sum" />

<!-- 下面三块默认折叠：最优解 / 讲解 / 类似题目 -->
...
```

## 每题内容规范（必须项）

每道题必须包含以下三块（**默认折叠**）：

1. **最优解**（折叠）
2. **最优解讲解**（折叠）
3. **类似题目**（折叠）

折叠实现方式（MDX 原生）：

```mdx
<details>
<summary><strong>最优解</strong>（一句话说明套路）</summary>

...内容...

</details>
```

### 1) 最优解（折叠）

- 必须使用 `UniversalEditor` 包裹
- 必须使用 **JavaScript**（不要 TS）
- 建议使用 `!! js` 标记，便于编辑器识别
- 必须包含 **5 个**可运行测试用例（`console.log(...)`）

模板：

```mdx
<details>
<summary><strong>最优解</strong>（这里写套路，例如：哈希表/双指针/滑动窗口）</summary>

<UniversalEditor title="内存模型比较" compare={true}>
```javascript !! js
function solve(input) {
  // ...
}

// 5 个测试用例（覆盖：常规 / 边界 / 重复 / 负数 / 空输入 等）
console.log(solve(/*...*/))
console.log(solve(/*...*/))
console.log(solve(/*...*/))
console.log(solve(/*...*/))
console.log(solve(/*...*/))
```
</UniversalEditor>

</details>
```

> 备注：目前 `compare={true}` 即使只放一个代码块也能渲染；如果未来需要对比两种写法，再添加第二个代码块即可。

### 2) 最优解讲解（折叠）

- 通俗易懂，强调“怎么想到”的过程
- 尽量用「大白话」解释，不要使用专业术语，而且要有推理过程
- 推荐 3～6 条要点，尽量用“你在做什么/为什么这样做”的口吻

模板：

```mdx
<details>
<summary><strong>最优解讲解</strong>（通俗版）</summary>

- **一句话目标**：...
- **关键观察**：...
- **为什么这样是最优**：...

</details>
```

### 3) 类似题目（折叠）

- 给一个可迁移的“套路模板”（JS）
- 尽量抽象成“输入/状态/更新/退出条件”

模板：

```mdx
<details>
<summary><strong>类似题目</strong>（这里写套路名）</summary>

```javascript
// 这里写同类题通用模板（不要 TS）
```

</details>
```

## 原题内容（必须项）

每题必须在标题下方包含原题渲染组件：

```mdx
<LeetCodeProblem titleSlug="two-sum" />
```

- `titleSlug` 来自 `list.json` 的 `titleSlug`
- 原题内容通过 `https://leetcode.cn/graphql` 拉取 `translatedContent`（中文题面）

## 侧边栏/导航（meta.json）

`content/docs/leetcode/meta.json` 中的 `pages` 用于控制侧边栏顺序。

规则：

- `pages` 里按题单分组顺序写：`["index", "01-hash", "02-two-pointers", ...]`
- 新增分组页后，务必把文件名（不含扩展名）追加到 `pages`

## 质量检查清单（提交前自检）

- **顺序**：分组与题目顺序与 `list.json` 一致
- **折叠**：每题三块（最优解/讲解/类似）默认折叠
- **语言**：最优解与模板均为 JavaScript（无 TS 类型/泛型）
- **可运行**：每题最优解有 5 个测试用例
- **原题**：每题都有 `<LeetCodeProblem titleSlug="..."/>`

