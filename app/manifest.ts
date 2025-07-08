import { MetadataRoute } from 'next'

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
    icons: [
      {
        src: '/icon-192x192.png',
        sizes: '192x192',
        type: 'image/png',
        purpose: 'maskable',
      },
      {
        src: '/icon-512x512.png',
        sizes: '512x512',
        type: 'image/png',
        purpose: 'maskable',
      },
      {
        src: '/apple-touch-icon.png',
        sizes: '180x180',
        type: 'image/png',
      },
    ],
    screenshots: [
      {
        src: '/screenshot-desktop.png',
        sizes: '1280x720',
        type: 'image/png',
        form_factor: 'wide',
        label: 'LangShift.dev 桌面版界面',
      },
      {
        src: '/screenshot-mobile.png',
        sizes: '390x844',
        type: 'image/png',
        form_factor: 'narrow',
        label: 'LangShift.dev 移动版界面',
      },
    ],
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