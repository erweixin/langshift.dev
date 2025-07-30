import Head from 'next/head'

interface SEOHeadProps {
  title?: string
  description?: string
  keywords?: string[]
  canonical?: string
  noindex?: boolean
  ogType?: string
  twitterCard?: string
  structuredData?: object
  lang?: string
  showAlternateLinks?: boolean
}

export function SEOHead({
  title = '编程语言转换学习平台',
  description = 'LangShift.dev 是专门为开发者设计的编程语言转换学习平台。通过对比不同编程语言的语法特性和概念映射，帮助开发者快速掌握新语言。',
  keywords = ['编程语言', '语言学习', 'JavaScript', 'Python', 'Rust', '开发者', '代码对比', '语法转换'],
  canonical,
  noindex = false,
  ogType = 'website',
  twitterCard = 'summary_large_image',
  structuredData,
  lang = 'zh-CN',
  showAlternateLinks = false
}: SEOHeadProps) {
  const siteUrl = process.env.NEXT_PUBLIC_SITE_URL || 'https://langshift.dev'
  const fullCanonical = canonical ? `${siteUrl}${canonical}` : siteUrl

  return (
    <Head>
      {/* 基础 SEO 标签 */}
      <title>{title}</title>
      <meta name="description" content={description} />
      <meta name="keywords" content={keywords.join(', ')} />
      <meta name="author" content="LangShift.dev" />
      <meta name="robots" content={noindex ? 'noindex, nofollow' : 'index, follow'} />
      <link rel="canonical" href={fullCanonical} />
      
      {/* 多语言 alternate 链接 */}
      {showAlternateLinks && (
        <>
          <link rel="alternate" href={`${siteUrl}/zh-cn`} hrefLang="zh-CN" />
          <link rel="alternate" href={`${siteUrl}/zh-tw`} hrefLang="zh-TW" />
          <link rel="alternate" href={`${siteUrl}/en`} hrefLang="en" />
          <link rel="alternate" href={siteUrl} hrefLang="x-default" />
        </>
      )}
      
      {/* 语言和编码 */}
      <meta charSet="utf-8" />
      <meta name="viewport" content="width=device-width, initial-scale=1" />
      <meta httpEquiv="Content-Language" content={lang} />
      
      {/* Open Graph */}
      <meta property="og:title" content={title} />
      <meta property="og:description" content={description} />
      <meta property="og:type" content={ogType} />
      <meta property="og:url" content={fullCanonical} />
      <meta property="og:image:width" content="1200" />
      <meta property="og:image:height" content="630" />
      <meta property="og:site_name" content="LangShift.dev" />
      <meta property="og:locale" content={lang} />
      
      {/* Twitter Cards */}
      <meta name="twitter:card" content={twitterCard} />
      <meta name="twitter:title" content={title} />
      <meta name="twitter:description" content={description} />
      <meta name="twitter:site" content="@langshift_dev" />
      <meta name="twitter:creator" content="@langshift_dev" />
      
      {/* 其他重要 meta 标签 */}
      <meta name="theme-color" content="#1e293b" />
      <meta name="color-scheme" content="light dark" />
      <meta name="application-name" content="LangShift.dev" />
      <meta name="apple-mobile-web-app-title" content="LangShift.dev" />
      <meta name="apple-mobile-web-app-capable" content="yes" />
      <meta name="apple-mobile-web-app-status-bar-style" content="default" />
      
      {/* 结构化数据 */}
      {structuredData && (
        <script
          type="application/ld+json"
          dangerouslySetInnerHTML={{
            __html: JSON.stringify(structuredData)
          }}
        />
      )}
      
      {/* 预连接优化 */}
      <link rel="preconnect" href="https://fonts.googleapis.com" />
      <link rel="preconnect" href="https://fonts.gstatic.com" crossOrigin="anonymous" />
      <link rel="preconnect" href="https://cdn.jsdelivr.net" />
      
      {/* DNS 预取 */}
      <link rel="dns-prefetch" href="//www.google-analytics.com" />
      <link rel="dns-prefetch" href="//cdn.jsdelivr.net" />
    </Head>
  )
} 