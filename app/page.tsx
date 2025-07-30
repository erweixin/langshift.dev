import { courseStructuredData } from '@/lib/seo-structured-data';
import { getTranslations, type SupportedLanguage } from '@/messages';
import { headers } from 'next/headers';
import { Metadata } from 'next';
import { redirect } from 'next/navigation';
import { HomePage } from '@/components/home/HomePage';
import { getCourses } from '@/lib';

// 生成静态元数据 - 使用默认语言
export async function generateMetadata(): Promise<Metadata> {
  const t = getTranslations('zh-cn'); // 使用简体中文作为默认
  const siteUrl = process.env.NEXT_PUBLIC_SITE_URL || 'https://langshift.dev';

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
      canonical: siteUrl,
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
      url: siteUrl,
      siteName: 'LangShift.dev',
      locale: 'zh_CN',
      type: 'website',
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

// 获取用户首选语言 - 用于服务端重定向
async function getPreferredLanguage(): Promise<SupportedLanguage> {
  const headersList = await headers();
  const acceptLanguage = headersList.get('accept-language') || '';
  
  // 解析 Accept-Language 头
  const languages = acceptLanguage
    .split(',')
    .map(lang => lang.split(';')[0].trim().toLowerCase());
  
  // 按优先级匹配语言
  for (const lang of languages) {
    if (lang.startsWith('zh-tw')) return 'zh-tw';
    if (lang.startsWith('zh-cn') || lang.startsWith('zh')) return 'zh-cn';
    if (lang.startsWith('en')) return 'en';
  }
  
  // 默认返回简体中文
  return 'zh-cn';
}

export default async function HomePageComponent() {
  // 检测用户语言并重定向到对应的语言页面
  const preferredLang = await getPreferredLanguage();
  if (preferredLang !== 'zh-cn') {
    redirect(`/${preferredLang}`);
  }

  const t = getTranslations('zh-cn'); // 使用简体中文作为默认

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
        url: `https://langshift.dev/zh-cn/docs/${course.name}`,
        provider: "LangShift.dev",
        courseMode: "online",
        educationalLevel: course.level === "Advanced" ? "Advanced" : "Intermediate"
      })
    }))
  });

  // 语言重定向脚本 - 客户端检测
  const languageRedirectScript = `
    (function() {
      // 只在客户端执行
      if (typeof window === 'undefined') return;
      
      // 检查是否已经重定向过
      if (sessionStorage.getItem('langRedirected')) return;
      
      // 获取用户首选语言
      const userLang = navigator.language || navigator.userLanguage || 'zh-CN';
      const lang = userLang.toLowerCase();
      
      // 确定目标语言
      let targetLang = 'zh-cn';
      if (lang.startsWith('zh-tw')) {
        targetLang = 'zh-tw';
      } else if (lang.startsWith('en')) {
        targetLang = 'en';
      }
      
      // 如果不是默认语言，进行重定向
      if (targetLang !== 'zh-cn') {
        sessionStorage.setItem('langRedirected', 'true');
        window.location.href = '/' + targetLang;
      }
    })();
  `;

  return (
    <HomePage
      lang="zh-cn"
      translations={t}
      courses={courses}
      isDefaultPage={true}
      structuredData={structuredData}
      languageRedirectScript={languageRedirectScript}
    />
  );
}