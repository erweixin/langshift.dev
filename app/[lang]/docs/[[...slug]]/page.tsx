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

  // 生成结构化数据
  const structuredData = {
    "@context": "https://schema.org",
    "@type": "TechArticle",
    "headline": page.data.title,
    "description": page.data.description,
    "author": {
      "@type": "Organization",
      "name": "js2py"
    },
    "publisher": {
      "@type": "Organization",
      "name": "JavaScript 到 Python 教程",
      "logo": {
        "@type": "ImageObject",
        "url": "https://js2py.lites.dev/logo.png"
      }
    },
    "datePublished": new Date().toISOString(),
    "dateModified": new Date().toISOString(),
    "mainEntityOfPage": {
      "@type": "WebPage",
      "@id": `https://js2py.lites.dev/docs/${params.slug?.join('/') || ''}`
    },
    "inLanguage": "zh-CN",
    "isPartOf": {
      "@type": "Course",
      "name": "JavaScript 到 Python 教程",
      "description": "专为前端开发者设计的 Python 学习教程"
    }
  }

  return (
    <>
      <Script
        id="structured-data"
        type="application/ld+json"
        dangerouslySetInnerHTML={{
          __html: JSON.stringify(structuredData),
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
      images: [
        {
          url: `${siteUrl}/og-image.png`,
          width: 1200,
          height: 630,
          alt: pageTitle,
        },
      ],
    },
    twitter: {
      card: 'summary_large_image',
      title: pageTitle,
      description: page.data.description,
      images: [`${siteUrl}/og-image.png`],
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
