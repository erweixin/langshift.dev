# LangShift.dev SEO 优化指南

本文档记录了 LangShift.dev 项目的 SEO 优化配置和最佳实践。

## SEO 组件架构

### 1. SEOHead 组件 (`components/seo-head.tsx`)

主要的 SEO 组件，提供以下功能：

- **基础 SEO 标签**：title, description, keywords, canonical
- **Open Graph**：社交媒体分享优化
- **Twitter Cards**：Twitter 分享优化
- **结构化数据**：JSON-LD 格式的结构化数据
- **预连接优化**：DNS 预取和预连接
- **PWA 支持**：应用图标和主题色

### 2. SEODocPage 组件 (`components/seo-doc-page.tsx`)

专门用于文档页面的 SEO 组件：

- **面包屑导航**：结构化数据支持
- **文章结构化数据**：Article 类型的 JSON-LD
- **课程结构化数据**：Course 类型的 JSON-LD
- **页面特定元数据**：作者、发布时间等

### 3. Analytics 组件 (`components/analytics.tsx`)

网站分析组件：

- **Google Analytics 4**：页面浏览和事件追踪
- **Google Tag Manager**：标签管理
- **自定义事件追踪**：课程进度、代码执行等

## 配置文件

### 1. sitemap.ts (`app/sitemap.ts`)

动态生成网站地图，包含：

- 所有语言版本的页面
- 课程和模块页面
- 更新频率和优先级设置

### 2. robots.ts (`app/robots.ts`)

搜索引擎爬虫规则：

- 允许和禁止的路径
- 针对不同爬虫的规则
- sitemap 位置

### 3. manifest.ts (`app/manifest.ts`)

PWA 配置文件：

- 应用名称和描述
- 图标和截图
- 快捷方式配置
- 主题色和显示模式

### 4. next.config.mjs

Next.js 配置优化：

- 图片优化配置
- 安全头部设置
- 缓存策略
- 重定向规则

## 结构化数据

### 1. 网站结构化数据

```json
{
  "@context": "https://schema.org",
  "@type": "WebSite",
  "name": "LangShift.dev",
  "description": "编程语言转换学习平台",
  "url": "https://langshift.dev",
  "potentialAction": {
    "@type": "SearchAction",
    "target": "https://langshift.dev/search?q={search_term_string}",
    "query-input": "required name=search_term_string"
  }
}
```

### 2. 组织结构化数据

```json
{
  "@context": "https://schema.org",
  "@type": "Organization",
  "name": "LangShift.dev",
  "url": "https://langshift.dev",
  "logo": "https://langshift.dev/logo.png",
  "description": "编程语言转换学习平台",
  "sameAs": [
    "https://github.com/langshift-dev",
    "https://twitter.com/langshift_dev"
  ]
}
```

### 3. 课程结构化数据

```json
{
  "@context": "https://schema.org",
  "@type": "Course",
  "name": "JavaScript 到 Python",
  "description": "从 JavaScript 开发者视角学习 Python",
  "provider": {
    "@type": "Organization",
    "name": "LangShift.dev"
  },
  "courseMode": "online",
  "educationalLevel": "intermediate"
}
```

### 4. 文章结构化数据

```json
{
  "@context": "https://schema.org",
  "@type": "Article",
  "headline": "文章标题",
  "description": "文章描述",
  "author": {
    "@type": "Organization",
    "name": "LangShift.dev"
  },
  "publisher": {
    "@type": "Organization",
    "name": "LangShift.dev"
  },
  "datePublished": "2024-01-15",
  "dateModified": "2024-01-15"
}
```

## 多语言 SEO

### 1. 语言检测

根据 URL 路径检测语言：
- `/en/` - 英文
- `/zh-cn/` - 简体中文
- `/zh-tw/` - 繁体中文

### 2. 语言特定元数据

- 根据语言设置不同的 title 和 description
- 语言特定的关键词
- 正确的 lang 属性

### 3. hreflang 标签

为多语言页面添加 hreflang 标签：

```html
<link rel="alternate" hreflang="en" href="https://langshift.dev/en/" />
<link rel="alternate" hreflang="zh-cn" href="https://langshift.dev/zh-cn/" />
<link rel="alternate" hreflang="zh-tw" href="https://langshift.dev/zh-tw/" />
```

## 性能优化

### 1. 图片优化

- 使用 Next.js Image 组件
- WebP 和 AVIF 格式支持
- 响应式图片尺寸
- 懒加载

### 2. 资源预加载

```html
<link rel="preconnect" href="https://fonts.googleapis.com" />
<link rel="preconnect" href="https://fonts.gstatic.com" crossOrigin="anonymous" />
```

### 3. 缓存策略

- 静态资源：1年缓存
- API 路由：不缓存
- 页面内容：根据更新频率设置

## 安全配置

### 1. 安全头部

```javascript
{
  'X-Content-Type-Options': 'nosniff',
  'X-Frame-Options': 'DENY',
  'X-XSS-Protection': '1; mode=block',
  'Referrer-Policy': 'strict-origin-when-cross-origin',
  'Permissions-Policy': 'camera=(), microphone=(), geolocation=()'
}
```

### 2. CSP 配置

为 SVG 图片设置内容安全策略：

```javascript
contentSecurityPolicy: "default-src 'self'; script-src 'none'; sandbox;"
```

## 环境变量

### 必需的环境变量

```bash
# 网站基础配置
NEXT_PUBLIC_SITE_URL=https://langshift.dev

# SEO 配置
NEXT_PUBLIC_SITE_NAME=LangShift.dev
NEXT_PUBLIC_SITE_DESCRIPTION=编程语言转换学习平台

# 社交媒体配置
NEXT_PUBLIC_TWITTER_HANDLE=@langshift_dev
NEXT_PUBLIC_GITHUB_URL=https://github.com/langshift-dev

# 分析工具配置
NEXT_PUBLIC_GA_ID=G-XXXXXXXXXX
NEXT_PUBLIC_GTM_ID=GTM-XXXXXXXX
```

## 最佳实践

### 1. 内容优化

- 每个页面都有唯一的 title 和 description
- 使用语义化的 HTML 标签
- 添加适当的标题层级 (H1-H6)
- 包含相关的关键词

### 2. 技术优化

- 确保页面加载速度快
- 实现响应式设计
- 提供良好的用户体验
- 支持无障碍访问

### 3. 链接优化

- 使用描述性的锚文本
- 避免断开的链接
- 实现面包屑导航
- 添加内部链接

### 4. 监控和分析

- 使用 Google Search Console 监控搜索表现
- 通过 Google Analytics 分析用户行为
- 定期检查 Core Web Vitals
- 监控页面加载速度

## 测试工具

### 1. SEO 测试工具

- Google Search Console
- Google PageSpeed Insights
- GTmetrix
- Lighthouse

### 2. 结构化数据测试

- Google Rich Results Test
- Schema.org Validator

### 3. 移动端测试

- Google Mobile-Friendly Test
- Responsive Design Checker

## 维护指南

### 1. 定期检查

- 每月检查 sitemap 更新
- 季度审查关键词表现
- 定期更新内容
- 监控技术 SEO 指标

### 2. 内容更新

- 保持内容的新鲜度
- 定期添加新的课程模块
- 更新过时的信息
- 优化现有内容

### 3. 技术维护

- 更新依赖包
- 检查安全漏洞
- 优化性能
- 修复技术问题

## 常见问题

### 1. 页面不被索引

- 检查 robots.txt 配置
- 确认页面没有被 noindex
- 检查 sitemap 是否包含该页面
- 验证页面可访问性

### 2. 结构化数据错误

- 使用 Google Rich Results Test 验证
- 检查 JSON-LD 语法
- 确认数据类型正确
- 验证必需字段

### 3. 性能问题

- 优化图片大小和格式
- 减少 JavaScript 包大小
- 启用压缩
- 使用 CDN

---

通过遵循这些 SEO 最佳实践，LangShift.dev 将能够获得更好的搜索引擎排名和用户体验。 