import { InteractiveCodeComparison } from '@/components/interactive-code-comparison';
import { SEOHead } from '@/components/seo-head';
import { courseStructuredData } from '@/lib/seo-structured-data';
import { getTranslations, type SupportedLanguage } from '@/messages';
import Link from 'next/link';
import { ArrowRight, Code, Sparkles, Zap, Target, Users, BookOpen, Rocket, CheckCircle, Star, Play } from 'lucide-react';
import { Header } from '@/components/header';
import { headers } from 'next/headers';
import { Metadata } from 'next';
import { redirect } from 'next/navigation';

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
      images: [
        {
          url: `${siteUrl}/og-image.png`,
          width: 1200,
          height: 630,
          alt: 'LangShift.dev - 编程语言转换学习平台',
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

export default async function HomePage() {
  // 检测用户语言并重定向到对应的语言页面
  const preferredLang = await getPreferredLanguage();
  if (preferredLang !== 'zh-cn') {
    redirect(`/${preferredLang}`);
  }

  const t = getTranslations('zh-cn'); // 使用简体中文作为默认

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

  const courseStructuredDataList = courses.map(course =>
    courseStructuredData({
      name: course.title,
      description: course.description,
      url: `https://langshift.dev/zh-cn/${course.name}`,
      provider: 'LangShift.dev',
      courseMode: 'online',
      educationalLevel: 'intermediate',
    })
  );

  return (
    <div className="min-h-screen bg-gradient-to-br from-slate-900 via-slate-800 to-slate-900">
      <Header lang="zh-cn" />
      
      {/* 结构化数据 */}
      <script
        type="application/ld+json"
        dangerouslySetInnerHTML={{
          __html: JSON.stringify({
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
                "url": `https://langshift.dev/zh-cn/${course.name}`,
                "provider": {
                  "@type": "Organization",
                  "name": "LangShift.dev"
                }
              }
            }))
          })
        }}
      />

      {/* 语言重定向脚本 - 客户端检测 */}
      <script
        dangerouslySetInnerHTML={{
          __html: `
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
          `
        }}
      />

      {/* Hero Section - 重新设计 */}
      <div className="relative overflow-hidden pt-16">
        {/* 动态背景 */}
        <div className="absolute inset-0">
          <div className="absolute inset-0 bg-gradient-to-br from-blue-600/10 via-purple-600/10 to-pink-600/10"></div>
          <div className="absolute top-0 left-0 w-full h-full">
            <div className="absolute top-20 left-10 w-72 h-72 bg-blue-500/20 rounded-full blur-3xl animate-pulse"></div>
            <div className="absolute top-40 right-20 w-96 h-96 bg-purple-500/20 rounded-full blur-3xl animate-pulse delay-1000"></div>
            <div className="absolute bottom-20 left-1/3 w-80 h-80 bg-pink-500/20 rounded-full blur-3xl animate-pulse delay-2000"></div>
          </div>
        </div>

        <div className="container mx-auto px-4 py-16 lg:py-24 relative z-10">
          <div className="text-center max-w-6xl mx-auto">
            {/* 主标题 */}
            <div className="mb-8">
              <h1 className="text-5xl md:text-7xl lg:text-8xl font-bold text-white mb-6 leading-tight">
                <span className="bg-gradient-to-r from-blue-400 via-purple-500 to-pink-500 bg-clip-text text-transparent">
                  {t.home.hero.title}
                </span>
              </h1>
              <p className="text-2xl md:text-3xl lg:text-4xl text-slate-300 mb-6 font-medium">
                {t.home.hero.subtitle}
              </p>
              <p className="text-lg md:text-xl text-slate-400 mb-8 max-w-4xl mx-auto leading-relaxed">
                {t.home.hero.description}
              </p>
            </div>

            {/* 核心特性展示 */}
            <div className="grid grid-cols-1 md:grid-cols-3 gap-6 mb-12 max-w-4xl mx-auto">
              <div className="bg-slate-800/30 backdrop-blur-sm border border-slate-700/50 rounded-2xl p-6 hover:scale-105 transition-all duration-300">
                <div className="w-12 h-12 bg-gradient-to-br from-blue-500 to-cyan-500 rounded-xl flex items-center justify-center mb-4 mx-auto">
                  <Code className="w-6 h-6 text-white" />
                </div>
                <h3 className="text-lg font-semibold text-white mb-2">{t.home.hero.coreFeatures.codeComparison.title}</h3>
                <p className="text-slate-400 text-sm">{t.home.hero.coreFeatures.codeComparison.description}</p>
              </div>
              <div className="bg-slate-800/30 backdrop-blur-sm border border-slate-700/50 rounded-2xl p-6 hover:scale-105 transition-all duration-300">
                <div className="w-12 h-12 bg-gradient-to-br from-green-500 to-emerald-500 rounded-xl flex items-center justify-center mb-4 mx-auto">
                  <Target className="w-6 h-6 text-white" />
                </div>
                <h3 className="text-lg font-semibold text-white mb-2">{t.home.hero.coreFeatures.progressiveLearning.title}</h3>
                <p className="text-slate-400 text-sm">{t.home.hero.coreFeatures.progressiveLearning.description}</p>
              </div>
              <div className="bg-slate-800/30 backdrop-blur-sm border border-slate-700/50 rounded-2xl p-6 hover:scale-105 transition-all duration-300">
                <div className="w-12 h-12 bg-gradient-to-br from-purple-500 to-pink-500 rounded-xl flex items-center justify-center mb-4 mx-auto">
                  <Zap className="w-6 h-6 text-white" />
                </div>
                <h3 className="text-lg font-semibold text-white mb-2">{t.home.hero.coreFeatures.realProjects.title}</h3>
                <p className="text-slate-400 text-sm">{t.home.hero.coreFeatures.realProjects.description}</p>
              </div>
            </div>

            {/* CTA 按钮 */}
            <div className="flex flex-col sm:flex-row gap-4 justify-center mb-16">
              <Link
                href={`/zh-cn/js2py`}
                className="group inline-flex items-center px-8 py-4 bg-gradient-to-r from-blue-500 to-purple-600 text-white font-semibold rounded-xl hover:from-blue-600 hover:to-purple-700 transition-all duration-300 transform hover:scale-105 shadow-lg hover:shadow-xl"
              >
                <Play className="w-5 h-5 mr-2" />
                {t.home.hero.cta}
                <ArrowRight className="w-5 h-5 ml-2 transform group-hover:translate-x-1 transition-transform" />
              </Link>
              <Link
                href={`/zh-cn/docs`}
                className="inline-flex items-center px-8 py-4 border border-slate-600 text-slate-300 font-semibold rounded-xl hover:bg-slate-800 hover:text-white transition-all duration-300 backdrop-blur-sm"
              >
                <BookOpen className="w-5 h-5 mr-2" />
                {t.home.hero.viewLearningPath}
              </Link>
            </div>

            {/* 统计数据 */}
            <div className="grid grid-cols-2 md:grid-cols-4 gap-8 max-w-4xl mx-auto">
              <div className="text-center group">
                <div className="text-3xl md:text-4xl font-bold text-white mb-2 group-hover:text-blue-400 transition-colors">
                  {t.home.hero.stats.learners}
                </div>
                <div className="text-slate-400 text-sm">{t.home.hero.statsLabels.learners}</div>
              </div>
              <div className="text-center group">
                <div className="text-3xl md:text-4xl font-bold text-white mb-2 group-hover:text-green-400 transition-colors">
                  {t.home.hero.stats.languages}
                </div>
                <div className="text-slate-400 text-sm">{t.home.hero.statsLabels.languages}</div>
              </div>
              <div className="text-center group">
                <div className="text-3xl md:text-4xl font-bold text-white mb-2 group-hover:text-purple-400 transition-colors">
                  {t.home.hero.stats.modules}
                </div>
                <div className="text-slate-400 text-sm">{t.home.hero.statsLabels.modules}</div>
              </div>
              <div className="text-center group">
                <div className="text-3xl md:text-4xl font-bold text-white mb-2 group-hover:text-pink-400 transition-colors">
                  {t.home.hero.stats.projects}
                </div>
                <div className="text-slate-400 text-sm">{t.home.hero.statsLabels.projects}</div>
              </div>
            </div>
          </div>
        </div>
      </div>

      {/* 代码对比演示区域 - 重点突出 */}
      <div className="relative py-20">
        <div className="container mx-auto px-4">
          <div className="text-center mb-16">
            <div className="inline-flex items-center gap-2 bg-gradient-to-r from-blue-500/20 to-purple-500/20 border border-blue-500/30 rounded-full px-6 py-2 mb-6">
              <Sparkles className="w-4 h-4 text-blue-400" />
              <span className="text-blue-400 text-sm font-medium">{t.home.codeDemo.interactiveExperience}</span>
            </div>
            <h2 className="text-4xl md:text-5xl lg:text-6xl font-bold text-white mb-6">
              {t.home.codeDemo.experienceTitle}
            </h2>
            <p className="text-xl text-slate-400 max-w-3xl mx-auto leading-relaxed">
              {t.home.codeDemo.experienceDescription}
            </p>
          </div>
          
          {/* 代码对比组件 */}
          <div className="max-w-7xl mx-auto">
            <div className="relative">
              {/* 装饰性元素 */}
              <div className="absolute -top-4 -left-4 w-8 h-8 bg-gradient-to-br from-blue-500 to-purple-600 rounded-full opacity-60"></div>
              <div className="absolute -bottom-4 -right-4 w-6 h-6 bg-gradient-to-br from-green-500 to-emerald-600 rounded-full opacity-60"></div>
              
              <div className="bg-gradient-to-br from-slate-800/50 to-slate-900/50 backdrop-blur-sm border border-slate-700/50 rounded-3xl p-8 shadow-2xl">
                <InteractiveCodeComparison lang="zh-cn" />
              </div>
            </div>
          </div>
        </div>
      </div>

      {/* 学习路径展示 */}
      <div className="py-20 bg-gradient-to-br from-slate-800/30 to-slate-900/30">
        <div className="container mx-auto px-4">
          <div className="text-center mb-16">
            <h2 className="text-4xl md:text-5xl font-bold text-white mb-6">
              {t.home.learningPath.title}
            </h2>
            <p className="text-xl text-slate-400 max-w-3xl mx-auto">
              {t.home.learningPath.subtitle}
            </p>
          </div>

          <div className="grid md:grid-cols-3 gap-8 max-w-6xl mx-auto">
            {t.home.learningPath.modules.map((phase, phaseIndex) => (
              <div key={phaseIndex} className="relative group">
                <div className="absolute inset-0 bg-gradient-to-br from-slate-800/50 to-slate-900/50 rounded-2xl blur-xl opacity-0 group-hover:opacity-100 transition-opacity duration-500"></div>
                <div className="relative bg-slate-800/30 backdrop-blur-sm border border-slate-700/50 rounded-2xl p-8 hover:scale-105 transition-all duration-300">
                  <div className={`w-16 h-16 bg-gradient-to-br ${phaseIndex === 0 ? 'from-blue-500 to-cyan-500' : phaseIndex === 1 ? 'from-green-500 to-emerald-500' : 'from-purple-500 to-pink-500'} rounded-2xl flex items-center justify-center mb-6 mx-auto shadow-lg`}>
                    <span className="text-2xl font-bold text-white">{phaseIndex + 1}</span>
                  </div>
                  <h3 className="text-2xl font-bold text-white mb-6 text-center">
                    {phase.phase}
                  </h3>
                  <div className="space-y-4">
                    {phase.items.map((item, itemIndex) => (
                      <div key={itemIndex} className="flex items-start">
                        <div className={`w-6 h-6 bg-gradient-to-br ${phaseIndex === 0 ? 'from-blue-500 to-cyan-500' : phaseIndex === 1 ? 'from-green-500 to-emerald-500' : 'from-purple-500 to-pink-500'} rounded-full flex items-center justify-center mr-4 mt-1 flex-shrink-0 shadow-sm`}>
                          <CheckCircle className="w-3 h-3 text-white" />
                        </div>
                        <p className="text-slate-300 leading-relaxed">{item}</p>
                      </div>
                    ))}
                  </div>
                </div>
              </div>
            ))}
          </div>
        </div>
      </div>

      {/* 课程选择区域 */}
      <div className="py-20">
        <div className="container mx-auto px-4">
          <div className="text-center mb-16">
            <h2 className="text-4xl md:text-5xl font-bold text-white mb-6">
              {t.home.coursesSection.title}
            </h2>
            <p className="text-xl text-slate-400 max-w-3xl mx-auto">
              {t.home.coursesSection.subtitle}
            </p>
          </div>

          <div className="grid lg:grid-cols-2 gap-8 max-w-6xl mx-auto">
            {courses.map((course) => (
              <Link
                key={course.name}
                href={`/zh-cn/${course.name}`}
                className="group block"
              >
                <div className={`relative overflow-hidden ${course.bgColor} ${course.borderColor} border backdrop-blur-sm rounded-3xl p-8 hover:scale-105 transition-all duration-500`}>
                  {/* 背景渐变 */}
                  <div className={`absolute inset-0 bg-gradient-to-br ${course.gradient} opacity-0 group-hover:opacity-100 transition-opacity duration-500`}></div>
                  
                  {/* 装饰性元素 */}
                  <div className="absolute top-4 right-4 w-20 h-20 bg-gradient-to-br from-white/5 to-transparent rounded-full"></div>
                  <div className="absolute bottom-4 left-4 w-16 h-16 bg-gradient-to-br from-white/5 to-transparent rounded-full"></div>

                  <div className="relative z-10">
                    <div className="flex items-center mb-6">
                      <div className={`w-20 h-20 bg-gradient-to-br ${course.color} rounded-2xl flex items-center justify-center text-4xl mr-6 shadow-xl`}>
                        {course.icon}
                      </div>
                      <div>
                        <h3 className="text-3xl font-bold text-white group-hover:text-blue-400 transition-colors">
                          {course.title}
                        </h3>
                        <div className="flex items-center gap-4 mt-3">
                          <span className="text-sm text-slate-400">{course.duration}</span>
                          <span className="px-4 py-1 bg-slate-700/50 rounded-full text-sm text-slate-300 backdrop-blur-sm">
                            {course.level}
                          </span>
                        </div>
                      </div>
                    </div>

                    <p className="text-slate-300 text-lg leading-relaxed mb-8">
                      {course.description}
                    </p>

                    {/* 特性标签 */}
                    <div className="flex flex-wrap gap-3 mb-8">
                      {course.features.map((feature, index) => (
                        <span
                          key={index}
                          className="px-4 py-2 bg-slate-800/50 backdrop-blur-sm rounded-full text-sm text-slate-300 border border-slate-700/50"
                        >
                          {feature}
                        </span>
                      ))}
                    </div>

                    <div className="flex items-center text-blue-400 group-hover:text-blue-300 transition-colors">
                      <span className="mr-2 font-semibold text-lg">
                        {t.home.courses.startLearning}
                      </span>
                      <ArrowRight className="w-6 h-6 transform group-hover:translate-x-2 transition-transform" />
                    </div>
                  </div>
                </div>
              </Link>
            ))}
          </div>
        </div>
      </div>

      {/* 核心特性展示 */}
      <div className="py-20 bg-gradient-to-br from-slate-800/30 to-slate-900/30">
        <div className="container mx-auto px-4">
          <div className="text-center mb-16">
            <h2 className="text-4xl md:text-5xl font-bold text-white mb-6">
              {t.home.features.title}
            </h2>
            <p className="text-xl text-slate-400 max-w-3xl mx-auto">
              {t.home.features.subtitle}
            </p>
          </div>

          <div className="grid md:grid-cols-2 lg:grid-cols-3 gap-8 max-w-7xl mx-auto">
            {[
              {
                icon: <Code className="w-8 h-8" />,
                title: t.home.features.codeEditor.title,
                description: t.home.features.codeEditor.description,
                color: "from-blue-500 to-cyan-500",
                bgColor: "bg-blue-500/10",
              },
              {
                icon: <Target className="w-8 h-8" />,
                title: t.home.features.syntaxComparison.title,
                description: t.home.features.syntaxComparison.description,
                color: "from-green-500 to-emerald-500",
                bgColor: "bg-green-500/10",
              },
              {
                icon: <BookOpen className="w-8 h-8" />,
                title: t.home.features.learningPath.title,
                description: t.home.features.learningPath.description,
                color: "from-purple-500 to-pink-500",
                bgColor: "bg-purple-500/10",
              },
              {
                icon: <Zap className="w-8 h-8" />,
                title: t.home.features.performance.title,
                description: t.home.features.performance.description,
                color: "from-orange-500 to-red-500",
                bgColor: "bg-orange-500/10",
              },
              {
                icon: <Rocket className="w-8 h-8" />,
                title: t.home.features.projects.title,
                description: t.home.features.projects.description,
                color: "from-indigo-500 to-purple-500",
                bgColor: "bg-indigo-500/10",
              },
              {
                icon: <Users className="w-8 h-8" />,
                title: t.home.features.community.title,
                description: t.home.features.community.description,
                color: "from-teal-500 to-cyan-500",
                bgColor: "bg-teal-500/10",
              },
            ].map((feature, index) => (
              <div key={index} className={`${feature.bgColor} border border-slate-700/50 rounded-2xl p-8 hover:scale-105 transition-all duration-300 backdrop-blur-sm group`}>
                <div className={`w-16 h-16 bg-gradient-to-br ${feature.color} rounded-2xl flex items-center justify-center mb-6 shadow-lg group-hover:shadow-xl transition-shadow`}>
                  <div className="text-white">
                    {feature.icon}
                  </div>
                </div>
                <h3 className="text-xl font-semibold text-white mb-4">
                  {feature.title}
                </h3>
                <p className="text-slate-400 leading-relaxed">
                  {feature.description}
                </p>
              </div>
            ))}
          </div>
        </div>
      </div>

      {/* 用户评价 */}
      <div className="py-20">
        <div className="container mx-auto px-4">
          <div className="text-center mb-16">
            <h2 className="text-4xl md:text-5xl font-bold text-white mb-6">
              {t.home.testimonials.title}
            </h2>
            <p className="text-xl text-slate-400 max-w-3xl mx-auto">
              {t.home.testimonials.subtitle}
            </p>
          </div>

          <div className="grid md:grid-cols-3 gap-8 max-w-6xl mx-auto">
            {t.home.testimonials.items.map((testimonial, index) => (
              <div key={index} className="bg-slate-800/30 backdrop-blur-sm border border-slate-700/50 rounded-2xl p-8 hover:scale-105 transition-all duration-300">
                <div className="flex items-center mb-6">
                  <div className="w-14 h-14 bg-gradient-to-br from-blue-500 to-purple-600 rounded-full flex items-center justify-center text-2xl mr-4">
                    {testimonial.avatar}
                  </div>
                  <div>
                    <h4 className="text-lg font-semibold text-white">{testimonial.name}</h4>
                    <p className="text-slate-400 text-sm">{testimonial.role}</p>
                    <div className="flex items-center gap-1 mt-1">
                      {[...Array(5)].map((_, i) => (
                        <Star key={i} className="w-4 h-4 fill-yellow-400 text-yellow-400" />
                      ))}
                    </div>
                  </div>
                </div>
                <p className="text-slate-300 leading-relaxed italic">
                  &ldquo;{testimonial.content}&rdquo;
                </p>
              </div>
            ))}
          </div>
        </div>
      </div>

      {/* 最终 CTA */}
      <div className="py-20">
        <div className="container mx-auto px-4">
          <div className="bg-gradient-to-r from-blue-600/20 to-purple-600/20 border border-blue-500/30 rounded-3xl p-12 text-center backdrop-blur-sm">
            <h2 className="text-4xl md:text-5xl font-bold text-white mb-6">
              {t.home.cta.title}
            </h2>
            <p className="text-xl text-slate-300 mb-8 max-w-2xl mx-auto">
              {t.home.cta.subtitle}
            </p>
            <div className="flex flex-col sm:flex-row gap-4 justify-center">
              <Link
                href={`/zh-cn/js2py`}
                className="group inline-flex items-center px-8 py-4 bg-gradient-to-r from-blue-500 to-purple-600 text-white font-semibold rounded-xl hover:from-blue-600 hover:to-purple-700 transition-all duration-300 transform hover:scale-105 shadow-lg"
              >
                <Play className="w-5 h-5 mr-2" />
                {t.home.cta.primary}
                <ArrowRight className="w-5 h-5 ml-2 transform group-hover:translate-x-1 transition-transform" />
              </Link>
              <Link
                href={`/zh-cn/docs`}
                className="inline-flex items-center px-8 py-4 border border-slate-600 text-slate-300 font-semibold rounded-xl hover:bg-slate-800 hover:text-white transition-all duration-300"
              >
                <BookOpen className="w-5 h-5 mr-2" />
                {t.home.cta.secondary}
              </Link>
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}