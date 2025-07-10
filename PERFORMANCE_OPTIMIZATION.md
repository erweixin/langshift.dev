# LangShift.dev 性能优化指南

## 概述

LangShift.dev 采用 Next.js 15.3+ App Router 架构，通过多种优化策略确保快速加载和良好的用户体验。

## SSG (Static Site Generation) 配置

### 已启用的 SSG 页面

1. **首页** (`app/page.tsx`)
   - 使用 `generateMetadata()` 生成动态元数据
   - 支持多语言 SEO 优化
   - 包含结构化数据

2. **文档页面** (`app/[lang]/docs/[[...slug]]/page.tsx`)
   - 使用 `generateStaticParams()` 预生成所有文档页面
   - 支持多语言文档
   - 包含技术文章结构化数据

3. **介绍页面** (`app/[lang]/intro/[[...slug]]/page.tsx`)
   - 使用 `generateStaticParams()` 预生成所有介绍页面
   - 支持多语言内容

### 静态生成的优势

- **快速加载**: 页面在构建时预生成，无需服务器端渲染
- **SEO 友好**: 搜索引擎可以快速索引所有页面
- **CDN 友好**: 静态文件可以部署到 CDN 边缘节点
- **成本效益**: 减少服务器计算资源需求

## 性能优化策略

### 1. 构建优化

```javascript
// next.config.mjs
const config = {
  // 启用 SWC 压缩
  swcMinify: true,
  
  // 压缩配置
  compress: true,
  
  // 实验性功能
  experimental: {
    optimizeCss: true,
    optimizePackageImports: ['@monaco-editor/react', 'fumadocs-ui'],
    modularizeImports: {
      '@monaco-editor/react': {
        transform: '@monaco-editor/react/{{member}}',
      },
    },
  },
}
```

### 2. 图片优化

- 使用 Next.js Image 组件
- 支持 WebP 和 AVIF 格式
- 响应式图片尺寸
- 懒加载优化

### 3. 代码分割

- 自动代码分割
- 动态导入重组件
- Monaco Editor 按需加载
- 路由级别代码分割

### 4. 缓存策略

```javascript
// 静态资源缓存
{
  source: '/:path*.(js|css|png|jpg|jpeg|gif|ico|svg|woff|woff2|ttf|eot)',
  headers: [
    {
      key: 'Cache-Control',
      value: 'public, max-age=31536000, immutable',
    },
  ],
}
```

### 5. 预连接优化

```html
<link rel="preconnect" href="https://fonts.googleapis.com" />
<link rel="preconnect" href="https://fonts.gstatic.com" crossOrigin="anonymous" />
<link rel="preconnect" href="https://cdn.jsdelivr.net" />
```

## SEO 优化

### 1. 元数据优化

- 使用 Next.js Metadata API
- 动态生成标题和描述
- 多语言 SEO 支持
- Open Graph 和 Twitter Cards

### 2. 结构化数据

- 课程结构化数据
- 技术文章结构化数据
- 组织信息结构化数据
- 面包屑导航结构化数据

### 3. 站点地图

- 自动生成 XML 站点地图
- 包含所有静态页面
- 多语言版本支持
- 更新频率和优先级配置

### 4. Robots.txt

- 传统 robots.txt 文件
- Next.js robots.ts API
- 搜索引擎友好配置
- 禁止访问私有页面

## 监控和分析

### 1. 性能指标

- Core Web Vitals
- First Contentful Paint (FCP)
- Largest Contentful Paint (LCP)
- Cumulative Layout Shift (CLS)

### 2. 构建分析

```bash
# 分析构建包大小
pnpm build
pnpm analyze

# 检查包依赖
pnpm why <package-name>
```

### 3. 运行时监控

- 编辑器性能监控
- 代码执行性能
- 内存使用情况
- 错误追踪

## 部署优化

### 1. Vercel 部署

- 自动静态优化
- 边缘函数支持
- 全球 CDN 分发
- 自动 HTTPS

### 2. 其他平台

- Netlify 静态部署
- GitHub Pages
- AWS S3 + CloudFront
- 自托管服务器

## 最佳实践

### 1. 开发阶段

- 使用 `pnpm dev` 进行开发
- 启用 React Strict Mode
- 使用 TypeScript 严格模式
- 定期运行性能测试

### 2. 构建阶段

- 检查构建警告和错误
- 分析包大小
- 验证静态生成页面
- 测试多语言支持

### 3. 部署阶段

- 验证所有页面可访问
- 检查 SEO 元数据
- 测试性能指标
- 监控错误日志

## 故障排除

### 常见问题

1. **构建失败**
   - 检查 TypeScript 错误
   - 验证依赖版本兼容性
   - 检查环境变量配置

2. **页面加载缓慢**
   - 分析包大小
   - 检查图片优化
   - 验证缓存配置

3. **SEO 问题**
   - 验证元数据生成
   - 检查结构化数据
   - 测试搜索引擎爬虫

### 调试工具

- Chrome DevTools
- Lighthouse
- WebPageTest
- Next.js Analytics

## 未来优化计划

1. **增量静态再生 (ISR)**
   - 定期更新静态内容
   - 保持页面新鲜度

2. **服务端组件优化**
   - 减少客户端 JavaScript
   - 提升首屏性能

3. **PWA 支持**
   - 离线功能
   - 推送通知
   - 应用安装

4. **国际化优化**
   - 按需加载语言包
   - 智能语言检测
   - 本地化内容优化 