# Google Analytics 配置指南

## 概述

LangShift.dev 已集成 Google Analytics 4 (GA4) 用于网站分析。配置已优化以确保不会影响静态站点生成 (SSG) 性能。

## 当前配置

- **GA ID**: `G-F8EL781P96`
- **加载策略**: `afterInteractive` (不影响 SSG)
- **隐私保护**: 启用 IP 匿名化

## 环境变量配置

创建 `.env.local` 文件（如果不存在）：

```bash
# Google Analytics
NEXT_PUBLIC_GA_ID=G-F8EL781P96

# 网站配置
NEXT_PUBLIC_SITE_URL=https://langshift.dev
```

## 功能特性

### 自动追踪事件

1. **页面浏览** - 自动追踪所有页面访问
2. **课程进度** - 追踪学习进度
3. **代码执行** - 追踪代码编辑器使用
4. **搜索行为** - 追踪搜索查询
5. **语言切换** - 追踪多语言使用
6. **编辑器使用** - 追踪编辑器交互

### 自定义维度

- `dimension1`: 课程名称
- `dimension2`: 编程语言
- `dimension3`: 模块名称
- `dimension4`: 用户类型

### 隐私保护

- IP 地址匿名化
- 禁用 Google 信号
- 禁用广告个性化信号

## 使用方法

### 在组件中使用

```tsx
import { trackEvent, trackCourseProgress } from '@/components/analytics';

// 追踪自定义事件
trackEvent('button_click', { button_name: 'start_learning' });

// 追踪课程进度
trackCourseProgress('JavaScript to Python', '语法对比', 75);
```

### 页面浏览追踪

页面浏览会自动追踪，无需手动调用。

## 性能优化

- 使用 `afterInteractive` 策略加载，不影响首屏渲染
- 脚本异步加载，不阻塞页面渲染
- 仅在客户端执行，完全兼容 SSG

## 调试

在开发环境中，可以在浏览器控制台查看 GA 事件：

```javascript
// 查看 dataLayer
console.log(window.dataLayer);

// 手动触发事件
window.gtag('event', 'test_event', { test: true });
```

## 注意事项

1. 确保 `.env.local` 文件已正确配置
2. 在生产环境中验证 GA 数据收集
3. 定期检查 GA 报告以优化用户体验
4. 遵守 GDPR 和隐私法规要求 