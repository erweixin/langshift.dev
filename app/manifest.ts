import { MetadataRoute } from 'next'

// 为静态导出配置
export const dynamic = 'force-static'
export const revalidate = false

export default function manifest(): MetadataRoute.Manifest {
  const baseUrl = process.env.NEXT_PUBLIC_SITE_URL || 'https://langshift.dev'
  
  return {
    name: 'LangShift.dev - 编程语言转换学习平台',
    short_name: 'LangShift',
    description: '专门为开发者设计的编程语言转换学习平台',
    start_url: '/',
    display: 'standalone',
    background_color: '#1e293b',
    theme_color: '#1e293b',
    orientation: 'portrait-primary',
    scope: '/',
    lang: 'zh-CN',
    dir: 'ltr',
    categories: ['education', 'productivity', 'developer'],
    shortcuts: [
      {
        name: 'JavaScript 到 Python',
        short_name: 'JS2Py',
        description: '学习 JavaScript 到 Python 的转换',
        url: '/js2py',
        icons: [
          {
            src: '/shortcut-js2py.png',
            sizes: '96x96',
          },
        ],
      },
      {
        name: 'JavaScript 到 Rust',
        short_name: 'JS2Rust',
        description: '学习 JavaScript 到 Rust 的转换',
        url: '/js2rust',
        icons: [
          {
            src: '/shortcut-js2rust.png',
            sizes: '96x96',
          },
        ],
      },
    ],
    related_applications: [
      {
        platform: 'webapp',
        url: baseUrl,
      },
    ],
    prefer_related_applications: false,
  }
} 