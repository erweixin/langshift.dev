/**
 * CDN 容灾管理器
 * 支持多个 CDN 源的自动故障转移
 */

export interface CDNConfig {
  name: string
  baseUrl: string
  priority: number // 优先级，数字越小优先级越高
}

export interface CDNResource {
  name: string
  cdns: CDNConfig[]
  healthCheck?: (url: string) => Promise<boolean>
}

export class CDNDisasterRecovery {
  private static instance: CDNDisasterRecovery
  private healthCache: Map<string, { healthy: boolean; lastCheck: number }> = new Map()
  private readonly cacheTimeout = 5 * 60 * 1000 // 5分钟缓存

  private constructor() {}

  static getInstance(): CDNDisasterRecovery {
    if (!CDNDisasterRecovery.instance) {
      CDNDisasterRecovery.instance = new CDNDisasterRecovery()
    }
    return CDNDisasterRecovery.instance
  }

  /**
   * 检查 CDN 健康状态
   */
  private async checkCDNHealth(url: string, timeout = 3000): Promise<boolean> {
    try {
      const controller = new AbortController()
      const timeoutId = setTimeout(() => {
        controller.abort()
        throw new Error('CDN 健康检查超时')
      }, timeout)

      const response = await fetch(url, {
        method: 'HEAD',
        signal: controller.signal,
        cache: 'no-cache'
      })

      clearTimeout(timeoutId)
      return response.ok
    } catch (error) {
      console.warn(`CDN 健康检查失败: ${url}`, error)
      return false
    }
  }

  /**
   * 获取健康的 CDN URL
   */
  async getHealthyCDN(resource: CDNResource, path: string = ''): Promise<string> {
    const sortedCDNs = [...resource.cdns].sort((a, b) => a.priority - b.priority)
    
    for (const cdn of sortedCDNs) {
      const fullUrl = `${cdn.baseUrl}${path}`
      const cacheKey = cdn.baseUrl
      const cached = this.healthCache.get(cacheKey)
      
      // 检查缓存
      if (cached && Date.now() - cached.lastCheck < this.cacheTimeout) {
        if (cached.healthy) {
          console.log(`使用缓存的健康 CDN: ${cdn.name} - ${fullUrl}`)
          return cdn.baseUrl
        }
        continue
      }

      // 健康检查
      const isHealthy = resource.healthCheck 
        ? await resource.healthCheck(fullUrl)
        : await this.checkCDNHealth(fullUrl)
      
      // 更新缓存
      this.healthCache.set(cacheKey, {
        healthy: isHealthy,
        lastCheck: Date.now()
      })

      if (isHealthy) {
        console.log(`CDN 健康检查通过: ${cdn.name} - ${fullUrl}`)
        return cdn.baseUrl
      } else {
        console.warn(`CDN 健康检查失败: ${cdn.name} - ${fullUrl}`)
      }
    }

    // 如果所有 CDN 都不健康，返回优先级最高的
    const fallbackUrl = `${sortedCDNs[0].baseUrl}`
    console.warn(`所有 CDN 都不健康，使用备用: ${fallbackUrl}`)
    return fallbackUrl
  }

  /**
   * 清除健康检查缓存
   */
  clearHealthCache(): void {
    this.healthCache.clear()
  }

  /**
   * 批量预检查 CDN 健康状态
   */
  async preCheckCDNs(resources: CDNResource[]): Promise<void> {
    const checkPromises: Promise<void>[] = []

    for (const resource of resources) {
      for (const cdn of resource.cdns) {
        checkPromises.push(
          (async () => {
            const isHealthy = resource.healthCheck 
              ? await resource.healthCheck(cdn.baseUrl)
              : await this.checkCDNHealth(cdn.baseUrl)
            
            this.healthCache.set(cdn.baseUrl, {
              healthy: isHealthy,
              lastCheck: Date.now()
            })
          })()
        )
      }
    }

    await Promise.allSettled(checkPromises)
  }
}

// 预定义的 CDN 配置
export const CDN_CONFIGS = {
  // Monaco Editor CDN 配置
  MONACO_EDITOR: {
    name: 'monaco-editor',
    cdns: [
      {
        name: 'JSDelivr',
        baseUrl: 'https://cdn.jsdelivr.net/npm/monaco-editor@0.52.2/min',
        priority: 1
      },
      {
        name: 'JSDelivr 备用',
        baseUrl: 'https://fastly.jsdelivr.net/npm/monaco-editor@0.52.2/min',
        priority: 2
      },
      {
        name: 'UNPKG',
        baseUrl: 'https://unpkg.com/monaco-editor@0.52.2/min',
        priority: 3
      },
      {
        name: 'bootcdn',
        baseUrl: 'https://cdn.bootcdn.net/ajax/libs/monaco-editor/0.52.2/min',
        priority: 4
      },
    ]
  } as CDNResource,

  // Pyodide CDN 配置
  PYODIDE: {
    name: 'pyodide',
    cdns: [
      {
        name: 'JSDelivr',
        baseUrl: 'https://cdn.jsdelivr.net/pyodide/v0.27.0/full',
        priority: 1
      },
      {
        name: 'JSDelivr 备用',
        baseUrl: 'https://fastly.jsdelivr.net/pyodide/v0.27.0/full',
        priority: 2
      },
      {
        name: 'UNPKG',
        baseUrl: 'https://unpkg.com/pyodide@0.27.0/dist',
        priority: 3
      },
      {
        name: 'bootcdn',
        baseUrl: 'https://cdn.bootcdn.net/ajax/libs/pyodide/0.28.0',
        priority: 4
      }
    ]
  } as CDNResource
}

// 全局 CDN 管理器实例
export const cdnManager = CDNDisasterRecovery.getInstance()

// 便捷函数
export async function getMonacoEditorCDN(): Promise<string> {
  return cdnManager.getHealthyCDN(CDN_CONFIGS.MONACO_EDITOR, '/vs/loader.min.js')
}

export async function getPyodideCDN(): Promise<string> {
  return cdnManager.getHealthyCDN(CDN_CONFIGS.PYODIDE)
}

// 预检查所有 CDN
export async function preCheckAllCDNs(): Promise<void> {
  await cdnManager.preCheckCDNs([
    CDN_CONFIGS.MONACO_EDITOR,
    CDN_CONFIGS.PYODIDE
  ])
}