'use client'

import React, { useState, useEffect, useRef } from 'react'

interface PerformanceMetrics {
  editorCount: number
  visibleEditors: number
  totalMemoryUsage: number
  averageLoadTime: number
  renderTime: number
}

interface EditorPerformanceMonitorProps {
  children: React.ReactNode
  showMetrics?: boolean
  onMetricsUpdate?: (metrics: PerformanceMetrics) => void
}

// 性能监控 Hook
export function useEditorPerformance() {
  const [metrics, setMetrics] = useState<PerformanceMetrics>({
    editorCount: 0,
    visibleEditors: 0,
    totalMemoryUsage: 0,
    averageLoadTime: 0,
    renderTime: 0
  })

  const [isMonitoring, setIsMonitoring] = useState(false)
  const startTimeRef = useRef<number>(0)
  const loadTimesRef = useRef<number[]>([])

  const startMonitoring = () => {
    setIsMonitoring(true)
    startTimeRef.current = performance.now()
  }

  const stopMonitoring = () => {
    setIsMonitoring(false)
  }

  const recordLoadTime = (loadTime: number) => {
    loadTimesRef.current.push(loadTime)
    if (loadTimesRef.current.length > 10) {
      loadTimesRef.current.shift() // 只保留最近10次
    }
  }

  const updateMetrics = (updates: Partial<PerformanceMetrics>) => {
    setMetrics(prev => ({
      ...prev,
      ...updates,
      averageLoadTime: loadTimesRef.current.length > 0 
        ? loadTimesRef.current.reduce((a, b) => a + b, 0) / loadTimesRef.current.length 
        : 0
    }))
  }

  const getMemoryUsage = async (): Promise<number> => {
    if ('memory' in performance) {
      return (performance as any).memory.usedJSHeapSize / 1024 / 1024 // MB
    }
    return 0
  }

  return {
    metrics,
    isMonitoring,
    startMonitoring,
    stopMonitoring,
    recordLoadTime,
    updateMetrics,
    getMemoryUsage
  }
}

// 性能监控组件
export function EditorPerformanceMonitor({
  children,
  showMetrics = false,
  onMetricsUpdate
}: EditorPerformanceMonitorProps) {
  const {
    metrics,
    isMonitoring,
    startMonitoring,
    stopMonitoring,
    recordLoadTime,
    updateMetrics,
    getMemoryUsage
  } = useEditorPerformance()

  const intervalRef = useRef<NodeJS.Timeout | null>(null)

  useEffect(() => {
    if (isMonitoring) {
      intervalRef.current = setInterval(async () => {
        const memoryUsage = await getMemoryUsage()
        const renderTime = performance.now() - (performance.now() - 16) // 估算渲染时间

        updateMetrics({
          totalMemoryUsage: memoryUsage,
          renderTime
        })

        onMetricsUpdate?.(metrics)
      }, 1000) // 每秒更新一次
    }

    return () => {
      if (intervalRef.current) {
        clearInterval(intervalRef.current)
      }
    }
  }, [isMonitoring, updateMetrics, onMetricsUpdate, metrics, getMemoryUsage])

  useEffect(() => {
    // 自动开始监控
    startMonitoring()

    return () => {
      stopMonitoring()
    }
  }, [startMonitoring, stopMonitoring])

  return (
    <div className="relative">
      {children}
      
      {showMetrics && (
        <div className="fixed bottom-4 right-4 bg-black/80 text-white p-4 rounded-lg text-xs font-mono z-50">
          <div className="mb-2 font-bold">编辑器性能监控</div>
          <div>编辑器数量: {metrics.editorCount}</div>
          <div>可见编辑器: {metrics.visibleEditors}</div>
          <div>内存使用: {metrics.totalMemoryUsage.toFixed(2)} MB</div>
          <div>平均加载时间: {metrics.averageLoadTime.toFixed(2)} ms</div>
          <div>渲染时间: {metrics.renderTime.toFixed(2)} ms</div>
        </div>
      )}
    </div>
  )
}

// 性能优化建议组件
export function PerformanceOptimizationTips() {
  const [tips, setTips] = useState<string[]>([])

  useEffect(() => {
    const performanceTips = [
      '使用虚拟化编辑器减少 DOM 节点数量',
      '实现懒加载，只在需要时渲染编辑器',
      '共享 Monaco Editor 实例减少内存占用',
      '使用 Intersection Observer 检测可见性',
      '合理设置编辑器高度避免过度渲染',
      '定期清理不可见的编辑器实例',
      '使用 Web Workers 处理大量代码分析',
      '实现编辑器状态缓存减少重复计算'
    ]

    setTips(performanceTips)
  }, [])

  return (
    <div className="bg-blue-50 dark:bg-blue-900/20 border border-blue-200 dark:border-blue-800 rounded-lg p-4">
      <h3 className="text-sm font-semibold text-blue-800 dark:text-blue-200 mb-2">
        性能优化建议
      </h3>
      <ul className="text-xs text-blue-700 dark:text-blue-300 space-y-1">
        {tips.map((tip, index) => (
          <li key={index} className="flex items-start">
            <span className="text-blue-500 mr-2">•</span>
            {tip}
          </li>
        ))}
      </ul>
    </div>
  )
}

// 编辑器统计组件
export function EditorStats({ 
  totalEditors, 
  activeEditors, 
  memoryUsage 
}: {
  totalEditors: number
  activeEditors: number
  memoryUsage: number
}) {
  const efficiency = totalEditors > 0 ? (activeEditors / totalEditors) * 100 : 0

  return (
    <div className="grid grid-cols-3 gap-4 p-4 bg-gray-50 dark:bg-gray-800 rounded-lg">
      <div className="text-center">
        <div className="text-2xl font-bold text-blue-600 dark:text-blue-400">
          {totalEditors}
        </div>
        <div className="text-xs text-gray-600 dark:text-gray-400">
          总编辑器数
        </div>
      </div>
      
      <div className="text-center">
        <div className="text-2xl font-bold text-green-600 dark:text-green-400">
          {activeEditors}
        </div>
        <div className="text-xs text-gray-600 dark:text-gray-400">
          活跃编辑器
        </div>
      </div>
      
      <div className="text-center">
        <div className="text-2xl font-bold text-purple-600 dark:text-purple-400">
          {memoryUsage.toFixed(1)}
        </div>
        <div className="text-xs text-gray-600 dark:text-gray-400">
          内存使用 (MB)
        </div>
      </div>
      
      <div className="col-span-3">
        <div className="flex items-center justify-between text-xs text-gray-600 dark:text-gray-400 mb-1">
          <span>效率</span>
          <span>{efficiency.toFixed(1)}%</span>
        </div>
        <div className="w-full bg-gray-200 dark:bg-gray-700 rounded-full h-2">
          <div 
            className="bg-gradient-to-r from-blue-500 to-green-500 h-2 rounded-full transition-all duration-300"
            style={{ width: `${efficiency}%` }}
          />
        </div>
      </div>
    </div>
  )
} 