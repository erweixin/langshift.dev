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

export default async function Page(props: {
  params: Promise<{ slug?: string[] }>
}) {
  const params = await props.params
  const page = source.getPage(params.slug) as unknown as {
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
          <MDX components={{ ...defaultMdxComponents, Code, SideBySideCode, PythonEditor }} />
        </DocsBody>
      </DocsPage>
    </>
  )
}

export async function generateStaticParams() {
  return source.generateParams()
}

export async function generateMetadata(props: {
  params: Promise<{ slug?: string[] }>
}) {
  const params = await props.params
  const page = source.getPage(params.slug) as unknown as {
    data: {
      title: string
      description: string
    }
  }
  if (!page) notFound()

  return {
    title: page.data.title,
    description: page.data.description,
  }
}
