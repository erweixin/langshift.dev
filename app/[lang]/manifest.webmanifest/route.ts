import { NextRequest, NextResponse } from 'next/server';

// 为静态导出配置
export const dynamic = 'force-static';
export const revalidate = false;

// 生成静态参数
export async function generateStaticParams() {
  return [
    { lang: 'zh-cn' },
    { lang: 'zh-tw' },
    { lang: 'en' },
  ];
}

export async function GET(
  request: NextRequest,
  { params }: { params: Promise<{ lang: string }> }
) {
  const lang = (await params).lang;
  const baseUrl = process.env.NEXT_PUBLIC_SITE_URL || 'https://langshift.dev';
  
  // 语言配置
  const langConfig = {
    'zh-cn': {
      name: 'LangShift.dev - 编程语言转换学习平台',
      short_name: 'LangShift',
      description: '专门为开发者设计的编程语言转换学习平台',
      start_url: `/zh-cn`,
      lang: 'zh-CN',
      shortcuts: [
        {
          name: 'JavaScript 到 Python',
          short_name: 'JS2Py',
          description: '学习 JavaScript 到 Python 的转换',
          url: `/zh-cn/docs/js2py`,
        },
        {
          name: 'JavaScript 到 Rust',
          short_name: 'JS2Rust',
          description: '学习 JavaScript 到 Rust 的转换',
          url: `/zh-cn/docs/js2rust`,
        },
        {
          name: 'JavaScript 到 Go',
          short_name: 'JS2Go',
          description: '学习 JavaScript 到 Go 的转换',
          url: `/zh-cn/docs/js2go`,
        },
      ]
    },
    'zh-tw': {
      name: 'LangShift.dev - 程式語言轉換學習平台',
      short_name: 'LangShift',
      description: '專門為開發者設計的程式語言轉換學習平台',
      start_url: `/zh-tw`,
      lang: 'zh-TW',
      shortcuts: [
        {
          name: 'JavaScript 到 Python',
          short_name: 'JS2Py',
          description: '學習 JavaScript 到 Python 的轉換',
          url: `/zh-tw/docs/js2py`,
        },
        {
          name: 'JavaScript 到 Rust',
          short_name: 'JS2Rust',
          description: '學習 JavaScript 到 Rust 的轉換',
          url: `/zh-tw/docs/js2rust`,
        },
        {
          name: 'JavaScript 到 Go',
          short_name: 'JS2Go',
          description: '學習 JavaScript 到 Go 的轉換',
          url: `/zh-tw/docs/js2go`,
        },
      ]
    },
    'en': {
      name: 'LangShift.dev - Programming Language Learning Platform',
      short_name: 'LangShift',
      description: 'Learn new programming languages faster through syntax comparison and concept mapping',
      start_url: `/en`,
      lang: 'en-US',
      shortcuts: [
        {
          name: 'JavaScript to Python',
          short_name: 'JS2Py',
          description: 'Learn JavaScript to Python conversion',
          url: `/en/docs/js2py`,
        },
        {
          name: 'JavaScript to Rust',
          short_name: 'JS2Rust',
          description: 'Learn JavaScript to Rust conversion',
          url: `/en/docs/js2rust`,
        },
        {
          name: 'JavaScript to Go',
          short_name: 'JS2Go',
          description: 'Learn JavaScript to Go conversion',
          url: `/en/docs/js2go`,
        },
      ]
    }
  };

  const config = langConfig[lang as keyof typeof langConfig] || langConfig['zh-cn'];
  
  const manifest = {
    name: config.name,
    short_name: config.short_name,
    description: config.description,
    start_url: config.start_url,
    display: 'standalone',
    background_color: '#1e293b',
    theme_color: '#1e293b',
    orientation: 'portrait-primary',
    scope: '/',
    lang: config.lang,
    dir: 'ltr',
    categories: ['education', 'productivity', 'developer'],
    shortcuts: config.shortcuts.map(shortcut => ({
      ...shortcut,
      icons: [
        {
          src: `/shortcut-${shortcut.short_name.toLowerCase()}.png`,
          sizes: '96x96',
        },
      ],
    })),
    related_applications: [
      {
        platform: 'webapp',
        url: baseUrl,
      },
    ],
    prefer_related_applications: false,
  };

  return NextResponse.json(manifest, {
    headers: {
      'Content-Type': 'application/manifest+json',
    },
  });
}