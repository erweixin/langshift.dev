'use client'

import Script from 'next/script'

interface AnalyticsProps {
  gaId?: string
  gtmId?: string
}

export function Analytics({ 
  gaId = process.env.NEXT_PUBLIC_GA_ID || 'G-F8EL781P96', 
  gtmId 
}: AnalyticsProps) {
  if (!gaId && !gtmId) {
    return null
  }

  return (
    <>
      {/* Google Analytics 4 */}
      {gaId && (
        <>
          <Script
            src={`https://www.googletagmanager.com/gtag/js?id=${gaId}`}
            strategy="afterInteractive"
            id="google-analytics-script"
          />
          <Script id="google-analytics" strategy="afterInteractive">
            {`
              window.dataLayer = window.dataLayer || [];
              function gtag(){dataLayer.push(arguments);}
              gtag('js', new Date());
              gtag('config', '${gaId}', {
                page_title: document.title,
                page_location: window.location.href,
                custom_map: {
                  'custom_parameter_1': 'course_name',
                  'custom_parameter_2': 'language'
                },
                // 优化配置
                anonymize_ip: true,
                allow_google_signals: false,
                allow_ad_personalization_signals: false,
                // 自定义维度
                custom_map: {
                  'dimension1': 'course_name',
                  'dimension2': 'language',
                  'dimension3': 'module_name',
                  'dimension4': 'user_type'
                }
              });
            `}
          </Script>
        </>
      )}

      {/* Google Tag Manager */}
      {gtmId && (
        <>
          <Script id="google-tag-manager" strategy="afterInteractive">
            {`
              (function(w,d,s,l,i){w[l]=w[l]||[];w[l].push({'gtm.start':
              new Date().getTime(),event:'gtm.js'});var f=d.getElementsByTagName(s)[0],
              j=d.createElement(s),dl=l!='dataLayer'?'&l='+l:'';j.async=true;j.src=
              'https://www.googletagmanager.com/gtm.js?id='+i+dl;f.parentNode.insertBefore(j,f);
              })(window,document,'script','dataLayer','${gtmId}');
            `}
          </Script>
          <noscript>
            <iframe
              src={`https://www.googletagmanager.com/ns.html?id=${gtmId}`}
              height="0"
              width="0"
              style={{ display: 'none', visibility: 'hidden' }}
            />
          </noscript>
        </>
      )}
    </>
  )
}

// 页面浏览事件追踪
export function trackPageView(url: string, title: string) {
  if (typeof window !== 'undefined' && window.gtag) {
    window.gtag('config', process.env.NEXT_PUBLIC_GA_ID || 'G-F8EL781P96', {
      page_title: title,
      page_location: url,
    })
  }
}

// 自定义事件追踪
export function trackEvent(eventName: string, parameters: Record<string, unknown> = {}) {
  if (typeof window !== 'undefined' && window.gtag) {
    window.gtag('event', eventName, parameters)
    // 开发环境下的调试信息
    if (process.env.NODE_ENV === 'development') {
      console.log('GA Event:', eventName, parameters)
    }
  }
}

// 课程学习事件追踪
export function trackCourseProgress(courseName: string, moduleName: string, progress: number) {
  trackEvent('course_progress', {
    course_name: courseName,
    module_name: moduleName,
    progress_percentage: progress,
    // 自定义维度
    dimension1: courseName,
    dimension3: moduleName,
  })
}

// 代码运行事件追踪
export function trackCodeExecution(language: string, success: boolean) {
  trackEvent('code_execution', {
    programming_language: language,
    execution_success: success,
    // 自定义维度
    dimension2: language,
  })
}

// 搜索事件追踪
export function trackSearch(query: string, resultsCount: number) {
  trackEvent('search', {
    search_term: query,
    results_count: resultsCount,
  })
}

// 语言切换事件追踪
export function trackLanguageSwitch(fromLang: string, toLang: string) {
  trackEvent('language_switch', {
    from_language: fromLang,
    to_language: toLang,
    dimension2: toLang,
  })
}

// 编辑器使用事件追踪
export function trackEditorUsage(editorType: string, action: string) {
  trackEvent('editor_usage', {
    editor_type: editorType,
    action: action,
  })
}

declare global {
  interface Window {
    gtag: (...args: unknown[]) => void
  }
} 