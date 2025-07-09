// 通用编辑器组件的类型定义

export type SupportedLanguage = 'python' | 'javascript' | 'typescript' | 'rust' | 'cpp' | 'java'

export interface CodeBlock {
  lang: string
  value: string
}

export interface UniversalEditorProps {
  children?: React.ReactNode
  title?: string
  theme?: 'vs-light' | 'vs-dark' | 'auto'
  readOnly?: boolean
  showOutput?: boolean
  compare?: boolean
  code?: CodeBlock[]
  height?: number
  preloadLibraries?: string[]
  allowDynamicImports?: boolean
  primaryLanguage?: SupportedLanguage
  secondaryLanguage?: SupportedLanguage
}

export interface LanguageConfig {
  name: string
  extension: string
  monacoLanguage: string
  runtime: string
}

export interface RuntimeResult {
  output: string
  error: string | null
}

export interface LanguageRuntime {
  execute: (code: string) => Promise<RuntimeResult> | RuntimeResult
}

export interface RuntimeManager {
  getRuntime(language: string): Promise<LanguageRuntime>
  subscribe(language: string, callback: (runtime: LanguageRuntime) => void): () => void
  isRuntimeLoading(language: string): boolean
  isRuntimeReady(language: string): boolean
  getSupportedLanguages(): string[]
}

// 运行时配置接口
export interface RuntimeConfig {
  python?: {
    indexURL?: string
    preloadLibraries?: string[]
  }
  rust?: {
    apiUrl?: string
    channel?: 'stable' | 'beta' | 'nightly'
    edition?: '2015' | '2018' | '2021'
  }
  cpp?: {
    apiUrl?: string
    compiler?: string
    options?: string
  }
  java?: {
    apiUrl?: string
    version?: string
  }
}

// 编辑器状态
export interface EditorState {
  runtimes: Map<string, LanguageRuntime>
  loadingStates: Map<string, boolean>
  codeBlocks: Map<string, string>
  output: string
  error: string
  isRunning: boolean
  currentTheme: 'vs-light' | 'vs-dark'
}

// 事件处理器
export interface EditorEventHandlers {
  onCodeChange?: (language: string, code: string) => void
  onRun?: (language: string, result: RuntimeResult) => void
  onError?: (language: string, error: string) => void
  onRuntimeLoad?: (language: string, runtime: LanguageRuntime) => void
}

// 扩展的编辑器属性
export interface ExtendedUniversalEditorProps extends UniversalEditorProps, EditorEventHandlers {
  runtimeConfig?: RuntimeConfig
  onStateChange?: (state: EditorState) => void
}

// 语言特定的配置
export interface LanguageSpecificConfig {
  python: {
    pyodideVersion: string
    defaultLibraries: string[]
    matplotlibBackend: 'inline' | 'notebook'
  }
  javascript: {
    allowEval: boolean
    sandboxMode: boolean
    maxExecutionTime: number
  }
  typescript: {
    compilerOptions: {
      target: string
      module: string
      strict: boolean
    }
  }
  rust: {
    defaultCrates: string[]
    allowNetwork: boolean
  }
  cpp: {
    defaultHeaders: string[]
    compilerFlags: string[]
  }
  java: {
    defaultImports: string[]
    classpath: string[]
  }
}

// 性能监控接口
export interface PerformanceMetrics {
  loadTime: number
  executionTime: number
  memoryUsage: number
  errorCount: number
}

export interface PerformanceMonitor {
  startTimer(name: string): void
  endTimer(name: string): number
  recordMetric(name: string, value: number): void
  getMetrics(): PerformanceMetrics
}

// 代码分析接口
export interface CodeAnalysis {
  language: SupportedLanguage
  lines: number
  characters: number
  complexity: number
  imports: string[]
  functions: string[]
  classes: string[]
}

export interface CodeAnalyzer {
  analyze(code: string, language: SupportedLanguage): CodeAnalysis
}

// 安全配置
export interface SecurityConfig {
  allowFileSystem: boolean
  allowNetwork: boolean
  allowEval: boolean
  maxExecutionTime: number
  maxMemoryUsage: number
  allowedDomains: string[]
  blockedKeywords: string[]
}

// 主题配置
export interface ThemeConfig {
  light: {
    background: string
    foreground: string
    selection: string
    comment: string
    string: string
    keyword: string
    function: string
    variable: string
  }
  dark: {
    background: string
    foreground: string
    selection: string
    comment: string
    string: string
    keyword: string
    function: string
    variable: string
  }
}

// 国际化接口
export interface I18nConfig {
  locale: string
  messages: {
    [key: string]: string
  }
}

// 完整的编辑器配置
export interface CompleteEditorConfig {
  props: ExtendedUniversalEditorProps
  runtimeConfig: RuntimeConfig
  languageConfig: Partial<LanguageSpecificConfig>
  securityConfig: SecurityConfig
  themeConfig: ThemeConfig
  i18nConfig: I18nConfig
  performanceMonitor: PerformanceMonitor
  codeAnalyzer: CodeAnalyzer
}

// 工具类型
export type LanguageMap<T> = {
  [K in SupportedLanguage]: T
}

export type OptionalLanguageMap<T> = {
  [K in SupportedLanguage]?: T
}

// 运行时工厂接口
export interface RuntimeFactory {
  createRuntime(language: SupportedLanguage, config?: any): Promise<LanguageRuntime>
  supportsLanguage(language: string): language is SupportedLanguage
  getRuntimeInfo(language: SupportedLanguage): {
    name: string
    version: string
    features: string[]
  }
}

// 插件系统接口
export interface EditorPlugin {
  name: string
  version: string
  init?(editor: any): void
  destroy?(): void
  onCodeChange?(language: string, code: string): void
  onRun?(language: string, result: RuntimeResult): void
}

export interface PluginManager {
  register(plugin: EditorPlugin): void
  unregister(pluginName: string): void
  getPlugin(name: string): EditorPlugin | undefined
  getAllPlugins(): EditorPlugin[]
}

// 导出别名类型
export type Language = SupportedLanguage 