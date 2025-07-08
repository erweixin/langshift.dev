'use client'

import React, { useState, useEffect, useRef, useCallback, useMemo } from 'react'
import { useMonacoManager } from './monaco-manager'

interface VirtualizedEditorProps {
  language: string
  value: string
  onChange?: (value: string | undefined) => void
  theme?: string
  height?: number
  options?: any
  isVisible?: boolean
  onVisibilityChange?: (visible: boolean) => void
}

// 虚拟化编辑器组件
export function VirtualizedEditor({
  language,
  value,
  onChange,
  theme = 'vs-light',
  height = 300,
  options = {},
  isVisible = true,
  onVisibilityChange,
  ...props
}: VirtualizedEditorProps) {
  const { isReady, isLoading, error } = useMonacoManager()
  const [shouldRender, setShouldRender] = useState(false)
  const [isInViewport, setIsInViewport] = useState(false)
  const containerRef = useRef<HTMLDivElement>(null)
  const observerRef = useRef<IntersectionObserver | null>(null)

  // 检查元素是否在视口中
  useEffect(() => {
    if (!containerRef.current) return

    const observer = new IntersectionObserver(
      ([entry]) => {
        const visible = entry.isIntersecting
        setIsInViewport(visible)
        
        // 当元素进入视口时，延迟渲染编辑器
        if (visible && !shouldRender) {
          const timer = setTimeout(() => {
            setShouldRender(true)
            onVisibilityChange?.(true)
          }, 100) // 100ms 延迟，避免频繁渲染

          return () => clearTimeout(timer)
        }
        
        if (!visible && shouldRender) {
          onVisibilityChange?.(false)
        }
      },
      {
        rootMargin: '200px', // 提前 200px 开始加载
        threshold: 0.1
      }
    )

    observer.observe(containerRef.current)
    observerRef.current = observer

    return () => {
      observer.disconnect()
    }
  }, [shouldRender, onVisibilityChange])

  // 清理函数
  useEffect(() => {
    return () => {
      if (observerRef.current) {
        observerRef.current.disconnect()
      }
    }
  }, [])

  // 如果不在视口中，显示占位符
  if (!isInViewport) {
    return (
      <div 
        ref={containerRef}
        className="border rounded p-4 bg-gray-50 dark:bg-gray-800"
        style={{ height }}
      >
        <div className="flex items-center justify-center h-full">
          <div className="text-gray-600 dark:text-gray-400 text-sm">
            滚动到此处查看编辑器
          </div>
        </div>
      </div>
    )
  }

  // 如果还没有准备好渲染，显示加载状态
  if (!shouldRender || isLoading || !isReady) {
    return (
      <div 
        ref={containerRef}
        className="border rounded p-4 bg-gray-50 dark:bg-gray-800"
        style={{ height }}
      >
        <div className="flex items-center justify-center h-full">
          <div className="text-gray-600 dark:text-gray-400 text-sm">
            {error ? '编辑器加载失败' : '正在加载编辑器...'}
          </div>
        </div>
      </div>
    )
  }

  // 错误状态
  if (error) {
    return (
      <div 
        ref={containerRef}
        className="border rounded p-4 bg-red-50 dark:bg-red-900/20"
        style={{ height }}
      >
        <div className="text-red-600 dark:text-red-400 text-sm">
          {error}
        </div>
      </div>
    )
  }

  // 渲染实际的编辑器
  return (
    <div ref={containerRef} style={{ height }}>
      <MonacoEditorLazy
        language={language}
        theme={theme}
        value={value}
        onChange={onChange}
        height={height}
        options={options}
        {...props}
      />
    </div>
  )
}

// 懒加载的 Monaco Editor
const MonacoEditorLazy = React.lazy(() => 
  import('@monaco-editor/react').then(module => ({ 
    default: module.default 
  }))
)

// 编辑器容器组件
export function EditorContainer({
  children,
  className = '',
  ...props
}: {
  children: React.ReactNode
  className?: string
  [key: string]: any
}) {
  return (
    <div 
      className={`border rounded overflow-hidden bg-white dark:bg-gray-900 ${className}`}
      {...props}
    >
      {children}
    </div>
  )
}

// 优化的编辑器组管理器
export function useEditorOptimization() {
  const [visibleEditors, setVisibleEditors] = useState<Set<string>>(new Set())
  const [editorStates, setEditorStates] = useState<Map<string, any>>(new Map())

  const registerEditor = useCallback((id: string, initialState: any = {}) => {
    setEditorStates(prev => new Map(prev).set(id, initialState))
  }, [])

  const unregisterEditor = useCallback((id: string) => {
    setEditorStates(prev => {
      const newMap = new Map(prev)
      newMap.delete(id)
      return newMap
    })
    setVisibleEditors(prev => {
      const newSet = new Set(prev)
      newSet.delete(id)
      return newSet
    })
  }, [])

  const setEditorVisible = useCallback((id: string, visible: boolean) => {
    setVisibleEditors(prev => {
      const newSet = new Set(prev)
      if (visible) {
        newSet.add(id)
      } else {
        newSet.delete(id)
      }
      return newSet
    })
  }, [])

  const updateEditorState = useCallback((id: string, updates: any) => {
    setEditorStates(prev => {
      const newMap = new Map(prev)
      const currentState = newMap.get(id) || {}
      newMap.set(id, { ...currentState, ...updates })
      return newMap
    })
  }, [])

  return {
    visibleEditors,
    editorStates,
    registerEditor,
    unregisterEditor,
    setEditorVisible,
    updateEditorState
  }
} 