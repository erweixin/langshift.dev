import { source } from "@/lib/source"
import {
  DocsPage,
  DocsBody,
  DocsDescription,
  DocsTitle,
} from "fumadocs-ui/page"
import { notFound } from "next/navigation"
import defaultMdxComponents from "fumadocs-ui/mdx"
import { Code } from "@/components/code"
import { SideBySideCode } from "@/components/side-by-side-code"
import PythonEditor from "@/components/python-editor"
import Script from "next/script"
import UniversalEditor from "@/components/universal-editor"
import { generateKeywordsFromSlug } from "@/lib/seo-keywords"
import { courseStructuredData } from "@/lib/seo-structured-data"

export default async function Page(props: {
  params: Promise<{ slug?: string[], lang: string }>
}) {
  const params = await props.params
  const page = source.getPage(params.slug, params.lang) as unknown as { 
    data: {
      title: string
      description: string
      body: any
      toc: any
      full: boolean
    }
  }
  if (!page) notFound()

  const MDX = page.data.body

  // 获取课程名称
  const courseName = params.slug?.[0] || '';
  const siteUrl = process.env.NEXT_PUBLIC_SITE_URL || 'https://langshift.dev';
  const canonical = `${siteUrl}/${params.lang}/docs/${params.slug?.join('/') || ''}`;
  
  // 课程名称映射
  const courseNameMap: Record<string, { name: string; description: string; level: string }> = {
    'js2py': {
      name: 'JavaScript 到 Python 转换学习',
      description: '从 JavaScript 开发者视角学习 Python，掌握语法转换和概念映射',
      level: 'Intermediate'
    },
    'js2rust': {
      name: 'JavaScript 到 Rust 转换学习',
      description: '从 JavaScript 开发者视角学习 Rust，理解内存安全和系统编程',
      level: 'Advanced'
    },
    'js2go': {
      name: 'JavaScript 到 Go 转换学习',
      description: '从 JavaScript 开发者视角学习 Go，专注于并发编程和云原生开发',
      level: 'Intermediate'
    },
    'js2cpp': {
      name: 'JavaScript 到 C++ 转换学习',
      description: '从 JavaScript 背景掌握 C++，专注于性能、内存管理和系统编程',
      level: 'Advanced'
    },
    'js2swift': {
      name: 'JavaScript 到 Swift 转换学习',
      description: '从 JavaScript 开发者视角学习 Swift，专注于类型安全、iOS 开发和协议导向编程',
      level: 'Intermediate'
    },
    'js2c': {
      name: 'JavaScript 到 C 转换学习',
      description: '从 JavaScript 开发者视角学习 C 语言，掌握内存管理、指针操作和系统编程',
      level: 'Advanced'
    },
    'js2kotlin': {
      name: 'JavaScript 到 Kotlin 转换学习',
      description: '从 JavaScript 开发者视角学习 Kotlin，掌握协程编程、Android 开发和 JVM 生态系统',
      level: 'Intermediate'
    }
  };
  
  const courseInfo = courseNameMap[courseName] || {
    name: '编程语言转换学习',
    description: '编程语言转换学习课程',
    level: 'Intermediate'
  };

  // 生成结构化数据
  const articleStructuredData = {
    "@context": "https://schema.org",
    "@type": "TechArticle",
    "headline": page.data.title,
    "description": page.data.description,
    "author": {
      "@type": "Organization",
      "name": "LangShift.dev"
    },
    "publisher": {
      "@type": "Organization",
      "name": "LangShift.dev",
      "logo": {
        "@type": "ImageObject",
        "url": `${siteUrl}/logo.png`
      }
    },
    "datePublished": new Date().toISOString(),
    "dateModified": new Date().toISOString(),
    "mainEntityOfPage": {
      "@type": "WebPage",
      "@id": canonical
    },
    "inLanguage": params.lang === 'zh-cn' ? 'zh-CN' : params.lang === 'zh-tw' ? 'zh-TW' : 'en',
    "isPartOf": courseStructuredData({
      name: courseInfo.name,
      description: courseInfo.description,
      url: `${siteUrl}/${params.lang}/docs/${courseName}`,
      provider: "LangShift.dev",
      courseMode: "online",
      educationalLevel: courseInfo.level
    })
  };

  const allStructuredData = [articleStructuredData];

  return (
    <>
      <Script
        id="structured-data"
        type="application/ld+json"
        dangerouslySetInnerHTML={{
          __html: JSON.stringify(allStructuredData),
        }}
      />
      <DocsPage toc={page.data.toc} full={page.data.full}>
        <DocsTitle>{page.data.title}</DocsTitle>
        <DocsDescription>{page.data.description}</DocsDescription>
        <DocsBody>
          <MDX components={{ ...defaultMdxComponents, Code, SideBySideCode, PythonEditor, UniversalEditor }} />
        </DocsBody>
      </DocsPage>
    </>
  )
}

export async function generateStaticParams() {
  return source.generateParams()
}

export async function generateMetadata(props: {
  params: Promise<{ slug?: string[], lang: string }>
}) {
  const params = await props.params
  const page = source.getPage(params.slug, params.lang) as unknown as {
    data: {
      title: string
      description: string
    }
  }
  if (!page) notFound()

  // 生成页面关键词
  const keywords = generateKeywordsFromSlug(params.slug, params.lang)
  const siteUrl = process.env.NEXT_PUBLIC_SITE_URL || 'https://langshift.dev'
  const canonical = params.slug ? `${siteUrl}/${params.lang}/docs/${params.slug.join('/')}` : `${siteUrl}/${params.lang}/docs`

  // 优化标题，移除域名
  const pageTitle = page.data.title.replace(/^LangShift\.dev\s*[-–—]\s*/, '')

  return {
    title: pageTitle,
    description: page.data.description,
    keywords: keywords,
    authors: [{ name: 'LangShift.dev' }],
    creator: 'LangShift.dev',
    publisher: 'LangShift.dev',
    robots: {
      index: true,
      follow: true,
      googleBot: {
        index: true,
        follow: true,
        'max-video-preview': -1,
        'max-image-preview': 'large',
        'max-snippet': -1,
      },
    },
    alternates: {
      canonical: canonical,
      languages: {
        'zh-CN': `${siteUrl}/zh-cn/docs/${params.slug?.join('/') || ''}`,
        'zh-TW': `${siteUrl}/zh-tw/docs/${params.slug?.join('/') || ''}`,
        'en': `${siteUrl}/en/docs/${params.slug?.join('/') || ''}`,
        'x-default': `${siteUrl}/zh-cn/docs/${params.slug?.join('/') || ''}`,
      },
    },
    openGraph: {
      title: pageTitle,
      description: page.data.description,
      url: canonical,
      siteName: 'LangShift.dev',
      locale: params.lang === 'zh-cn' ? 'zh_CN' : params.lang === 'zh-tw' ? 'zh_TW' : 'en_US',
      type: 'article',
    },
    twitter: {
      card: 'summary_large_image',
      title: pageTitle,
      description: page.data.description,
      creator: '@langshift_dev',
      site: '@langshift_dev',
    },
    other: {
      'theme-color': '#1e293b',
      'color-scheme': 'light dark',
      'application-name': 'LangShift.dev',
      'apple-mobile-web-app-title': 'LangShift.dev',
      'apple-mobile-web-app-capable': 'yes',
      'apple-mobile-web-app-status-bar-style': 'default',
    },
  };
}
