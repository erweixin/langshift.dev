'use client'

import { SEOHead } from './seo-head';
import { courseStructuredData } from '@/lib/seo-structured-data';
import { usePathname } from 'next/navigation';

interface SEODocPageProps {
  title: string;
  description: string;
  keywords?: string[];
  lang?: string;
  courseName?: string;
  moduleName?: string;
  lastModified?: string;
  author?: string;
}

export function SEODocPage({
  title,
  description,
  keywords = [],
  lang = 'zh-CN',
  courseName,
  moduleName,
  lastModified,
  author = 'LangShift.dev'
}: SEODocPageProps) {
  const pathname = usePathname();
  const siteUrl = process.env.NEXT_PUBLIC_SITE_URL || 'https://langshift.dev';
  const canonical = `${siteUrl}${pathname}`;
  
  // 生成面包屑结构化数据
  const breadcrumbStructuredData = {
    "@context": "https://schema.org",
    "@type": "BreadcrumbList",
    "itemListElement": [
      {
        "@type": "ListItem",
        "position": 1,
        "name": "首页",
        "item": `${siteUrl}/${lang}`
      },
      ...(courseName ? [{
        "@type": "ListItem",
        "position": 2,
        "name": courseName,
        "item": `${siteUrl}/${lang}/${courseName}`
      }] : []),
      ...(moduleName ? [{
        "@type": "ListItem",
        "position": 3,
        "name": moduleName,
        "item": canonical
      }] : [])
    ]
  };

  // 生成文章结构化数据
  const articleStructuredData = {
    "@context": "https://schema.org",
    "@type": "Article",
    "headline": title,
    "description": description,
    "author": {
      "@type": "Organization",
      "name": author
    },
    "publisher": {
      "@type": "Organization",
      "name": "LangShift.dev",
      "logo": {
        "@type": "ImageObject",
        "url": `${siteUrl}/logo.png`
      }
    },
    "datePublished": lastModified || new Date().toISOString(),
    "dateModified": lastModified || new Date().toISOString(),
    "mainEntityOfPage": {
      "@type": "WebPage",
      "@id": canonical
    },
    "inLanguage": lang,
    "isPartOf": courseName ? {
      "@type": "Course",
      "name": courseName,
      "url": `${siteUrl}/${lang}/${courseName}`
    } : undefined
  };

  // 如果是课程页面，添加课程结构化数据
  const courseData = courseName ? courseStructuredData({
    name: courseName,
    description: description,
    url: `${siteUrl}/${lang}/${courseName}`,
    provider: 'LangShift.dev',
    courseMode: 'online',
    educationalLevel: 'intermediate',
  }) : null;

  const allStructuredData = [
    breadcrumbStructuredData,
    articleStructuredData,
    ...(courseData ? [courseData] : [])
  ];

  return (
    <>
      <SEOHead
        title={title}
        description={description}
        keywords={keywords}
        canonical={canonical}
        ogType="article"
        structuredData={allStructuredData}
        lang={lang}
      />
      
      {/* 额外的文档页面 meta 标签 */}
      <meta name="article:author" content={author} />
      {lastModified && <meta name="article:modified_time" content={lastModified} />}
      {lastModified && <meta name="article:published_time" content={lastModified} />}
      <meta name="article:section" content={courseName || 'Programming'} />
      <meta name="article:tag" content={keywords.join(', ')} />
    </>
  );
}

// 面包屑导航组件
export function BreadcrumbNav({ 
  items 
}: { 
  items: Array<{ name: string; href: string }> 
}) {
  return (
    <nav className="flex items-center space-x-2 text-sm text-slate-400 mb-6">
      {items.map((item, index) => (
        <div key={item.href} className="flex items-center">
          {index > 0 && (
            <svg className="w-4 h-4 mx-2" fill="none" stroke="currentColor" viewBox="0 0 24 24">
              <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M9 5l7 7-7 7" />
            </svg>
          )}
          <a
            href={item.href}
            className="hover:text-blue-400 transition-colors"
          >
            {item.name}
          </a>
        </div>
      ))}
    </nav>
  );
} 