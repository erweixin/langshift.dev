'use client'

import { useEffect, useRef, useState } from 'react'
import { loader } from '@monaco-editor/react'
import { getMonacoEditorCDN } from '@/lib/cdn-disaster-recovery'

// Monaco Editor 全局管理器
class MonacoManager {
  private static instance: MonacoManager
  private isInitialized = false
  private initPromise: Promise<void> | null = null
  private subscribers: Set<() => void> = new Set()

  private constructor() {}

  static getInstance(): MonacoManager {
    if (!MonacoManager.instance) {
      MonacoManager.instance = new MonacoManager()
    }
    return MonacoManager.instance
  }

  async initialize(): Promise<void> {
    if (this.isInitialized) {
      return
    }

    if (this.initPromise) {
      return this.initPromise
    }

    this.initPromise = this.loadMonaco()
    
    try {
      await this.initPromise
      this.isInitialized = true
      // 通知所有订阅者
      this.subscribers.forEach(callback => callback())
    } catch (error) {
      this.initPromise = null
      throw error
    }

    return this.initPromise
  }

  private async loadMonaco(): Promise<void> {
    try {
      // 获取健康的 Monaco Editor CDN
      const healthyCDN = await getMonacoEditorCDN()
      console.log('healthyCDN', healthyCDN)
      // 配置 Monaco Editor 加载器
      loader.config({
        paths: {
          vs: `${healthyCDN}/vs`
        }
      })

      console.log(`Monaco Editor 使用 CDN: ${healthyCDN}`)

      // 预加载 Monaco Editor
      await loader.init()
    } catch (error) {
      console.error('Monaco Editor CDN 容灾加载失败，使用默认 CDN:', error)
      
      // 备用方案：使用默认 CDN
      loader.config({
        paths: {
          vs: 'https://cdn.jsdelivr.net/npm/monaco-editor@0.52.2/min/vs'
        }
      })

      await loader.init()
    }
  }

  subscribe(callback: () => void): () => void {
    this.subscribers.add(callback)
    
    // 如果已经初始化，立即调用回调
    if (this.isInitialized) {
      callback()
    }

    return () => {
      this.subscribers.delete(callback)
    }
  }

  isReady(): boolean {
    return this.isInitialized
  }
}

// 全局管理器实例
const monacoManager = MonacoManager.getInstance()

// React Hook 用于管理 Monaco Editor 状态
export function useMonacoManager() {
  const [isReady, setIsReady] = useState(false)
  const [isLoading, setIsLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    const unsubscribe = monacoManager.subscribe(() => {
      setIsReady(true)
      setIsLoading(false)
    })

    // 如果还没有初始化，开始初始化
    if (!monacoManager.isReady()) {
      monacoManager.initialize().catch((err) => {
        console.error('Monaco Editor 初始化失败:', err)
        setError('编辑器初始化失败')
        setIsLoading(false)
      })
    }

    return unsubscribe
  }, [])

  return { isReady, isLoading, error }
}

// 优化的 Monaco Editor 组件
export function OptimizedMonacoEditor({
  language,
  value,
  onChange,
  theme = 'vs-light',
  height = 300,
  options = {},
  ...props
}: {
  language: string
  value: string
  onChange?: (value: string | undefined) => void
  theme?: string
  height?: number
  options?: any
  [key: string]: any
}) {
  const { isReady, isLoading, error } = useMonacoManager()
  const editorRef = useRef<any>(null)

  if (error) {
    return (
      <div className="border rounded p-4 bg-red-50 dark:bg-red-900/20">
        <div className="text-red-600 dark:text-red-400 text-sm">
          {error}
        </div>
      </div>
    )
  }

  if (isLoading) {
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

  if (!isReady) {
    return (
      <div className="border rounded p-4 bg-gray-50 dark:bg-gray-800">
        <div className="flex items-center justify-center h-32">
          <div className="text-gray-600 dark:text-gray-400 text-sm">
            编辑器未就绪
          </div>
        </div>
      </div>
    )
  }

  return (
    <div className="border rounded overflow-hidden">
      <div
        ref={editorRef}
        style={{ height }}
        className="w-full"
        {...props}
      />
    </div>
  )
} 