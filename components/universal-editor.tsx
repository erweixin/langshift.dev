'use client'
import * as React from 'react'
import { useState, useEffect, ReactNode, Suspense } from 'react'
import { useMonacoManager } from './monaco-manager'
import { VirtualizedEditor } from './virtualized-editor'
import { trackCodeExecution, trackEditorUsage } from '@/components/analytics'
import { getTranslations, type SupportedLanguage } from '@/messages'
import { useParams } from 'next/navigation'
import { getPyodideCDN } from '@/lib/cdn-disaster-recovery'

interface CodeBlock {
  language: string
  code: string
}

interface UniversalEditorProps {
  children?: ReactNode
  title?: string
  theme?: 'vs-light' | 'vs-dark' | 'auto'
  readOnly?: boolean
  showOutput?: boolean
  compare?: boolean
  code?: any[]
  height?: number
  preloadLibraries?: string[]
  allowDynamicImports?: boolean
  canRun?: boolean
}

// 语言运行时管理器
class LanguageRuntimeManager {
  private static instance: LanguageRuntimeManager
  private runtimes: Map<string, any> = new Map()
  private loadingStates: Map<string, boolean> = new Map()
  private loadPromises: Map<string, Promise<any>> = new Map()
  private subscribers: Map<string, Set<(runtime: any) => void>> = new Map()

  private constructor() {}

  static getInstance(): LanguageRuntimeManager {
    if (!LanguageRuntimeManager.instance) {
      LanguageRuntimeManager.instance = new LanguageRuntimeManager()
    }
    return LanguageRuntimeManager.instance
  }

  async getRuntime(language: string): Promise<any> {
    // 如果已经加载完成，直接返回
    if (this.runtimes.has(language)) {
      return this.runtimes.get(language)
    }

    // 如果正在加载，等待加载完成
    if (this.loadPromises.has(language)) {
      return this.loadPromises.get(language)
    }

    // 开始加载
    this.loadingStates.set(language, true)
    const loadPromise = this.loadRuntime(language)
    this.loadPromises.set(language, loadPromise)
    
    try {
      const runtime = await loadPromise
      this.runtimes.set(language, runtime)
      // 通知所有订阅者
      const subscribers = this.subscribers.get(language) || new Set()
      subscribers.forEach(callback => callback(runtime))
      return runtime
    } catch (error) {
      this.loadPromises.delete(language)
      this.loadingStates.set(language, false)
      throw error
    } finally {
      this.loadingStates.set(language, false)
    }
  }

  private async loadRuntime(language: string): Promise<any> {
    switch (language) {
      case 'python':
        return this.loadPyodide()
      case 'javascript':
      case 'typescript':
        return this.loadJavaScriptRuntime()
      case 'rust':
        return this.loadRustRuntime()
      case 'cpp':
        return this.loadCppRuntime()
      case 'c':
        return this.loadCRuntime()
      case 'java':
        return this.loadJavaRuntime()
      case 'go':
        return this.loadGoRuntime()
      case 'swift':
        return this.loadSwiftRuntime()
      case 'kotlin':
        return this.loadKotlinRuntime()
      default:
        throw new Error(`不支持的语言: ${language}`)
    }
  }

  private async loadPyodide(): Promise<any> {
    // 检查 Pyodide 是否已加载
    if (!(globalThis as any).loadPyodide) {
      throw new Error('Pyodide 未加载。请确保在 js2py 模块页面访问此功能。')
    }

    try {
      // 获取健康的 Pyodide CDN
      const healthyCDN = await getPyodideCDN()
      
      console.log(`Pyodide 使用 CDN: ${healthyCDN}`)
      
      const pyodideInstance = await (globalThis as any).loadPyodide({
        indexURL: healthyCDN,
      })
      
      // 只预加载基础库，其他库按需加载
      await this.preloadBasicPythonLibraries(pyodideInstance)
      
      return pyodideInstance
    } catch (error) {
      console.error('Pyodide CDN 容灾加载失败，尝试默认 CDN:', error)
      
      try {
        // 备用方案：使用默认 CDN
        const pyodideInstance = await (globalThis as any).loadPyodide({
          indexURL: "https://cdn.jsdelivr.net/pyodide/v0.27.0/full/",
        })
        
        await this.preloadBasicPythonLibraries(pyodideInstance)
        return pyodideInstance
      } catch (fallbackError) {
        console.error('Pyodide 初始化完全失败:', fallbackError)
        throw fallbackError
      }
    }
  }

  private async loadJavaScriptRuntime(): Promise<any> {
    // JavaScript 运行时就是浏览器环境
    return {
      execute: (code: string) => {
        const logs: string[] = []
        const originalLog = console.log
        console.log = (...args) => {
          logs.push(args.join(' '))
          originalLog(...args)
        }
        
        try {
          eval(code)
          return { output: logs.join('\n'), error: null }
        } catch (error: any) {
          return { output: '', error: error.message }
        } finally {
          console.log = originalLog
        }
      }
    }
  }

  private async loadRustRuntime(): Promise<any> {
    // 使用 Rust Playground API 或 WebAssembly
    return {
      execute: async (code: string) => {
        try {
          // 检查是否包含 main 函数，如果没有则自动添加
          let processedCode = code
          if (!code.includes('fn main()') && !code.includes('fn main(')) {
            // 移除可能存在的 fn main() { } 空函数
            processedCode = code.replace(/fn\s+main\s*\(\s*\)\s*\{\s*\}\s*/g, '')
            
            // 添加 main 函数包装
            processedCode = `fn main() {
    ${processedCode.trim()}
}`
          }
          
          console.log(JSON.stringify({
            channel: 'stable',
            mode: 'debug',
            edition: '2021',
            crateType: 'bin',
            tests: false,
            code: processedCode,
            backtrace: false,
          }))
          const response = await fetch('https://play.rust-lang.org/execute', {
            method: 'POST',
            headers: {
              'Content-Type': 'application/json',
            },
            body: JSON.stringify({
              channel: 'stable',
              mode: 'debug',
              edition: '2021',
              crateType: 'bin',
              tests: false,
              code: processedCode,
              backtrace: false,
            }),
          })
          
          const result = await response.json()
          console.log(result)
          if (result.success) {
            return {
              output: result.stdout || '',
              error: undefined
            }
          }
          return {
            output: result.stdout || '',
            error: result.stderr || null
          }
        } catch (error: any) {
          return { output: '', error: error.message }
        }
      }
    }
  }

  private async loadCppRuntime(): Promise<any> {
    // 使用在线 C++ 编译器 API
    return {
      execute: async (code: string) => {
        try {
          // 这里可以集成在线 C++ 编译器 API
          // 例如 Wandbox API 或其他在线编译器
          const response = await fetch('https://wandbox.org/api/compile.json', {
            method: 'POST',
            headers: {
              'Content-Type': 'application/json',
            },
            body: JSON.stringify({
              code: code,
              compiler: 'gcc-head',
              options: 'warning,gnu++17',
            }),
          })
          
          const result = await response.json()
          return {
            output: result.program_output || '',
            error: result.program_message || null
          }
        } catch (error: any) {
          return { output: '', error: error.message }
        }
      }
    }
  }

  private async loadCRuntime(): Promise<any> {
    // 使用在线 C 编译器 API
    return {
      execute: async (code: string) => {
        try {
          // 使用 Wandbox API 编译 C 代码
          const response = await fetch('https://wandbox.org/api/compile.json', {
            method: 'POST',
            headers: {
              'Content-Type': 'application/json',
            },
            body: JSON.stringify({
              code: code,
              compiler: 'gcc-head',
              options: 'warning,gnu11',
            }),
          })
          
          const result = await response.json()
          
          // 处理编译结果
          if (result.status === '0') {
            return {
              output: result.program_output || '',
              error: null
            }
          } else {
            return {
              output: result.program_output || '',
              error: result.program_message || '编译失败'
            }
          }
        } catch (error: any) {
          console.error('C 代码执行错误:', error)
          return { 
            output: '', 
            error: `C 代码执行失败: ${error.message}` 
          }
        }
      }
    }
  }

  private async loadJavaRuntime(): Promise<any> {
    // 使用在线 Java 编译器 API
    return {
      execute: async (code: string) => {
        try {
          // 这里可以集成在线 Java 编译器 API
          // 例如 JDoodle API 或其他在线编译器
          const response = await fetch('https://api.jdoodle.com/v1/execute', {
            method: 'POST',
            headers: {
              'Content-Type': 'application/json',
            },
            body: JSON.stringify({
              script: code,
              language: 'java',
              versionIndex: '3',
            }),
          })
          
          const result = await response.json()
          return {
            output: result.output || '',
            error: result.error || null
          }
        } catch (error: any) {
          return { output: '', error: error.message }
        }
      }
    }
  }

  private async loadGoRuntime(): Promise<any> {
    // 使用在线 Go 编译器 API
    return {
      execute: async (code: string) => {
        try {
          // 这里可以集成在线 Go 编译器 API
          // 例如 Go Playground API 或其他在线编译器
          const response = await fetch('https://play.golang.org/compile', {
            method: 'POST',
            headers: {
              'Content-Type': 'application/x-www-form-urlencoded',
            },
            body: `body=${encodeURIComponent(code)}`,
          })
          
          const result = await response.json()
          if (result.Errors) {
            return {
              output: '',
              error: result.Errors
            }
          }
          return {
            output: result.Events?.[0]?.Message || '',
            error: null
          }
        } catch (error: any) {
          return { output: '', error: error.message }
        }
      }
    }
  }

  private async loadSwiftRuntime(): Promise<any> {
    // 使用本地 API 代理 Paiza.io 在线 Swift 编译器
    return {
      execute: async (code: string) => {
        try {
          const response = await fetch('/api/swift', {
            method: 'POST',
            headers: {
              'Content-Type': 'application/json',
            },
            body: JSON.stringify({ code })
          })
          
          if (!response.ok) {
            throw new Error(`API 请求失败: ${response.status}`)
          }
          
          const result = await response.json()
          return {
            output: result.output || '',
            error: result.error || null
          }
          
        } catch (error: any) {
          console.error('Swift 执行错误:', error)
          return { 
            output: '', 
            error: `Swift 执行失败: ${error.message}` 
          }
        }
      }
    }
  }

  private async loadKotlinRuntime(): Promise<any> {
    // 使用 Kotlin Playground API
    return {
      execute: async (code: string) => {
        try {
          // 检查是否包含 main 函数，如果没有则自动添加
          let processedCode = code
          if (!code.includes('fun main()') && !code.includes('fun main(')) {
            // 移除可能存在的 fun main() { } 空函数
            processedCode = code.replace(/fun\s+main\s*\(\s*\)\s*\{\s*\}\s*/g, '')
            
            // 添加 main 函数包装
            processedCode = `fun main() {
    ${processedCode.trim()}
}`
          }
          
          // 使用 Kotlin Playground API 执行代码
          // 参考: https://jetbrains.github.io/kotlin-playground/examples/
          const response = await fetch('https://api.kotlinlang.org/api/1.9.10/compiler/run', {
            method: 'POST',
            headers: {
              'Content-Type': 'application/json',
            },
            body: JSON.stringify({
              // code: code,
              args: '',
              files: [{
                name: "File.kt",
                publicId: "",
                text: processedCode,
              }],
              confType: 'java',
              channel: 'stable'
            }),
          })
          
          if (!response.ok) {
            throw new Error(`Kotlin API 请求失败: ${response.status}`)
          }
          
          const result = await response.json()
          
          // 处理执行结果
          if (result.exception) {
            return {
              output: result.text || '',
              error: result.exception
            }
          } else if (result.errors && Object.values(result.errors).some((errors: any) => errors.length > 0)) {
            // 处理编译错误
            const errorMessages = Object.entries(result.errors)
              .filter(([_, errors]: [string, any]) => errors.length > 0)
              .map(([file, errors]: [string, any]) => 
                `${file}: ${errors.map((e: any) => e.message).join(', ')}`
              )
              .join('\n')
            
            return {
              output: result.text || '',
              error: errorMessages
            }
          } else {
            // 成功执行
            const output = result.text || ''
            return {
              output: output.replace(/<outStream>(.*?)<\/outStream>/s, '$1').trim(),
              error: null
            }
          }
        } catch (error: any) {
          console.error('Kotlin 代码执行错误:', error)
          return { 
            output: '', 
            error: `Kotlin 代码执行失败: ${error.message}` 
          }
        }
      }
    }
  }

  private async preloadBasicPythonLibraries(pyodide: any) {
    try {
      // 只预加载基础库，这些库很小且常用
      const basicLibraries = [
        'json', 'datetime', 'math', 'random', 'os', 'sys', 're', 
        'collections', 'itertools', 'functools', 'time', 'pathlib'
      ]
      
      for (const lib of basicLibraries) {
        try {
          await pyodide.loadPackage(lib)
          console.log(`预加载基础 Python 库: ${lib}`)
        } catch (err) {
          console.warn(`无法预加载基础 Python 库 ${lib}:`, err)
        }
      }
    } catch (error) {
      console.warn('预加载基础 Python 库时出错:', error)
    }
  }

  private async preloadPythonLibraries(pyodide: any) {
    try {
      const commonLibraries = [
        'numpy', 'pandas', 'matplotlib', 'requests', 'json', 'datetime',
        'math', 'random', 'os', 'sys', 're', 'collections', 'itertools',
        'functools', 'urllib', 'time', 'pathlib'
      ]
      
      for (const lib of commonLibraries) {
        try {
          await pyodide.loadPackage(lib)
          console.log(`预加载 Python 库: ${lib}`)
        } catch (err) {
          console.warn(`无法预加载 Python 库 ${lib}:`, err)
        }
      }
    } catch (error) {
      console.warn('预加载 Python 库时出错:', error)
    }
  }

  subscribe(language: string, callback: (runtime: any) => void): () => void {
    if (!this.subscribers.has(language)) {
      this.subscribers.set(language, new Set())
    }
    
    const subscribers = this.subscribers.get(language)!
    subscribers.add(callback)
    
    // 如果已经加载完成，立即调用回调
    if (this.runtimes.has(language)) {
      callback(this.runtimes.get(language))
    }

    // 返回取消订阅函数
    return () => {
      subscribers.delete(callback)
    }
  }

  isRuntimeLoading(language: string): boolean {
    return this.loadingStates.get(language) || false
  }

  isRuntimeReady(language: string): boolean {
    return this.runtimes.has(language)
  }

  getSupportedLanguages(): string[] {
    return ['python', 'javascript', 'typescript', 'rust', 'cpp', 'c', 'java', 'go', 'swift', 'kotlin']
  }

  // 检查是否需要加载运行时
  shouldLoadRuntime(language: string): boolean {
    // 只有 Python 需要特殊处理，其他语言运行时很轻量
    return language === 'python'
  }
}

// 全局运行时管理器实例
const runtimeManager = LanguageRuntimeManager.getInstance()

// Monaco Editor 包装组件
function MonacoEditorWrapper({
  language,
  value,
  onChange,
  theme,
  height,
  options,
  ...props
}: {
  language: string
  value: string
  onChange?: (value: string | undefined) => void
  theme: string
  height: number
  options: any
  [key: string]: any
}) {
  const { isReady, isLoading, error } = useMonacoManager()
  
  // 从路由参数获取语言
  const params = useParams();
  const lang = (params?.lang as SupportedLanguage) || 'zh-cn';
  const t = getTranslations(lang)

  if (error) {
    return (
      <div className="border rounded p-4 bg-red-50 dark:bg-red-900/20">
        <div className="text-red-600 dark:text-red-400 text-sm">
          {error}
        </div>
      </div>
    )
  }

  if (isLoading || !isReady) {
    return (
      <div className="border rounded p-4 bg-gray-50 dark:bg-gray-800">
        <div className="flex items-center justify-center h-32">
          <div className="text-gray-600 dark:text-gray-400 text-sm">
            {t.editor.loading}
          </div>
        </div>
      </div>
    )
  }

  return (
          <Suspense fallback={
        <div className="border rounded p-4 bg-gray-50 dark:bg-gray-800">
          <div className="flex items-center justify-center h-32">
            <div className="text-gray-600 dark:text-gray-400 text-sm">
              {t.editor.loading}
            </div>
          </div>
        </div>
      }>
      <VirtualizedEditor
        height={height}
        language={language}
        theme={theme}
        value={value}
        onChange={onChange}
        options={{
          minimap: { enabled: false },
          fontSize: 14,
          lineNumbers: 'on',
          roundedSelection: false,
          scrollBeyondLastLine: false,
          automaticLayout: true,
          ...options
        }}
        {...props}
      />
    </Suspense>
  )
}

// 语言配置
const languageConfig = {
  python: {
    name: 'Python',
    extension: 'py',
    monacoLanguage: 'python',
    runtime: 'python'
  },
  javascript: {
    name: 'JavaScript',
    extension: 'js',
    monacoLanguage: 'javascript',
    runtime: 'javascript'
  },
  typescript: {
    name: 'TypeScript',
    extension: 'ts',
    monacoLanguage: 'typescript',
    runtime: 'typescript'
  },
  rust: {
    name: 'Rust',
    extension: 'rs',
    monacoLanguage: 'rust',
    runtime: 'rust'
  },
  cpp: {
    name: 'C++',
    extension: 'cpp',
    monacoLanguage: 'cpp',
    runtime: 'cpp'
  },
  java: {
    name: 'Java',
    extension: 'java',
    monacoLanguage: 'java',
    runtime: 'java'
  },
  go: {
    name: 'Go',
    extension: 'go',
    monacoLanguage: 'go',
    runtime: 'go'
  },
  swift: {
    name: 'Swift',
    extension: 'swift',
    monacoLanguage: 'swift',
    runtime: 'swift'
  },
  c: {
    name: 'C',
    extension: 'c',
    monacoLanguage: 'c',
    runtime: 'c',
  },
  kotlin: {
    name: 'Kotlin',
    extension: 'kt',
    monacoLanguage: 'kotlin',
    runtime: 'kotlin'
  }
}

export default function UniversalEditor(params: UniversalEditorProps) {
  const {
    title,
    theme = 'auto',
    readOnly = false,
    showOutput = true,
    compare = false,
    code = [],
    height = 300,
    preloadLibraries = [],
    allowDynamicImports = true,
    canRun = true
  } = params;

  // 从路由参数获取语言
  const params_ = useParams();
  const lang = (params_?.lang as SupportedLanguage) || 'zh-cn';
  
  // 获取翻译
  const t = getTranslations(lang);
  const defaultTitle = t.editor.title;

  const [runtimes, setRuntimes] = useState<Map<string, any>>(new Map())
  const [loadingStates, setLoadingStates] = useState<Map<string, boolean>>(new Map())
  const [output, setOutput] = useState<string>('')
  const [error, setError] = useState<string>('')
  const [codeBlocks, setCodeBlocks] = useState<Map<string, string>>(new Map())
  const [primaryLanguage, setPrimaryLanguage] = useState<keyof typeof languageConfig>('python')
  const [secondaryLanguage, setSecondaryLanguage] = useState<keyof typeof languageConfig | null>(null)
  const [isRunning, setIsRunning] = useState(false)
  const [isClient, setIsClient] = useState(false)
  const [currentTheme, setCurrentTheme] = useState<'vs-light' | 'vs-dark'>('vs-light')
  
  // 检查是否在客户端
  useEffect(() => {
    setIsClient(true)
  }, [])
  
  // 检测暗色模式
  useEffect(() => {
    if (!isClient) return
    
    const checkTheme = () => {
      if (theme === 'auto') {
        const isDark = document.documentElement.classList.contains('dark')
        setCurrentTheme(isDark ? 'vs-dark' : 'vs-light')
      } else {
        setCurrentTheme(theme)
      }
    }
    
    checkTheme()
    
    const observer = new MutationObserver((mutations) => {
      mutations.forEach((mutation) => {
        if (mutation.type === 'attributes' && mutation.attributeName === 'class') {
          checkTheme()
        }
      })
    })
    
    observer.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ['class']
    })
    
    return () => {
      observer.disconnect()
    }
  }, [isClient, theme])
  
  // 按需初始化运行时 - 只在需要时加载
  useEffect(() => {
    if (!isClient) return

    const languages = [primaryLanguage]
    if (secondaryLanguage) {
      languages.push(secondaryLanguage)
    }

    const unsubscribers: (() => void)[] = []

    languages.forEach(language => {
      const unsubscribe = runtimeManager.subscribe(language, (runtime) => {
        setRuntimes(prev => new Map(prev.set(language, runtime)))
        setLoadingStates(prev => new Map(prev.set(language, false)))
      })

      unsubscribers.push(unsubscribe)
    })

    return () => {
      unsubscribers.forEach(unsubscribe => unsubscribe())
    }
  }, [isClient, primaryLanguage, secondaryLanguage])
  
  // 解析代码块并设置语言
  useEffect(() => {
    if (code.length === 0) {
      console.warn('code is empty')
      return;
    }
    const newCodeBlocks = new Map<string, string>()
    const languages: string[] = []
    
    code.forEach(block => {
      const lang = block.lang?.toLowerCase()
      if (lang && languageConfig[lang as keyof typeof languageConfig]) {
        newCodeBlocks.set(lang, block.value)
        languages.push(lang)
      }
    })
    
    // 设置主语言和次语言
    if (languages.length > 0) {
      const firstLang = languages[0] as keyof typeof languageConfig
      setPrimaryLanguage(firstLang)
      if (languages.length > 1) {
        const secondLang = languages[1] as keyof typeof languageConfig
        setSecondaryLanguage(secondLang)
      } else {
        setSecondaryLanguage(null)
      }
    }
    
    setCodeBlocks(newCodeBlocks)
  }, [code])
  
  // 运行代码 - 按需加载运行时
  const runCode = async (language: string) => {
    const code = codeBlocks.get(language)
    
    if (!code?.trim()) return
    
    setIsRunning(true)
    setOutput('')
    setError('')
    
    // 追踪编辑器使用
    trackEditorUsage('universal-editor', 'code-execution')
    
    try {
      // 按需加载运行时
      let runtime = runtimes.get(language)
      if (!runtime) {
        setLoadingStates(prev => new Map(prev.set(language, true)))
        try {
          runtime = await runtimeManager.getRuntime(language)
          setRuntimes(prev => new Map(prev.set(language, runtime)))
        } catch (err: any) {
          console.error(`${language} 运行时初始化失败:`, err)
          setError(t.editor.executionFailed.replace('{language}', t.editor.languages[language as keyof typeof t.editor.languages] || language))
          setLoadingStates(prev => new Map(prev.set(language, false)))
          setIsRunning(false)
          // 追踪执行失败
          trackCodeExecution(language, false)
          return
        } finally {
          setLoadingStates(prev => new Map(prev.set(language, false)))
        }
      }
      
      let result: { output: string; error: string | null }
      if (language === 'python') {
        result = await runPythonCode(runtime, code)
      } else {
        result = await runtime.execute(code)
      }
      if (result.error) {
        setError(result.error)
        // 追踪执行失败
        trackCodeExecution(language, false)
      } else {
        setOutput(result.output)
        // 追踪执行成功
        trackCodeExecution(language, true)
      }
    } catch (err: any) {
      setError(err.message || t.editor.executionError)
      // 追踪执行失败
      trackCodeExecution(language, false)
    } finally {
      setIsRunning(false)
    }
  }

  // 运行 Python 代码的特殊处理
  const runPythonCode = async (pyodide: any, code: string) => {
    // 设置输出捕获
    pyodide.runPython(`
import sys
import io
from contextlib import redirect_stdout

class StringIO:
    def __init__(self):
        self.buffer = []
    
    def write(self, text):
        self.buffer.append(text)
    
    def getvalue(self):
        return ''.join(self.buffer)

# 创建输出捕获器
output_capture = StringIO()
sys.stdout = output_capture
    `)
    
    // 动态加载所需的库
    if (allowDynamicImports) {
      await loadRequiredPythonLibraries(pyodide, code)
    }
    
    // 执行用户代码
    pyodide.runPython(code)
    
    // 获取输出
    const output = pyodide.runPython(`
sys.stdout.getvalue()
    `)
    
    // 恢复标准输出
    pyodide.runPython(`
sys.stdout = sys.__stdout__
    `)
    
    return { output: output || '', error: null }
  }

  // 动态加载 Python 库 - 优化版本
  const loadRequiredPythonLibraries = async (pyodide: any, code: string) => {
    const importRegex = /^(?:from\s+(\w+)(?:\.\w+)*\s+import|import\s+(\w+)(?:\.\w+)*)/gm
    const matches = [...code.matchAll(importRegex)]
    
    const libraries = new Set<string>()
    
    for (const match of matches) {
      const lib = match[1] || match[2]
      if (lib) {
        libraries.add(lib)
      }
    }
    
    const libraryMap: { [key: string]: string } = {
      'numpy': 'numpy', 'np': 'numpy',
      'pandas': 'pandas', 'pd': 'pandas',
      'matplotlib': 'matplotlib', 'plt': 'matplotlib',
      'requests': 'requests', 'json': 'json',
      'datetime': 'datetime', 'math': 'math',
      'random': 'random', 'os': 'os', 'sys': 'sys',
      're': 're', 'collections': 'collections',
      'itertools': 'itertools', 'functools': 'functools',
      'urllib': 'urllib', 'time': 'time',
      'pathlib': 'pathlib', 'seaborn': 'seaborn',
      'scipy': 'scipy', 'sklearn': 'scikit-learn',
      'tensorflow': 'tensorflow', 'torch': 'torch',
      'cv2': 'opencv-python', 'PIL': 'pillow',
      'Image': 'pillow'
    }
    
    // 基础库列表（已预加载，无需重复加载）
    const basicLibraries = new Set([
      'json', 'math', 'random', 'os', 'sys', 're', 'collections', 
      'itertools', 'functools', 'time', 'pathlib', 'datetime'
    ])
    
    // 加载自定义预加载库
    for (const lib of preloadLibraries) {
      try {
        await pyodide.loadPackage(lib)
        console.log(`预加载自定义库: ${lib}`)
      } catch (err) {
        console.warn(`无法预加载自定义库 ${lib}:`, err)
      }
    }
    
    // 只加载非基础库
    for (const lib of libraries) {
      const packageName = libraryMap[lib]
      if (packageName && !basicLibraries.has(packageName)) {
        try {
          await pyodide.loadPackage(packageName)
          console.log(`动态加载库: ${packageName}`)
        } catch (err) {
          console.warn(`无法加载库 ${packageName}:`, err)
        }
      }
    }
  }
  
  if (!isClient) {
    return (
      <div className="border rounded-lg p-4 bg-gray-50 dark:bg-gray-800">
        <div className="flex items-center justify-center h-32">
          <div className="text-gray-600 dark:text-gray-400">
            {t.editor.loading}
          </div>
        </div>
      </div>
    )
  }
  
  const primaryConfig = languageConfig[primaryLanguage]
  const secondaryConfig = secondaryLanguage ? languageConfig[secondaryLanguage] : null
  const primaryCode = codeBlocks.get(primaryLanguage) || ''
  const secondaryCode = secondaryLanguage ? (codeBlocks.get(secondaryLanguage) || '') : ''

  return (
    <div className="border rounded-lg overflow-hidden bg-white dark:bg-gray-900">
      {/* 标题栏 */}
      <div className="flex items-center justify-between px-4 py-2 bg-gray-100 dark:bg-gray-800 border-b">
        <div className="text-sm font-medium text-gray-700 dark:text-gray-300 m-2">
          {title || defaultTitle}
        </div>
        { canRun && <div className="flex items-center space-x-2">
          <button
            onClick={() => runCode(primaryLanguage)}
            disabled={isRunning || !primaryCode.trim() || loadingStates.get(primaryLanguage)}
            className="px-3 py-1 text-xs bg-green-500 text-white rounded hover:bg-green-600 disabled:opacity-50"
          >
            {loadingStates.get(primaryLanguage) 
              ? t.editor.loadingRuntime.replace('{language}', t.editor.languages[primaryLanguage as keyof typeof t.editor.languages] || primaryConfig.name)
              : isRunning 
                ? t.editor.running
                : t.editor.run.replace('{language}', t.editor.languages[primaryLanguage as keyof typeof t.editor.languages] || primaryConfig.name)}
          </button>
          {compare && secondaryLanguage && secondaryCode && (
            <button
              onClick={() => runCode(secondaryLanguage)}
              disabled={isRunning || loadingStates.get(secondaryLanguage)}
                          className="px-3 py-1 text-xs bg-blue-500 text-white rounded hover:bg-blue-600 disabled:opacity-50"
          >
            {loadingStates.get(secondaryLanguage) 
              ? t.editor.loadingRuntime.replace('{language}', t.editor.languages[secondaryLanguage as keyof typeof t.editor.languages] || secondaryConfig?.name || '')
              : isRunning 
                ? t.editor.running
                : t.editor.run.replace('{language}', t.editor.languages[secondaryLanguage as keyof typeof t.editor.languages] || secondaryConfig?.name || '')}
          </button>
          )}
        </div> }
      </div>
      
      {/* 编辑器区域 */}
      <div className="flex flex-col lg:flex-row">
        {/* 主语言编辑器 */}
        <div className="flex-1">
          <MonacoEditorWrapper
            language={primaryConfig.monacoLanguage}
            theme={currentTheme}
            value={primaryCode}
            onChange={(value) => setCodeBlocks(prev => new Map(prev.set(primaryLanguage, value || '')))}
            height={height}
            options={{ readOnly }}
          />
        </div>
        
        {/* 对比语言编辑器 */}
        {compare && secondaryLanguage && secondaryConfig && (
          <div className="flex-1 lg:border-l border-t lg:border-t-0">
            <MonacoEditorWrapper
              language={secondaryConfig.monacoLanguage}
              theme={currentTheme}
              value={secondaryCode}
              onChange={(value) => setCodeBlocks(prev => new Map(prev.set(secondaryLanguage, value || '')))}
              height={height}
              options={{ readOnly }}
            />
          </div>
        )}
      </div>
      
      {/* 输出区域 */}
      {showOutput && (output || error) && (
        <div className="border-t bg-gray-50 dark:bg-gray-800">
          <div className="px-4 py-2 text-sm font-medium text-gray-700 dark:text-gray-300 border-b">
            {t.editor.output}
          </div>
          <div className="p-4">
            {error ? (
              <pre className="text-red-600 dark:text-red-400 text-sm whitespace-pre-wrap">
                {error}
              </pre>
            ) : (
              <pre className="text-gray-800 dark:text-gray-200 text-sm whitespace-pre-wrap">
                {output}
              </pre>
            )}
          </div>
        </div>
      )}
    </div>
  )
} 