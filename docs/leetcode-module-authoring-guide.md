---
title: LeetCode 题解模块编写规范
description: LangShift.dev 的 LeetCode 题解内容规范（按题单顺序、分组组织、折叠区块、UniversalEditor、测试用例要求）
---

本文用于规范 `content/docs/leetcode/` 下的 LeetCode 题解内容，确保**样式一致**、**可运行**、**可扩展**，并且方便后续继续新增分组/题目。

## 目标与原则

- **按题单顺序**：内容顺序以 `content/docs/leetcode/list.json` 为准。
- **按分组组织**：每个题单分组单独一个 MDX 文件（例如 `01-hash.mdx`）。
- **每题三大块默认折叠**：只在点击后展开，提升阅读效率。
- **最优解统一用 JavaScript**：不要使用 TypeScript（不写类型、不写泛型）；**避免** `Constructor.prototype.method = function` 等显式原型链写法，优先 **`class` 方法**或**纯函数**，与课内「少用原型链」风格一致。
- **最优解必须可运行**：每道题最优解至少提供 **5 个测试用例**（`console.log(...)`）。
- **包含原题内容**：通过组件动态拉取并渲染题面（避免仓库内复制粘贴整段题面）。

## 目录结构与命名

推荐结构：

- `content/docs/leetcode/index.mdx`：模块介绍页 + 分组入口
- `content/docs/leetcode/list.json`：题单与分组（数据源）
- `content/docs/leetcode/meta.json`：分组根节点元数据（仅 `title` 与 `root`；侧边栏顺序由文件名排序决定，见下文）
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
- **`UniversalEditor` 仅支持两栏对比**：题解内固定为 **JavaScript + Go**（`title` 使用 `参考解法（JavaScript / Go）`），不要把第三种语言写进同一个 `UniversalEditor`。
- **Rust**：放在 `UniversalEditor` **之后**、仍在本题「最优解」`<details>` 内，用嵌套的 `<details>` 折叠，标题建议 `Rust 参考`，代码块使用普通 ` ```rust `（只展示、不参与双栏编辑器）。

模板：

```mdx
<details>
<summary><strong>最优解</strong>（这里写套路，例如：哈希表/双指针/滑动窗口）</summary>

<UniversalEditor title="参考解法（JavaScript / Go）" compare={true}>
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

```go !! go
package main

import "fmt"

func main() {
  // ...
}
```
</UniversalEditor>

<details>
<summary><strong>Rust 参考</strong>（点击展开）</summary>

```rust
// 与题意一致的参考实现（仅展示）
```

</details>

</details>
```

> 备注：`compare={true}` 下站点里的 `UniversalEditor` 只会渲染**前两个**代码块；第三语言必须用上面的 `details` 方式，避免被丢弃或布局异常。

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

`content/docs/leetcode/meta.json` 与仓库其他文档模块一致，**只保留** `title` 与 `root`。Fumadocs 会按**文件名**对同目录下的页面排序，因此分组页应继续使用 **`01-`、`02-`…** 前缀，使侧边栏顺序与题单一致。

新增分组页时：只需加入 `NN-slug.mdx`（`NN` 为两位序号），**无需**再维护 `pages` 列表。

## 质量检查清单（提交前自检）

- **顺序**：分组与题目顺序与 `list.json` 一致
- **折叠**：每题三块（最优解/讲解/类似）默认折叠
- **语言**：最优解与模板均为 JavaScript（无 TS 类型/泛型）
- **可运行**：每题最优解有 5 个测试用例
- **原题**：每题都有 `<LeetCodeProblem titleSlug="..."/>`

