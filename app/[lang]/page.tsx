import { getTranslations, type SupportedLanguage } from '@/messages';
import { Metadata } from 'next';
import { notFound } from 'next/navigation';
import { HomePage } from '@/components/home/HomePage';
import { getCourses } from '@/lib';
import { courseStructuredData } from '@/lib/seo-structured-data';

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
    },
    twitter: {
      card: 'summary_large_image',
      title: t.home.seo.title,
      description: t.home.seo.description,
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

  const courses = getCourses(t);
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
      "item": courseStructuredData({
        name: course.title,
        description: course.description,
        url: `https://langshift.dev/${supportedLang}/docs/${course.name}`,
        provider: "LangShift.dev",
        courseMode: "online",
        educationalLevel: course.level === "Advanced" ? "Advanced" : "Intermediate"
      })
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