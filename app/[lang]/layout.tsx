import '@/styles/global.css';

import { I18nProvider, type Translations } from 'fumadocs-ui/i18n';
import { RootProvider } from 'fumadocs-ui/provider';
import Script from 'next/script';
import { SEOHead } from '@/components/seo-head';
import { defaultStructuredData, organizationStructuredData } from '@/lib/seo-structured-data';
import { Analytics } from '@/components/analytics';
import { Metadata } from 'next';

const zhCn: Partial<Translations> = {
  search: '简体中文',
  // other translations
};

const zhTw: Partial<Translations> = {
  search: '繁體中文',
  // other translations
};

// available languages that will be displayed on UI
// make sure `locale` is consistent with your i18n config
const locales = [
  {
    name: 'English',
    locale: 'en',
  },
  {
    name: 'Simplified Chinese',
    locale: 'zh-cn',
  },
  {
    name: 'Traditional Chinese',
    locale: 'zh-tw',
  },
];

export async function generateMetadata({
  params,
}: {
  params: Promise<{ lang: string }>;
}): Promise<Metadata> {
  const lang = (await params).lang;
  
  // 语言配置
  const langConfig = {
    'zh-cn': {
      manifest: '/zh-cn/manifest.webmanifest',
    },
    'zh-tw': {
      manifest: '/zh-tw/manifest.webmanifest',
    },
    'en': {
      manifest: '/en/manifest.webmanifest',
    }
  };

  const config = langConfig[lang as keyof typeof langConfig] || langConfig['zh-cn'];
  
  return {
    manifest: config.manifest,
  };
}

export default async function RootLayout({
  params,
  children,
}: {
  params: Promise<{ lang: string }>;
  children: React.ReactNode;
}) {
  const lang = (await params).lang;
  const isChinese = lang === 'zh-cn' || lang === 'zh-tw';
  
  const seoLang = isChinese ? 'zh-CN' : 'en-US';
  const seoTitle = isChinese 
    ? '编程语言转换学习平台' 
    : 'Programming Language Learning Platform';
  const seoDescription = isChinese
    ? 'LangShift.dev 是专门为开发者设计的编程语言转换学习平台。通过对比不同编程语言的语法特性和概念映射，帮助开发者快速掌握新语言。'
    : 'Learn new programming languages faster through syntax comparison and concept mapping. Designed for developers who want to leverage existing knowledge to master Python, Rust and more with interactive code editors and progressive learning paths.';

  return (
    <html lang={lang} suppressHydrationWarning>
      <head>
        {/* Pyodide 运行时 */}
        <Script
          src="https://cdn.jsdelivr.net/pyodide/v0.27.0/full/pyodide.js"
          strategy="beforeInteractive"
        />
        
        {/* 基础图标和 PWA 支持 */}
        <link rel="icon" href="/favicon.ico" />
        
        {/* 主题和颜色 */}
        <meta name="theme-color" content="#1e293b" />
        <meta name="color-scheme" content="light dark" />
        
        {/* 结构化数据 */}
        <script
          type="application/ld+json"
          dangerouslySetInnerHTML={{
            __html: JSON.stringify(defaultStructuredData)
          }}
        />
        <script
          type="application/ld+json"
          dangerouslySetInnerHTML={{
            __html: JSON.stringify(organizationStructuredData)
          }}
        />
      </head>
      <body>
        <I18nProvider
          locale={lang}
          locales={locales}
          translations={{ zhCn, zhTw }[lang]}
        >
          <RootProvider>
            <SEOHead
              title={seoTitle}
              description={seoDescription}
              lang={seoLang}
              keywords={isChinese 
                ? ['编程语言', '语言学习', 'JavaScript', 'Python', 'Rust', '开发者', '代码对比', '语法转换']
                : ['programming languages', 'language learning', 'JavaScript', 'Python', 'Rust', 'developers', 'code comparison', 'syntax conversion']
              }
              showAlternateLinks={true}
            />
            {children}
            <Analytics 
              gaId={process.env.NEXT_PUBLIC_GA_ID}
              gtmId={process.env.NEXT_PUBLIC_GTM_ID}
            />
          </RootProvider>
        </I18nProvider>
      </body>
    </html>
  );
}
