# 🌍 LangShift.dev - 多语言学习平台

> 专为开发者设计的编程语言转换学习平台，通过对比学习快速掌握新语言

[![Next.js](https://img.shields.io/badge/Next.js-15.3+-black.svg)](https://nextjs.org)
[![License](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-brightgreen.svg)](CONTRIBUTING.md)

## 📖 项目简介

LangShift.dev 是一个专门为开发者设计的编程语言转换学习平台。通过对比不同编程语言的语法特性和概念映射，帮助你快速理解新语言的核心概念，并能够将其应用到实际项目中。

我们的核心理念是：**通过已知语言理解未知语言**，让学习新编程语言变得简单高效。

## 🎯 学习目标

- 通过语言对比快速掌握新编程语言
- 理解不同语言的语法特性和设计哲学
- 构建多语言开发能力
- 学会在不同语言间迁移开发思维

## 🌐 支持的语言转换

### 🔄 JavaScript ↔ Python
- 从 JavaScript 开发者视角学习 Python
- 语法映射：变量、函数、类、异步编程
- 生态系统对比：npm vs pip、Node.js vs Python
- 实战项目：Web 开发、数据处理、自动化脚本

### 🔄 JavaScript ↔ Rust
- 从 JavaScript 开发者视角学习 Rust
- 内存管理：垃圾回收 vs 所有权系统
- 类型系统：动态类型 vs 静态类型
- 性能优化：解释执行 vs 编译优化

### 🚀 更多语言支持计划中...
- TypeScript ↔ Go
- Python ↔ Rust
- JavaScript ↔ Go

## 📚 学习模块结构

### 🚀 模块 0：语言转换学习法
- 为什么需要学习多种编程语言？
- 语言转换学习的核心方法论
- 环境搭建与工具配置
- 第一个跨语言项目：Hello, World!

### 🧱 模块 1：基础语法映射
- 变量声明与作用域对比
- 数据类型与结构映射
- 控制流语句对比
- 函数定义与调用方式

### 🧰 模块 2：模块化与项目组织
- 包管理与依赖系统对比
- 模块导入导出机制
- 项目结构规范
- 构建工具与开发环境

### 🧠 模块 3：编程范式对比
- 面向对象编程实现差异
- 函数式编程特性对比
- 异步编程模型对比
- 错误处理机制

### 🌍 模块 4：生态系统与工具链
- 标准库与第三方库对比
- 开发工具与 IDE 配置
- 调试与测试工具
- 部署与运维实践

### 🧪 模块 5：实战项目
- 跨语言项目架构设计
- 性能优化策略对比
- 最佳实践与设计模式
- 团队协作与代码规范

## 🛠️ 技术栈

### 平台技术
- **框架**: Next.js 15+ (App Router)
- **文档**: Fumadocs + MDX
- **样式**: Tailwind CSS
- **代码编辑器**: Monaco Editor
- **国际化**: 支持中英文

### 语言运行时
- **Python**: Pyodide (浏览器端 Python)
- **JavaScript**: V8 Engine
- **Rust**: WebAssembly (计划中)

### 开发工具
- **搜索**: Orama
- **类型检查**: TypeScript
- **代码质量**: ESLint, Prettier

## 🚀 快速开始

### 环境要求

- Node.js 18+ 
- pnpm (推荐包管理器)
- 现代浏览器 (支持 WebAssembly)

### 安装步骤

1. **克隆项目**
   ```bash
   git clone https://github.com/erweixin/langshift.dev.git
   cd langshift.dev
   ```

2. **安装依赖**
   ```bash
   pnpm install
   ```

3. **启动开发服务器**
   ```bash
   pnpm dev
   ```

4. **访问项目**
   打开浏览器访问 [http://localhost:3000](http://localhost:3000)

## 📁 项目结构

```
langshift.dev/
├── app/                    # Next.js App Router
│   └── [lang]/            # 国际化路由
│       ├── (home)/        # 首页
│       ├── docs/          # 文档页面
│       └── intro/         # 介绍页面
├── components/            # React 组件
│   ├── python-editor.tsx  # Python 代码编辑器
│   ├── side-by-side-code.tsx # 对比代码组件
│   └── ...
├── content/              # 文档内容
│   └── docs/            # 文档目录
│       ├── js2py/       # JavaScript → Python
│       ├── js2rust/     # JavaScript → Rust
│       └── ...
├── lib/                 # 工具函数
├── styles/              # 样式文件
└── README.md           # 项目说明
```

## 🎯 学习建议

1. **选择起点**: 从你最熟悉的语言开始
2. **对比学习**: 重点关注语法差异和概念映射
3. **动手实践**: 每个概念都要动手写代码验证
4. **项目驱动**: 通过实战项目巩固所学知识
5. **循序渐进**: 按照模块顺序学习，打好基础

## 🌟 特色功能

### 🔄 交互式代码编辑器
- 支持多种编程语言语法高亮
- 实时代码执行和错误提示
- 代码对比模式，直观显示差异

### 📚 结构化学习路径
- 模块化课程设计
- 渐进式难度递增
- 丰富的代码示例和练习题

### 🌍 多语言支持
- 中英文双语界面
- 国际化文档内容
- 本地化学习体验

## 🤝 贡献指南

欢迎提交 Issue 和 Pull Request！

1. Fork 本仓库
2. 创建特性分支 (`git checkout -b feature/AmazingFeature`)
3. 提交更改 (`git commit -m 'Add some AmazingFeature'`)
4. 推送到分支 (`git push origin feature/AmazingFeature`)
5. 打开 Pull Request

### 贡献类型
- 🐛 Bug 修复
- ✨ 新功能开发
- 📝 文档改进
- 🌍 国际化翻译
- 🎨 UI/UX 优化

## 📄 许可证

本项目采用 MIT 许可证 - 查看 [LICENSE](LICENSE) 文件了解详情。

## 🙏 致谢

感谢所有为这个平台做出贡献的开发者！

## 📞 联系我们

- 项目主页: [GitHub](https://github.com/erweixin/langshift.dev)
- 问题反馈: [Issues](https://github.com/erweixin/langshift.dev/issues)
- 讨论交流: [Discussions](https://github.com/erweixin/langshift.dev/discussions)

---

⭐ 如果这个项目对你有帮助，请给我们一个 Star！
