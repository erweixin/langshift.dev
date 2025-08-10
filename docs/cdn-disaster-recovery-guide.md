# CDN 容灾机制使用指南

## 概述

为了提高 LangShift.dev 的稳定性和可用性，我们实现了一套完整的 CDN 容灾机制。当主 CDN 不可用时，系统会自动切换到备用 CDN 源，确保网站功能正常。

## 功能特点

- **自动故障转移**: 智能检测 CDN 健康状态，自动切换到可用的 CDN
- **多源支持**: 支持 JSDelivr、UNPKG、阿里云、字节跳动等多个 CDN 源
- **健康检查缓存**: 避免重复检查，提高性能
- **优雅降级**: 即使所有 CDN 检查失败，也会使用默认配置

## 支持的 CDN 资源

### Monaco Editor
- JSDelivr (主源)
- JSDelivr Fastly (备源)
- UNPKG
- 字节跳动 CDN
- 阿里云 CDN

### Pyodide
- JSDelivr (主源)
- JSDelivr Fastly (备源)
- UNPKG
- 字节跳动 CDN
- 阿里云 CDN

## 使用方法

### 基本使用

```typescript
import { getMonacoEditorCDN, getPyodideCDN } from '@/lib/cdn-disaster-recovery'

// 获取健康的 Monaco Editor CDN
const monacoURL = await getMonacoEditorCDN()

// 获取健康的 Pyodide CDN
const pyodideURL = await getPyodideCDN()
```

### 预检查所有 CDN

```typescript
import { preCheckAllCDNs } from '@/lib/cdn-disaster-recovery'

// 在应用启动时预检查所有 CDN
await preCheckAllCDNs()
```

### 自定义 CDN 配置

```typescript
import { CDNDisasterRecovery, CDNConfig, CDNResource } from '@/lib/cdn-disaster-recovery'

const customResource: CDNResource = {
  name: 'my-library',
  cdns: [
    {
      name: 'Primary CDN',
      baseUrl: 'https://cdn.example.com/lib',
      priority: 1
    },
    {
      name: 'Backup CDN',
      baseUrl: 'https://backup.example.com/lib',
      priority: 2
    }
  ],
  healthCheck: async (url: string) => {
    // 自定义健康检查逻辑
    const response = await fetch(`${url}/check.js`, { method: 'HEAD' })
    return response.ok
  }
}

const cdnManager = CDNDisasterRecovery.getInstance()
const healthyURL = await cdnManager.getHealthyCDN(customResource, '/v1.0.0')
```

## 配置说明

### CDN 优先级

CDN 按照 `priority` 字段排序，数字越小优先级越高：

1. **JSDelivr (priority: 1)**: 主要 CDN，速度快，稳定性好
2. **JSDelivr Fastly (priority: 2)**: JSDelivr 的备用节点
3. **UNPKG (priority: 3)**: 备用 CDN，兼容性好
4. **字节跳动 (priority: 4)**: 国内用户优化
5. **阿里云 (priority: 5)**: 国内备用源

### 健康检查策略

- **检查频率**: 每 5 分钟重新检查一次
- **超时时间**: 5 秒超时
- **检查方法**: HEAD 请求，验证关键文件可访问性
- **缓存机制**: 成功的检查结果会被缓存，避免重复请求

## 监控和日志

系统会在控制台输出详细的 CDN 使用信息：

```
Monaco Editor 使用 CDN: https://cdn.jsdelivr.net/npm/monaco-editor@0.52.2/min
Pyodide 使用 CDN: https://cdn.jsdelivr.net/pyodide/v0.27.0/full
CDN 健康检查通过: JSDelivr - https://cdn.jsdelivr.net/npm/monaco-editor@0.52.2/min/vs/loader.js
```

如果 CDN 切换，会显示警告信息：

```
CDN 健康检查失败: JSDelivr - https://cdn.jsdelivr.net/npm/monaco-editor@0.52.2/min/vs/loader.js
Monaco Editor CDN 容灾加载失败，使用默认 CDN
```

## 性能优化

- **并行检查**: 多个 CDN 同时进行健康检查
- **缓存策略**: 避免重复的健康检查请求
- **预检查**: 应用启动时预先检查 CDN 状态
- **懒加载**: 只在需要时加载和检查 CDN

## 故障排除

### 常见问题

1. **所有 CDN 都不可用**
   - 系统会使用最高优先级的 CDN 作为备用
   - 检查网络连接和防火墙设置

2. **特定 CDN 总是失败**
   - 检查该 CDN 的 URL 配置是否正确
   - 验证健康检查逻辑是否合适

3. **切换频繁**
   - 检查网络稳定性
   - 调整健康检查缓存时间

### 调试模式

在开发环境中，可以通过以下方式查看详细日志：

```typescript
import { cdnManager } from '@/lib/cdn-disaster-recovery'

// 清除缓存，强制重新检查
cdnManager.clearHealthCache()

// 手动预检查
await preCheckAllCDNs()
```

## 扩展指南

### 添加新的 CDN 源

1. 在 `CDN_CONFIGS` 中添加新的 CDN 配置
2. 设置合适的优先级
3. 实现自定义健康检查（如果需要）

```typescript
export const CDN_CONFIGS = {
  MY_LIBRARY: {
    name: 'my-library',
    cdns: [
      // 新的 CDN 配置
      {
        name: '新 CDN',
        baseUrl: 'https://new-cdn.example.com/lib',
        priority: 6
      }
    ],
    healthCheck: async (url: string) => {
      // 自定义检查逻辑
      return true
    }
  } as CDNResource
}
```

### 集成新的库

1. 创建新的 CDNResource 配置
2. 实现获取函数
3. 在相关组件中使用

```typescript
export async function getNewLibraryCDN(): Promise<string> {
  return cdnManager.getHealthyCDN(CDN_CONFIGS.MY_LIBRARY)
}
```

这套 CDN 容灾机制确保了 LangShift.dev 在各种网络环境下都能稳定运行，提供了良好的用户体验。