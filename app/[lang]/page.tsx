import { InteractiveCodeComparison } from '@/components/interactive-code-comparison';
import { courseStructuredData } from '@/lib/seo-structured-data';
import { getTranslations, type SupportedLanguage } from '@/messages';
import Link from 'next/link';
import { ArrowRight, Code, Sparkles, Zap, Target, Users, BookOpen, Rocket, CheckCircle, Star, Play } from 'lucide-react';
import { Header } from '@/components/header';
import { Metadata } from 'next';
import { notFound } from 'next/navigation';

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

  const courseStructuredDataList = courses.map(course =>
    courseStructuredData({
      name: course.title,
      description: course.description,
      url: `https://langshift.dev/${supportedLang}/docs/${course.name}`,
      provider: 'LangShift.dev',
      courseMode: 'online',
      educationalLevel: 'intermediate',
    })
  );

  return (
    <div className="min-h-screen bg-gradient-to-br from-slate-900 via-slate-800 to-slate-900">
      <Header lang={supportedLang} />
      
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
                "url": `https://langshift.dev/${supportedLang}/docs/${course.name}`,
                "provider": {
                  "@type": "Organization",
                  "name": "LangShift.dev"
                }
              }
            }))
          })
        }}
      />

      {/* Hero Section */}
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
                href={`/${supportedLang}/docs/js2py`}
                className="group inline-flex items-center px-8 py-4 bg-gradient-to-r from-blue-500 to-purple-600 text-white font-semibold rounded-xl hover:from-blue-600 hover:to-purple-700 transition-all duration-300 transform hover:scale-105 shadow-lg hover:shadow-xl"
              >
                <Play className="w-5 h-5 mr-2" />
                {t.home.hero.cta}
                <ArrowRight className="w-5 h-5 ml-2 transform group-hover:translate-x-1 transition-transform" />
              </Link>
              <Link
                href={`/${supportedLang}/docs`}
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

      {/* 代码对比演示区域 */}
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
          
          <div className="bg-gradient-to-br from-slate-800/50 to-slate-900/50 backdrop-blur-sm border border-slate-700/50 rounded-3xl p-8 shadow-2xl">
            <InteractiveCodeComparison lang={supportedLang} />
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
            <p className="text-xl text-slate-400 max-w-3xl mx-auto mb-4">
              {t.home.learningPath.subtitle}
            </p>
            <p className="text-lg text-slate-500 max-w-4xl mx-auto">
              {t.home.learningPath.description}
            </p>
          </div>

          <div className="grid md:grid-cols-3 gap-8 max-w-6xl mx-auto mb-16">
            {t.home.learningPath.modules.map((phase, phaseIndex) => (
              <div key={phaseIndex} className="relative group">
                <div className="absolute inset-0 bg-gradient-to-br from-slate-800/50 to-slate-900/50 rounded-2xl blur-xl opacity-0 group-hover:opacity-100 transition-opacity duration-500"></div>
                <div className="relative bg-slate-800/30 backdrop-blur-sm border border-slate-700/50 rounded-2xl p-8 hover:scale-105 transition-all duration-300 h-full">
                  <div className={`w-16 h-16 bg-gradient-to-br ${phaseIndex === 0 ? 'from-blue-500 to-cyan-500' : phaseIndex === 1 ? 'from-green-500 to-emerald-500' : 'from-purple-500 to-pink-500'} rounded-2xl flex items-center justify-center mb-6 mx-auto shadow-lg`}>
                    <span className="text-2xl font-bold text-white">{phaseIndex + 1}</span>
                  </div>
                  <h3 className="text-2xl font-bold text-white mb-4 text-center">
                    {phase.phase}
                  </h3>
                  <p className="text-slate-400 text-center mb-6">
                    {phase.description}
                  </p>
                  <div className="text-center mb-6">
                    <span className="inline-flex items-center px-3 py-1 rounded-full text-sm font-medium bg-slate-700/50 text-slate-300">
                      ⏱️ {phase.duration}
                    </span>
                  </div>
                  <div className="space-y-4">
                    {phase.items.map((item, itemIndex) => (
                      <div key={itemIndex} className="group/item">
                        <div className="flex items-start mb-3">
                          <div className={`w-6 h-6 bg-gradient-to-br ${phaseIndex === 0 ? 'from-blue-500 to-cyan-500' : phaseIndex === 1 ? 'from-green-500 to-emerald-500' : 'from-purple-500 to-pink-500'} rounded-full flex items-center justify-center mr-4 mt-1 flex-shrink-0 shadow-sm`}>
                            <CheckCircle className="w-3 h-3 text-white" />
                          </div>
                          <div className="flex-1">
                            <h4 className="text-white font-semibold mb-1 group-hover/item:text-blue-400 transition-colors">
                              {item.title}
                            </h4>
                            <p className="text-slate-400 text-sm mb-2">
                              {item.description}
                            </p>
                            <div className="flex flex-wrap gap-1">
                              {item.focus.map((focus, focusIndex) => (
                                <span
                                  key={focusIndex}
                                  className="inline-block px-2 py-1 rounded text-xs bg-slate-700/50 text-slate-300"
                                >
                                  {focus}
                                </span>
                              ))}
                            </div>
                          </div>
                        </div>
                      </div>
                    ))}
                  </div>
                </div>
              </div>
            ))}
          </div>

          {/* 语言特定功能展示 */}
          <div className="max-w-6xl mx-auto">
            <div className="text-center mb-12">
              <h3 className="text-3xl md:text-4xl font-bold text-white mb-4">
                语言特定优化
              </h3>
              <p className="text-lg text-slate-400 max-w-2xl mx-auto">
                每种语言转换都有其独特的优化重点和学习特色
              </p>
            </div>
            <div className="grid md:grid-cols-2 gap-8">
              {Object.entries(t.home.learningPath.languageSpecificFeatures).map(([key, feature], index) => (
                <div key={key} className="relative group">
                  <div className="absolute inset-0 bg-gradient-to-br from-slate-800/50 to-slate-900/50 rounded-2xl blur-xl opacity-0 group-hover:opacity-100 transition-opacity duration-500"></div>
                  <div className="relative bg-slate-800/30 backdrop-blur-sm border border-slate-700/50 rounded-2xl p-8 hover:scale-105 transition-all duration-300">
                    <div className={`w-16 h-16 bg-gradient-to-br ${index === 0 ? 'from-green-500 to-emerald-500' : 'from-orange-500 to-red-500'} rounded-2xl flex items-center justify-center mb-6 shadow-lg`}>
                      <span className="text-2xl">{index === 0 ? '🐍' : '🦀'}</span>
                    </div>
                    <h4 className="text-2xl font-bold text-white mb-6">
                      {feature.title}
                    </h4>
                    <div className="space-y-4">
                      {feature.highlights.map((highlight, highlightIndex) => (
                        <div key={highlightIndex} className="flex items-start">
                          <div className={`w-6 h-6 bg-gradient-to-br ${index === 0 ? 'from-green-500 to-emerald-500' : 'from-orange-500 to-red-500'} rounded-full flex items-center justify-center mr-4 mt-1 flex-shrink-0 shadow-sm`}>
                            <CheckCircle className="w-3 h-3 text-white" />
                          </div>
                          <span className="text-slate-300 leading-relaxed">{highlight}</span>
                        </div>
                      ))}
                    </div>
                  </div>
                </div>
              ))}
            </div>
          </div>

          {/* 学习提示 */}
          <div className="max-w-5xl mx-auto mt-16">
            <div className="relative">
              <div className="absolute inset-0 bg-gradient-to-br from-slate-800/50 to-slate-900/50 rounded-3xl blur-xl opacity-60"></div>
              <div className="relative bg-gradient-to-br from-slate-800/30 to-slate-900/30 backdrop-blur-sm border border-slate-700/50 rounded-3xl p-10">
                <div className="text-center mb-8">
                  <div className="w-16 h-16 bg-gradient-to-br from-blue-500 to-purple-600 rounded-2xl flex items-center justify-center mb-6 mx-auto shadow-lg">
                    <span className="text-2xl">💡</span>
                  </div>
                  <h4 className="text-2xl font-bold text-white mb-4">
                    学习提示
                  </h4>
                  <p className="text-slate-400 max-w-2xl mx-auto">
                    遵循这些建议，让你的学习之旅更加高效和愉快
                  </p>
                </div>
                <div className="grid md:grid-cols-2 gap-6">
                  {t.home.learningPath.learningTips.map((tip, index) => (
                    <div key={index} className="flex items-start group">
                      <div className="w-8 h-8 bg-gradient-to-br from-blue-500 to-purple-600 rounded-full flex items-center justify-center mr-4 mt-1 flex-shrink-0 shadow-sm group-hover:scale-110 transition-transform">
                        <span className="text-white text-sm font-bold">{index + 1}</span>
                      </div>
                      <div className="flex-1">
                        <span className="text-slate-300 leading-relaxed group-hover:text-white transition-colors">{tip}</span>
                      </div>
                    </div>
                  ))}
                </div>
              </div>
            </div>
          </div>
        </div>
      </div>

      {/* 课程选择区域 */}
      <div className="py-20">
        <div className="container mx-auto px-4">
          <div className="text-center mb-16">
            <h2 className="text-4xl md:text-5xl lg:text-6xl font-bold text-white mb-6">
              {t.home.coursesSection.title}
            </h2>
            <p className="text-xl text-slate-400 max-w-3xl mx-auto leading-relaxed">
              {t.home.coursesSection.subtitle}
            </p>
          </div>
          
          <div className="grid md:grid-cols-2 gap-8 max-w-6xl mx-auto">
            {courses.map((course) => (
              <Link
                key={course.name}
                href={`/${supportedLang}/docs/${course.name}`}
                className="group block"
              >
                <div className="relative bg-gradient-to-br from-slate-800/50 to-slate-900/50 backdrop-blur-sm border border-slate-700/50 rounded-3xl p-8 hover:from-slate-700/50 hover:to-slate-800/50 transition-all duration-500 transform hover:scale-105 shadow-2xl hover:shadow-3xl">
                  <div className="flex items-center justify-between mb-6">
                    <div className={`w-16 h-16 bg-gradient-to-br ${course.color} rounded-2xl flex items-center justify-center text-3xl shadow-lg`}>
                      {course.icon}
                    </div>
                    <ArrowRight className="w-6 h-6 text-slate-400 group-hover:text-white transition-colors duration-300 group-hover:translate-x-2" />
                  </div>
                  
                  <h3 className="text-2xl font-bold text-white mb-4">{course.title}</h3>
                  <p className="text-slate-400 mb-6 leading-relaxed">{course.description}</p>
                  
                  <div className="flex flex-wrap gap-2 mb-6">
                    {course.features.map((feature, index) => (
                      <span
                        key={index}
                        className={`inline-flex items-center px-3 py-1 rounded-full text-sm font-medium ${course.bgColor} ${course.borderColor} text-white/90`}
                      >
                        {feature}
                      </span>
                    ))}
                  </div>
                  
                  <div className="flex items-center justify-between text-sm text-slate-400">
                    <span>{course.duration}</span>
                    <span className={`px-3 py-1 rounded-full ${course.bgColor} ${course.borderColor} text-white/90 font-medium`}>
                      {course.level}
                    </span>
                  </div>
                </div>
              </Link>
            ))}
          </div>
        </div>
      </div>

      {/* CTA 区域 */}
      <div className="py-20">
        <div className="container mx-auto px-4 text-center">
          <div className="max-w-4xl mx-auto">
            <h2 className="text-4xl md:text-5xl lg:text-6xl font-bold text-white mb-8">
              {t.home.cta.title}
            </h2>
            <p className="text-xl text-slate-400 mb-10 leading-relaxed">
              {t.home.cta.subtitle}
            </p>
            <div className="flex flex-col sm:flex-row gap-4 justify-center">
              <Link
                href={`/${supportedLang}/docs/js2py`}
                className="group inline-flex items-center px-8 py-4 bg-gradient-to-r from-blue-500 to-purple-600 text-white font-semibold rounded-xl hover:from-blue-600 hover:to-purple-700 transition-all duration-300 transform hover:scale-105 shadow-lg"
              >
                <Play className="w-5 h-5 mr-2" />
                {t.home.cta.primary}
                <ArrowRight className="w-5 h-5 ml-2 transform group-hover:translate-x-1 transition-transform" />
              </Link>
              <Link
                href={`/${supportedLang}/docs`}
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