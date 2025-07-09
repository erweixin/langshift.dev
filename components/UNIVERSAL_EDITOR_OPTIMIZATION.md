# Universal Editor 优化 - Pyodide 按需加载

## 问题描述

之前的实现存在以下性能问题：

1. **过早加载**：在组件初始化时就会加载所有语言的运行时，包括 Pyodide
2. **不必要的预加载**：即使没有 Python 代码，也会预加载 Python 库
3. **资源浪费**：Pyodide 是一个相当大的运行时（约 50MB+），不应该在没有 Python 代码时加载

## 优化方案

### 1. 按需加载运行时

- **之前**：组件初始化时加载所有运行时
- **现在**：只在用户点击"运行"按钮时才加载对应的运行时

```typescript
// 按需加载运行时
const runCode = async (language: string) => {
  let runtime = runtimes.get(language)
  if (!runtime) {
    // 只在需要时加载运行时
    runtime = await runtimeManager.getRuntime(language)
    setRuntimes(prev => new Map(prev.set(language, runtime)))
  }
  // ... 执行代码
}
```

### 2. 优化 Python 库预加载

- **之前**：预加载所有常用库（numpy, pandas, matplotlib 等）
- **现在**：只预加载基础库，其他库按需加载

```typescript
// 只预加载基础库
const basicLibraries = [
  'json', 'datetime', 'math', 'random', 'os', 'sys', 're', 
  'collections', 'itertools', 'functools', 'time', 'pathlib'
]
```

### 3. 智能库加载

- **之前**：所有库都尝试预加载
- **现在**：分析代码中的 import 语句，只加载需要的库

```typescript
// 分析代码中的 import 语句
const importRegex = /^(?:from\s+(\w+)(?:\.\w+)*\s+import|import\s+(\w+)(?:\.\w+)*)/gm
const matches = [...code.matchAll(importRegex)]

// 只加载非基础库
for (const lib of libraries) {
  const packageName = libraryMap[lib]
  if (packageName && !basicLibraries.has(packageName)) {
    await pyodide.loadPackage(packageName)
  }
}
```

## 性能提升

### 1. 初始加载时间

- **之前**：页面加载时立即开始下载 Pyodide（~50MB）
- **现在**：页面加载时只加载轻量级的 JavaScript 运行时

### 2. 内存使用

- **之前**：即使不使用 Python，也会占用 Pyodide 的内存
- **现在**：只有使用 Python 时才占用相关内存

### 3. 网络请求

- **之前**：预加载所有 Python 库，产生大量网络请求
- **现在**：按需加载，减少不必要的网络请求

## 使用场景

### 场景 1: 只有 JavaScript 代码
- **行为**：不会加载 Pyodide
- **性能**：快速加载，低内存占用

### 场景 2: 只有 Python 代码
- **行为**：点击运行时才加载 Pyodide
- **性能**：延迟加载，但只加载一次

### 场景 3: 对比模式
- **行为**：分别按需加载两个运行时
- **性能**：避免同时加载多个大型运行时

### 场景 4: Python 使用外部库
- **行为**：先加载 Pyodide，再按需加载特定库
- **性能**：避免预加载所有可能用不到的库

## 测试方法

访问 `/test-universal-editor` 页面可以测试以下场景：

1. **JavaScript 测试**：验证不会加载 Pyodide
2. **Python 测试**：验证按需加载 Pyodide
3. **对比模式测试**：验证分别加载两个运行时
4. **外部库测试**：验证动态加载 Python 库

## 监控和调试

### 控制台日志

- `预加载基础 Python 库: {lib}` - 基础库预加载
- `动态加载库: {packageName}` - 按需加载外部库
- `Pyodide 初始化失败: {error}` - 初始化错误

### 网络请求监控

使用浏览器开发者工具的网络面板可以观察到：
- 只有使用 Python 时才会下载 Pyodide 相关文件
- 只有使用特定库时才会下载对应的库文件

## 未来优化方向

1. **缓存策略**：缓存已加载的运行时，避免重复加载
2. **预加载提示**：在用户输入 Python 代码时提示正在预加载
3. **渐进式加载**：先加载核心功能，再加载扩展功能
4. **Web Workers**：将运行时加载移到 Web Worker 中，避免阻塞主线程

## 兼容性说明

- 支持所有现代浏览器
- 需要网络连接来下载 Pyodide 和 Python 库
- 建议在网络较慢的环境下使用，避免频繁切换语言 