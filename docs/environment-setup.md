# 环境变量配置指南

## 概述

LangShift.dev 使用环境变量来管理配置，包括 Google Analytics、网站 URL 等。

## 必需的环境变量

### 1. Google Analytics

```bash
# .env.local
NEXT_PUBLIC_GA_ID=G-F8EL781P96
```

**说明**：
- `NEXT_PUBLIC_GA_ID`: Google Analytics 4 的测量 ID
- 当前配置的 ID: `G-F8EL781P96`
- 使用 `NEXT_PUBLIC_` 前缀使其在客户端可用

### 2. 网站配置

```bash
# .env.local
NEXT_PUBLIC_SITE_URL=https://langshift.dev
```

**说明**：
- `NEXT_PUBLIC_SITE_URL`: 网站的完整 URL
- 用于生成规范链接和结构化数据

## 环境变量文件

### 开发环境

创建 `.env.local` 文件（已添加到 .gitignore）：

```bash
# Google Analytics
NEXT_PUBLIC_GA_ID=G-F8EL781P96

# 网站配置
NEXT_PUBLIC_SITE_URL=https://langshift.dev

# 开发环境
NODE_ENV=development
```

### 生产环境

在生产环境中，通过部署平台设置环境变量：

**Vercel**:
```bash
NEXT_PUBLIC_GA_ID=G-F8EL781P96
NEXT_PUBLIC_SITE_URL=https://langshift.dev
```

**Netlify**:
```bash
NEXT_PUBLIC_GA_ID=G-F8EL781P96
NEXT_PUBLIC_SITE_URL=https://langshift.dev
```

## 验证配置

### 1. 检查环境变量

在代码中检查环境变量是否正确加载：

```typescript
console.log('GA ID:', process.env.NEXT_PUBLIC_GA_ID)
console.log('Site URL:', process.env.NEXT_PUBLIC_SITE_URL)
```

### 2. 测试 GA 集成

访问 `/test-ga` 页面来测试 Google Analytics 集成：

1. 启动开发服务器：`npm run dev`
2. 访问：`http://localhost:8000/test-ga`
3. 点击测试按钮
4. 检查浏览器控制台的 GA 事件日志

### 3. 验证 GA 数据

1. 登录 Google Analytics 控制台
2. 查看实时报告
3. 确认事件数据正在收集

## 故障排除

### 常见问题

1. **GA 事件不触发**
   - 检查 `NEXT_PUBLIC_GA_ID` 是否正确设置
   - 确认 Analytics 组件已正确导入
   - 检查浏览器控制台是否有错误

2. **环境变量未加载**
   - 确认 `.env.local` 文件存在
   - 重启开发服务器
   - 检查变量名是否正确

3. **生产环境问题**
   - 确认部署平台的环境变量设置
   - 检查构建日志
   - 验证域名配置

### 调试技巧

1. **开发环境调试**
   ```typescript
   // 在组件中添加调试信息
   console.log('GA ID:', process.env.NEXT_PUBLIC_GA_ID)
   console.log('Site URL:', process.env.NEXT_PUBLIC_SITE_URL)
   ```

2. **GA 调试模式**
   ```typescript
   // 在 Analytics 组件中启用调试
   if (process.env.NODE_ENV === 'development') {
     console.log('GA Event:', eventName, parameters)
   }
   ```

3. **浏览器调试**
   - 打开开发者工具
   - 查看 Network 标签页的 GA 请求
   - 检查 Console 标签页的错误信息

## 安全注意事项

1. **不要提交敏感信息**
   - `.env.local` 文件已添加到 `.gitignore`
   - 不要在代码中硬编码 API 密钥

2. **环境变量命名**
   - 客户端变量使用 `NEXT_PUBLIC_` 前缀
   - 服务器端变量不使用前缀

3. **生产环境安全**
   - 使用环境变量而不是硬编码值
   - 定期轮换 API 密钥
   - 监控异常访问

## 相关文档

- [Google Analytics 配置指南](./analytics-setup.md)
- [Next.js 环境变量文档](https://nextjs.org/docs/basic-features/environment-variables)
- [项目部署指南](./deployment.md) 