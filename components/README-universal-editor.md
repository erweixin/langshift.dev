# 通用多语言编辑器组件

`UniversalEditor` 是一个支持多种编程语言在线运行的高性能编辑器组件，基于 Monaco Editor 构建，支持 Python、JavaScript、TypeScript、Rust、C++、Java 等语言。

## 特性

- 🚀 **多语言支持**: 支持 Python、JavaScript、TypeScript、Rust、C++、Java
- ⚡ **性能优化**: 使用虚拟化渲染和懒加载技术
- 🔄 **动态加载**: 按需加载语言运行时和依赖库
- 🎨 **主题适配**: 自动适配明暗主题
- 📊 **对比模式**: 支持两种语言的代码对比
- 🛠️ **可扩展**: 易于添加新的编程语言支持

## 安装依赖

确保项目中已安装以下依赖：

```bash
npm install monaco-editor @monaco-editor/react
```

## 基本用法

```tsx
import UniversalEditor from './components/universal-editor'

function App() {
  return (
    <UniversalEditor
      title="Python 代码编辑器"
      primaryLanguage="python"
      code={[
        { lang: 'python', value: 'print("Hello, World!")' }
      ]}
      height={300}
      showOutput={true}
    />
  )
}
```

## 对比模式

```tsx
<UniversalEditor
  title="JavaScript vs TypeScript 对比"
  primaryLanguage="javascript"
  secondaryLanguage="typescript"
  compare={true}
  code={[
    { lang: 'javascript', value: 'console.log("Hello JS")' },
    { lang: 'typescript', value: 'console.log("Hello TS")' }
  ]}
  height={400}
  showOutput={true}
/>
```

## API 参考

### Props

| 属性 | 类型 | 默认值 | 描述 |
|------|------|--------|------|
| `title` | `string` | `'多语言代码编辑器'` | 编辑器标题 |
| `theme` | `'vs-light' \| 'vs-dark' \| 'auto'` | `'auto'` | 编辑器主题 |
| `readOnly` | `boolean` | `false` | 是否只读模式 |
| `showOutput` | `boolean` | `true` | 是否显示输出区域 |
| `compare` | `boolean` | `false` | 是否启用对比模式 |
| `code` | `CodeBlock[]` | `[]` | 代码块数组 |
| `height` | `number` | `300` | 编辑器高度 |
| `preloadLibraries` | `string[]` | `[]` | 预加载的库列表 |
| `allowDynamicImports` | `boolean` | `true` | 是否允许动态导入 |
| `primaryLanguage` | `Language` | `'python'` | 主语言 |
| `secondaryLanguage` | `Language` | `undefined` | 对比语言 |

### 支持的语言

```typescript
type Language = 'python' | 'javascript' | 'typescript' | 'rust' | 'cpp' | 'java'
```

### 代码块格式

```typescript
interface CodeBlock {
  lang: string    // 语言标识符
  value: string   // 代码内容
}
```

## 语言运行时

### Python (Pyodide)
- 使用 Pyodide 在浏览器中运行 Python
- 支持 numpy、pandas、matplotlib 等常用库
- 自动动态加载依赖库

### JavaScript/TypeScript
- 使用浏览器原生 JavaScript 引擎
- 支持 ES6+ 语法特性
- 捕获 console.log 输出

### Rust
- 集成 Rust Playground API
- 支持标准库和第三方 crate
- 实时编译和运行

### C++
- 集成在线 C++ 编译器 API
- 支持 C++17 标准
- 多种编译器选项

### Java
- 集成在线 Java 编译器 API
- 支持 Java 11+ 特性
- 完整的类库支持

## 性能优化

### 虚拟化渲染
- 使用 `VirtualizedEditor` 组件
- 只渲染可见区域的代码行
- 大幅提升大文件性能

### 懒加载
- 按需加载语言运行时
- 延迟加载 Monaco Editor
- 减少初始加载时间

### 缓存机制
- 运行时实例缓存
- 预加载库缓存
- 避免重复加载

## 自定义配置

### 预加载库

```tsx
<UniversalEditor
  primaryLanguage="python"
  preloadLibraries={['numpy', 'pandas', 'matplotlib']}
  code={[{ lang: 'python', value: pythonCode }]}
/>
```

### 主题配置

```tsx
<UniversalEditor
  theme="vs-dark"  // 强制暗色主题
  primaryLanguage="javascript"
  code={[{ lang: 'javascript', value: jsCode }]}
/>
```

### 只读模式

```tsx
<UniversalEditor
  readOnly={true}
  showOutput={false}
  primaryLanguage="python"
  code={[{ lang: 'python', value: exampleCode }]}
/>
```

## 扩展新语言

要添加新的编程语言支持，需要：

1. 在 `LanguageRuntimeManager` 中添加新的运行时加载方法
2. 在 `languageConfig` 中添加语言配置
3. 更新类型定义

```typescript
// 添加新语言运行时
private async loadGoRuntime(): Promise<any> {
  return {
    execute: async (code: string) => {
      // 实现 Go 代码执行逻辑
      const response = await fetch('https://go-playground-api.com/execute', {
        method: 'POST',
        body: JSON.stringify({ code })
      })
      return await response.json()
    }
  }
}

// 添加语言配置
const languageConfig = {
  // ... 现有配置
  go: {
    name: 'Go',
    extension: 'go',
    monacoLanguage: 'go',
    runtime: 'go'
  }
}
```

## 错误处理

组件内置了完善的错误处理机制：

- 运行时加载失败
- 代码执行错误
- 网络请求失败
- 依赖库加载失败

错误信息会显示在输出区域，不会影响编辑器功能。

## 最佳实践

### 1. 性能优化
- 使用 `height` 属性限制编辑器高度
- 避免在 `code` 属性中传递过大的代码块
- 合理使用 `preloadLibraries` 预加载常用库

### 2. 用户体验
- 提供有意义的 `title` 描述编辑器用途
- 在对比模式下确保代码功能相似
- 使用 `readOnly` 模式展示示例代码

### 3. 错误处理
- 监听错误状态并提供用户友好的提示
- 在代码执行失败时提供修复建议
- 记录错误日志用于调试

## 示例

查看 `universal-editor-example.tsx` 文件获取完整的使用示例，包括：

- 单语言编辑器
- 对比模式编辑器
- 只读模式
- 不同语言的代码示例

## 注意事项

1. **网络依赖**: Rust、C++、Java 运行时需要网络连接
2. **浏览器兼容性**: 需要支持 ES6+ 的现代浏览器
3. **内存使用**: 大文件可能影响性能，建议使用虚拟化
4. **安全考虑**: 代码在用户浏览器中执行，注意安全风险

## 故障排除

### 常见问题

1. **Pyodide 加载失败**
   - 检查网络连接
   - 确认 CDN 可用性
   - 查看浏览器控制台错误

2. **Monaco Editor 不显示**
   - 确认 Monaco Editor 正确安装
   - 检查 CSS 样式冲突
   - 验证组件挂载状态

3. **代码执行失败**
   - 检查代码语法
   - 确认依赖库可用
   - 查看错误输出信息

### 调试技巧

- 启用浏览器开发者工具
- 查看网络请求状态
- 检查控制台错误信息
- 使用 React DevTools 调试组件状态 