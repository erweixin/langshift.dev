import { getTranslations, type SupportedLanguage } from '@/messages';
import { Metadata } from 'next';
import { notFound } from 'next/navigation';
import { HomePage } from '@/components/home/HomePage';

// 支持的语言
const supportedLanguages: SupportedLanguage[] = ['zh-cn', 'zh-tw', 'en'];

// 生成静态参数
export async function generateStaticParams() {
  return supportedLanguages.map((lang) => ({
    lang,
  }));
}

// 生成元数据
export async function generateMetadata({ params }: { params: Promise<{ lang: string }> }): Promise<Metadata> {
  const { lang } = await params;
  const supportedLang = lang as SupportedLanguage;
  
  if (!supportedLanguages.includes(supportedLang)) {
    notFound();
  }

  const t = getTranslations(supportedLang);
  const siteUrl = process.env.NEXT_PUBLIC_SITE_URL || 'https://langshift.dev';
  const pageUrl = supportedLang === 'zh-cn' ? siteUrl : `${siteUrl}/${supportedLang}`;

  return {
    title: t.home.seo.title,
    description: t.home.seo.description,
    keywords: t.home.seo.keywords,
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
      canonical: pageUrl,
      languages: {
        'zh-CN': `${siteUrl}/zh-cn`,
        'zh-TW': `${siteUrl}/zh-tw`,
        'en': `${siteUrl}/en`,
        'x-default': siteUrl,
      },
    },
    openGraph: {
      title: t.home.seo.title,
      description: t.home.seo.description,
      url: pageUrl,
      siteName: 'LangShift.dev',
      locale: supportedLang === 'zh-cn' ? 'zh_CN' : supportedLang === 'zh-tw' ? 'zh_TW' : 'en_US',
      type: 'website',
      images: [
        {
          url: `${siteUrl}/og-image.png`,
          width: 1200,
          height: 630,
          alt: supportedLang === 'zh-cn' ? '编程语言转换学习平台' : 'Programming Language Learning Platform',
        },
      ],
    },
    twitter: {
      card: 'summary_large_image',
      title: t.home.seo.title,
      description: t.home.seo.description,
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

export default async function LanguageHomePage({ params }: { params: Promise<{ lang: string }> }) {
  const { lang } = await params;
  const supportedLang = lang as SupportedLanguage;
  
  if (!supportedLanguages.includes(supportedLang)) {
    notFound();
  }

  const t = getTranslations(supportedLang);

  const courses = [
    {
      name: 'js2py',
      title: t.home.courses.js2py.title,
      description: t.home.courses.js2py.description,
      features: t.home.courses.js2py.features,
      duration: t.home.courses.js2py.duration,
      level: t.home.courses.js2py.level,
      icon: '🐍',
      color: 'from-green-500 to-emerald-600',
      bgColor: 'bg-green-500/10',
      borderColor: 'border-green-500/20',
      gradient: 'from-green-400/20 to-emerald-500/20',
    },
    {
      name: 'js2rust',
      title: t.home.courses.js2rust.title,
      description: t.home.courses.js2rust.description,
      features: t.home.courses.js2rust.features,
      duration: t.home.courses.js2rust.duration,
      level: t.home.courses.js2rust.level,
      icon: '🦀',
      color: 'from-orange-500 to-red-600',
      bgColor: 'bg-orange-500/10',
      borderColor: 'border-orange-500/20',
      gradient: 'from-orange-400/20 to-red-500/20',
    },
  ];

  // 生成结构化数据
  const structuredData = JSON.stringify({
    "@context": "https://schema.org",
    "@type": "ItemList",
    "name": t.home.structuredData.courseList,
    "description": t.home.seo.description,
    "numberOfItems": courses.length,
    "itemListElement": courses.map((course, index) => ({
      "@type": "ListItem",
      "position": index + 1,
      "item": {
        "@type": "Course",
        "name": course.title,
        "description": course.description,
        "url": `https://langshift.dev/${supportedLang}/docs/${course.name}`,
        "provider": {
          "@type": "Organization",
          "name": "LangShift.dev"
        }
      }
    }))
  });

  return (
    <HomePage
      lang={supportedLang}
      translations={t}
      courses={courses}
      isDefaultPage={false}
      structuredData={structuredData}
    />
  );
} 