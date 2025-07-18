'use client'
import * as React from 'react'
import { useState, useEffect, ReactNode, lazy, Suspense } from 'react'
import { useMonacoManager } from './monaco-manager'
import { VirtualizedEditor } from './virtualized-editor'

interface CodeBlock {
  language: string
  code: string
}

interface PythonEditorProps {
  children: ReactNode
  title?: string
  theme?: 'vs-light' | 'vs-dark' | 'auto'
  readOnly?: boolean
  showOutput?: boolean
  compare?: boolean
  code?: any[]
  height?: number
  preloadLibraries?: string[]
  allowDynamicImports?: boolean
}

// 全局 Pyodide 管理器
class PyodideManager {
  private static instance: PyodideManager
  private pyodide: any = null
  private isLoading = false
  private loadPromise: Promise<any> | null = null
  private subscribers: Set<(pyodide: any) => void> = new Set()

  private constructor() {}

  static getInstance(): PyodideManager {
    if (!PyodideManager.instance) {
      PyodideManager.instance = new PyodideManager()
    }
    return PyodideManager.instance
  }

  async getPyodide(): Promise<any> {
    // 如果已经加载完成，直接返回
    if (this.pyodide) {
      return this.pyodide
    }

    // 如果正在加载，等待加载完成
    if (this.loadPromise) {
      return this.loadPromise
    }

    // 开始加载
    this.isLoading = true
    this.loadPromise = this.loadPyodide()
    
    try {
      this.pyodide = await this.loadPromise
      // 通知所有订阅者
      this.subscribers.forEach(callback => callback(this.pyodide))
      return this.pyodide
    } catch (error) {
      this.loadPromise = null
      this.isLoading = false
      throw error
    } finally {
      this.isLoading = false
    }
  }

  private async loadPyodide(): Promise<any> {
    try {
      const pyodideInstance = await (globalThis as any).loadPyodide({
        indexURL: "https://cdn.jsdelivr.net/pyodide/v0.27.0/full/",
      })
      
      // 预加载常用的 Python 库
      await this.preloadCommonLibraries(pyodideInstance)
      
      return pyodideInstance
    } catch (error) {
      console.error('Pyodide 初始化失败:', error)
      throw error
    }
  }

  private async preloadCommonLibraries(pyodide: any) {
    try {
      // 预加载常用库
      const commonLibraries = [
        'numpy',
        'pandas', 
        'matplotlib',
        'requests',
        'json',
        'datetime',
        'math',
        'random',
        'os',
        'sys',
        're',
        'collections',
        'itertools',
        'functools'
      ]
      
      for (const lib of commonLibraries) {
        try {
          await pyodide.loadPackage(lib)
          console.log(`预加载库: ${lib}`)
        } catch (err) {
          // 某些库可能不可用，忽略错误
          console.warn(`无法预加载库 ${lib}:`, err)
        }
      }
    } catch (error) {
      console.warn('预加载库时出错:', error)
    }
  }

  subscribe(callback: (pyodide: any) => void): () => void {
    this.subscribers.add(callback)
    
    // 如果已经加载完成，立即调用回调
    if (this.pyodide) {
      callback(this.pyodide)
    }

    // 返回取消订阅函数
    return () => {
      this.subscribers.delete(callback)
    }
  }

  isPyodideLoading(): boolean {
    return this.isLoading
  }

  isPyodideReady(): boolean {
    return this.pyodide !== null
  }
}

// 全局管理器实例
const pyodideManager = PyodideManager.getInstance()
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
            正在加载编辑器...
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
            加载中...
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

export default function PythonEditor(params: PythonEditorProps) {
  const {
    title = 'Python 代码编辑器',
    theme = 'auto',
    readOnly = false,
    showOutput = true,
    compare = false,
    code = [],
    height = 300,
    preloadLibraries = [],
    allowDynamicImports = true
  } = params;
  const [pyodide, setPyodide] = useState<any>(null)
  const [isLoading, setIsLoading] = useState(true)
  const [output, setOutput] = useState<string>('')
  const [error, setError] = useState<string>('')
  const [pythonCode, setPythonCode] = useState<string>('')
  const [javascriptCode, setJavascriptCode] = useState<string>('')
  const [typescriptCode, setTypescriptCode] = useState<string>('')
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
        // 检查 HTML 元素是否有 dark 类
        const isDark = document.documentElement.classList.contains('dark')
        setCurrentTheme(isDark ? 'vs-dark' : 'vs-light')
      } else {
        setCurrentTheme(theme)
      }
    }
    
    // 初始检查
    checkTheme()
    
    // 监听主题变化
    const observer = new MutationObserver((mutations) => {
      mutations.forEach((mutation) => {
        if (mutation.type === 'attributes' && mutation.attributeName === 'class') {
          checkTheme()
        }
      })
    })
    
    // 观察 HTML 元素的 class 属性变化
    observer.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ['class']
    })
    
    return () => {
      observer.disconnect()
    }
  }, [isClient, theme])
  
  // 订阅 Pyodide 实例
  useEffect(() => {
    if (!isClient) return

    // 订阅 Pyodide 实例变化
    const unsubscribe = pyodideManager.subscribe((pyodideInstance) => {
      setPyodide(pyodideInstance)
      setIsLoading(false)
    })

    // 如果还没有开始加载，开始加载
    if (!pyodideManager.isPyodideReady() && !pyodideManager.isPyodideLoading()) {
      pyodideManager.getPyodide().catch((err) => {
        console.error('Pyodide 初始化失败:', err)
        setError('Python 环境初始化失败')
        setIsLoading(false)
      })
    }

    return unsubscribe
  }, [isClient])
  
  // 解析代码块
  useEffect(() => {
    const pythonBlock = code.find(block => block.lang === 'python')
    const jsBlock = code.find(block => block.lang === 'javascript' || block.lang === 'js')
    const tsBlock = code.find(block => block.lang === 'typescript' || block.lang === 'ts')
    
    if (pythonBlock) {
      setPythonCode(pythonBlock.value)
    }
    if (jsBlock) {
      setJavascriptCode(jsBlock.value)
    }
    if (tsBlock) {
      setTypescriptCode(tsBlock.value)
    }
  }, [code])
  
  // 运行 Python 代码
  const runPythonCode = async () => {
    if (!pyodide || !pythonCode.trim()) return
    
    setIsRunning(true)
    setOutput('')
    setError('')
    
    try {
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
      await loadRequiredLibraries(pyodide, pythonCode)
      
      // 执行用户代码
      pyodide.runPython(pythonCode)
      
      // 获取输出
      const output = pyodide.runPython(`
sys.stdout.getvalue()
      `)
      
      // 恢复标准输出
      pyodide.runPython(`
sys.stdout = sys.__stdout__
      `)
      
      setOutput(output || '')
      
    } catch (err: any) {
      setError(err.message || '代码执行出错')
    } finally {
      setIsRunning(false)
    }
  }

  // 动态加载所需的库
  const loadRequiredLibraries = async (pyodide: any, code: string) => {
    if (!allowDynamicImports) return
    
    // 解析代码中的 import 语句
    const importRegex = /^(?:from\s+(\w+)(?:\.\w+)*\s+import|import\s+(\w+)(?:\.\w+)*)/gm
    const matches = [...code.matchAll(importRegex)]
    
    const libraries = new Set<string>()
    
    for (const match of matches) {
      const lib = match[1] || match[2]
      if (lib) {
        libraries.add(lib)
      }
    }
    
    // 需要动态加载的库映射
    const libraryMap: { [key: string]: string } = {
      'numpy': 'numpy',
      'np': 'numpy',
      'pandas': 'pandas',
      'pd': 'pandas',
      'matplotlib': 'matplotlib',
      'plt': 'matplotlib',
      'requests': 'requests',
      'json': 'json',
      'datetime': 'datetime',
      'math': 'math',
      'random': 'random',
      'os': 'os',
      'sys': 'sys',
      're': 're',
      'collections': 'collections',
      'itertools': 'itertools',
      'functools': 'functools',
      'urllib': 'urllib',
      'time': 'time',
      'pathlib': 'pathlib',
      'seaborn': 'seaborn',
      'scipy': 'scipy',
      'sklearn': 'scikit-learn',
      'tensorflow': 'tensorflow',
      'torch': 'torch',
      'cv2': 'opencv-python',
      'PIL': 'pillow',
      'Image': 'pillow'
    }
    
    // 加载自定义预加载库
    for (const lib of preloadLibraries) {
      try {
        await pyodide.loadPackage(lib)
        console.log(`预加载自定义库: ${lib}`)
      } catch (err) {
        console.warn(`无法预加载自定义库 ${lib}:`, err)
      }
    }
    
    // 加载所需的库
    for (const lib of libraries) {
      const packageName = libraryMap[lib]
      if (packageName && packageName !== 'json' && packageName !== 'math' && 
          packageName !== 'random' && packageName !== 'os' && packageName !== 'sys' && 
          packageName !== 're' && packageName !== 'collections' && packageName !== 'itertools' && 
          packageName !== 'functools' && packageName !== 'time' && packageName !== 'pathlib') {
        try {
          await pyodide.loadPackage(packageName)
          console.log(`动态加载库: ${packageName}`)
        } catch (err) {
          console.warn(`无法加载库 ${packageName}:`, err)
          // 不抛出错误，让代码继续执行
        }
      }
    }
  }
  
  // 运行 JavaScript 代码
  const runJavascriptCode = () => {
    if (!javascriptCode.trim()) return
    
    try {
      // 创建一个新的 console.log 来捕获输出
      const logs: string[] = []
      const originalLog = console.log
      console.log = (...args) => {
        logs.push(args.join(' '))
        originalLog(...args)
      }
      
      // 执行代码
      eval(javascriptCode)
      
      // 恢复 console.log
      console.log = originalLog
      
      setOutput(logs.join('\n'))
      setError('')
    } catch (err: any) {
      setError(err.message || 'JavaScript 代码执行出错')
    }
  }
  
  if (!isClient) {
    return (
      <div className="border rounded-lg p-4 bg-gray-50 dark:bg-gray-800">
        <div className="flex items-center justify-center h-32">
          <div className="text-gray-600 dark:text-gray-400">
            正在加载...
          </div>
        </div>
      </div>
    )
  }
  
  return (
    <div className="border rounded-lg overflow-hidden bg-white dark:bg-gray-900">
      {/* 标题栏 */}
      <div className="flex items-center justify-between px-4 py-2 bg-gray-100 dark:bg-gray-800 border-b">
        <h3 className="text-sm font-medium text-gray-700 dark:text-gray-300 m-0">
          {title}
        </h3>
        <div className="flex items-center space-x-2">
          <button
            onClick={runPythonCode}
            disabled={isRunning || !pythonCode.trim()}
            className="px-3 py-1 text-xs bg-green-500 text-white rounded hover:bg-green-600 disabled:opacity-50"
          >
            {isLoading ? '加载 python中...' : isRunning ? '运行中...' : '运行 Python'}
          </button>
          {compare && (javascriptCode || typescriptCode) && (
            <button
              onClick={runJavascriptCode}
              disabled={isRunning}
              className="px-3 py-1 text-xs bg-blue-500 text-white rounded hover:bg-blue-600 disabled:opacity-50"
            >
              运行 JS/TS
            </button>
          )}
        </div>
      </div>
      
      {/* 编辑器区域 */}
      <div className="flex flex-col lg:flex-row">
        {/* Python 编辑器 */}
        <div className="flex-1">
          <MonacoEditorWrapper
            language="python"
            theme={currentTheme}
            value={pythonCode}
            onChange={(value) => setPythonCode(value || '')}
            height={height}
            options={{ readOnly }}
          />
        </div>
        
        {/* JavaScript 编辑器（对比模式） */}
        {compare && (javascriptCode || typescriptCode) && (
          <div className="flex-1 lg:border-l border-t lg:border-t-0">
            <MonacoEditorWrapper
              language={javascriptCode ? 'javascript' : 'typescript'}
              theme={currentTheme}
              value={javascriptCode || typescriptCode || ''}
              onChange={(value) => {
                if (javascriptCode) {
                  setJavascriptCode(value || '')
                } else {
                  setTypescriptCode(value || '')
                }
              }}
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
            输出结果
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