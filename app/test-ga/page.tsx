'use client'

import { trackEvent, trackCodeExecution, trackLanguageSwitch, trackEditorUsage } from '@/components/analytics'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'

export default function TestGAPage() {
  const testEvents = [
    {
      name: '测试自定义事件',
      action: () => trackEvent('test_custom_event', { test: true, timestamp: Date.now() })
    },
    {
      name: '测试代码执行成功',
      action: () => trackCodeExecution('python', true)
    },
    {
      name: '测试代码执行失败',
      action: () => trackCodeExecution('javascript', false)
    },
    {
      name: '测试语言切换',
      action: () => trackLanguageSwitch('zh-cn', 'en')
    },
    {
      name: '测试编辑器使用',
      action: () => trackEditorUsage('universal-editor', 'code-edit')
    }
  ]

  return (
    <div className="container mx-auto px-4 py-8">
      <div className="max-w-2xl mx-auto">
        <h1 className="text-3xl font-bold mb-8">Google Analytics 测试页面</h1>
        
        <Card className="mb-6">
          <CardHeader>
            <CardTitle>GA 配置信息</CardTitle>
            <CardDescription>
              当前使用的 Google Analytics ID: {process.env.NEXT_PUBLIC_GA_ID || 'G-F8EL781P96'}
            </CardDescription>
          </CardHeader>
          <CardContent>
            <p className="text-sm text-gray-600">
              打开浏览器开发者工具的控制台，可以看到 GA 事件被触发。
            </p>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>测试事件</CardTitle>
            <CardDescription>
              点击下面的按钮来测试不同的 GA 事件追踪
            </CardDescription>
          </CardHeader>
          <CardContent className="space-y-4">
            {testEvents.map((event, index) => (
              <Button
                key={index}
                onClick={event.action}
                variant="outline"
                className="w-full"
              >
                {event.name}
              </Button>
            ))}
          </CardContent>
        </Card>

        <div className="mt-8 p-4 bg-gray-50 rounded-lg">
          <h3 className="font-semibold mb-2">如何验证：</h3>
          <ol className="list-decimal list-inside space-y-1 text-sm text-gray-600">
            <li>打开浏览器开发者工具 (F12)</li>
            <li>切换到 Console 标签页</li>
            <li>点击上面的测试按钮</li>
            <li>查看控制台是否有 GA 相关的日志</li>
            <li>在 Google Analytics 实时报告中查看事件</li>
          </ol>
        </div>
      </div>
    </div>
  )
} 