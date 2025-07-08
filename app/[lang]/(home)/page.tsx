import { SEOHead } from '@/components/seo-head';
import { courseStructuredData } from '@/lib/seo-structured-data';
import { getTranslations, type SupportedLanguage } from '@/messages';
import Link from 'next/link';

interface HomePageProps {
  params: Promise<{ lang: string }>;
}

export default async function HomePage({ params }: HomePageProps) {
  const lang = (await params).lang as SupportedLanguage;
  const t = getTranslations(lang);
  
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
    },
  ];

  const courseStructuredDataList = courses.map(course => 
    courseStructuredData({
      name: course.title,
      description: course.description,
      url: `https://langshift.dev/${lang}/${course.name}`,
      provider: 'LangShift.dev',
      courseMode: 'online',
      educationalLevel: 'intermediate',
    })
  );

  return (
    <div className="min-h-screen bg-gradient-to-br from-slate-900 via-slate-800 to-slate-900">
      <SEOHead
        title={t.home.seo.title}
        description={t.home.seo.description}
        keywords={t.home.seo.keywords}
        ogType="website"
        structuredData={courseStructuredDataList}
      />
      
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
                "url": `https://langshift.dev/${lang}/${course.name}`,
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
      <div className="relative overflow-hidden">
        {/* Background Pattern */}
        <div 
          className="absolute inset-0 opacity-20"
          style={{
            backgroundImage: `url("data:image/svg+xml,%3Csvg width='60' height='60' viewBox='0 0 60 60' xmlns='http://www.w3.org/2000/svg'%3E%3Cg fill='none' fill-rule='evenodd'%3E%3Cg fill='%239C92AC' fill-opacity='0.05'%3E%3Ccircle cx='30' cy='30' r='2'/%3E%3C/g%3E%3C/g%3E%3C/svg%3E")`
          }}
        ></div>
        
        <div className="container mx-auto px-4 py-20">
          <div className="text-center max-w-4xl mx-auto">
            <h1 className="text-6xl md:text-8xl font-bold text-white mb-6 leading-tight">
              <span className="bg-gradient-to-r from-blue-400 via-purple-500 to-pink-500 bg-clip-text text-transparent">
                {t.home.hero.title}
              </span>
            </h1>
            <p className="text-2xl md:text-3xl text-slate-300 mb-8 font-medium">
              {t.home.hero.subtitle}
            </p>
            <p className="text-lg md:text-xl text-slate-400 mb-12 max-w-3xl mx-auto leading-relaxed">
              {t.home.hero.description}
            </p>
            
            {/* CTA Button */}
            <div className="flex flex-col sm:flex-row gap-4 justify-center mb-16">
              <Link
                href={`/${lang}/js2py`}
                className="inline-flex items-center px-8 py-4 bg-gradient-to-r from-blue-500 to-purple-600 text-white font-semibold rounded-xl hover:from-blue-600 hover:to-purple-700 transition-all duration-300 transform hover:scale-105 shadow-lg hover:shadow-xl"
              >
                {t.home.hero.cta}
                <svg className="w-5 h-5 ml-2" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                  <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M13 7l5 5m0 0l-5 5m5-5H6" />
                </svg>
              </Link>
            </div>

            {/* Stats */}
            <div className="grid grid-cols-2 md:grid-cols-4 gap-8 max-w-3xl mx-auto">
              <div className="text-center">
                <div className="text-3xl md:text-4xl font-bold text-white mb-2">
                  {t.home.hero.stats.learners}
                </div>
                <div className="text-slate-400 text-sm">活跃学习者</div>
              </div>
              <div className="text-center">
                <div className="text-3xl md:text-4xl font-bold text-white mb-2">
                  {t.home.hero.stats.languages}
                </div>
                <div className="text-slate-400 text-sm">支持语言</div>
              </div>
              <div className="text-center">
                <div className="text-3xl md:text-4xl font-bold text-white mb-2">
                  {t.home.hero.stats.modules}
                </div>
                <div className="text-slate-400 text-sm">学习模块</div>
              </div>
              <div className="text-center">
                <div className="text-3xl md:text-4xl font-bold text-white mb-2">
                  {t.home.hero.stats.projects}
                </div>
                <div className="text-slate-400 text-sm">实战项目</div>
              </div>
            </div>
          </div>
        </div>
      </div>

      {/* Courses Section */}
      <div className="container mx-auto px-4 py-20">
        <div className="text-center mb-16">
          <h2 className="text-4xl md:text-5xl font-bold text-white mb-6">
            选择你的学习路径
          </h2>
          <p className="text-xl text-slate-400 max-w-2xl mx-auto">
            从你熟悉的语言开始，快速掌握新语言的精髓
          </p>
        </div>

        <div className="grid lg:grid-cols-2 gap-8 max-w-6xl mx-auto">
          {courses.map((course) => (
            <Link
              key={course.name}
              href={`/${lang}/${course.name}`}
              className="group block"
            >
              <div className={`${course.bgColor} ${course.borderColor} border backdrop-blur-sm rounded-2xl p-8 hover:scale-105 transition-all duration-300 relative overflow-hidden`}>
                {/* Background Gradient */}
                <div className={`absolute inset-0 bg-gradient-to-br ${course.color} opacity-5 group-hover:opacity-10 transition-opacity duration-300`}></div>
                
                <div className="relative z-10">
                  <div className="flex items-center mb-6">
                    <div className={`w-16 h-16 bg-gradient-to-br ${course.color} rounded-2xl flex items-center justify-center text-3xl mr-6 shadow-lg`}>
                      {course.icon}
                    </div>
                    <div>
                      <h3 className="text-2xl font-bold text-white group-hover:text-blue-400 transition-colors">
                        {course.title}
                      </h3>
                      <div className="flex items-center gap-4 mt-2">
                        <span className="text-sm text-slate-400">{course.duration}</span>
                        <span className="px-3 py-1 bg-slate-700/50 rounded-full text-xs text-slate-300">
                          {course.level}
                        </span>
                      </div>
                    </div>
                  </div>
                  
                  <p className="text-slate-300 text-lg leading-relaxed mb-6">
                    {course.description}
                  </p>
                  
                  {/* Features */}
                  <div className="flex flex-wrap gap-2 mb-6">
                    {course.features.map((feature, index) => (
                      <span
                        key={index}
                        className="px-3 py-1 bg-slate-800/50 rounded-full text-sm text-slate-300"
                      >
                        {feature}
                      </span>
                    ))}
                  </div>
                  
                  <div className="flex items-center text-blue-400 group-hover:text-blue-300 transition-colors">
                    <span className="mr-2 font-medium">
                      {t.home.courses.startLearning}
                    </span>
                    <svg className="w-5 h-5 transform group-hover:translate-x-1 transition-transform" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                      <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M13 7l5 5m0 0l-5 5m5-5H6" />
                    </svg>
                  </div>
                </div>
              </div>
            </Link>
          ))}
        </div>
      </div>

      {/* Features Section */}
      <div className="container mx-auto px-4 py-20">
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
              icon: t.home.features.codeEditor.title,
              title: t.home.features.codeEditor.title,
              description: t.home.features.codeEditor.description,
              color: 'from-blue-500 to-cyan-500',
              bgColor: 'bg-blue-500/10',
            },
            {
              icon: t.home.features.syntaxComparison.title,
              title: t.home.features.syntaxComparison.title,
              description: t.home.features.syntaxComparison.description,
              color: 'from-green-500 to-emerald-500',
              bgColor: 'bg-green-500/10',
            },
            {
              icon: t.home.features.learningPath.title,
              title: t.home.features.learningPath.title,
              description: t.home.features.learningPath.description,
              color: 'from-purple-500 to-pink-500',
              bgColor: 'bg-purple-500/10',
            },
            {
              icon: t.home.features.performance.title,
              title: t.home.features.performance.title,
              description: t.home.features.performance.description,
              color: 'from-orange-500 to-red-500',
              bgColor: 'bg-orange-500/10',
            },
            {
              icon: t.home.features.projects.title,
              title: t.home.features.projects.title,
              description: t.home.features.projects.description,
              color: 'from-indigo-500 to-purple-500',
              bgColor: 'bg-indigo-500/10',
            },
            {
              icon: t.home.features.community.title,
              title: t.home.features.community.title,
              description: t.home.features.community.description,
              color: 'from-teal-500 to-cyan-500',
              bgColor: 'bg-teal-500/10',
            },
          ].map((feature, index) => (
            <div key={index} className={`${feature.bgColor} border border-slate-700/50 rounded-2xl p-8 hover:scale-105 transition-all duration-300`}>
              <div className={`w-16 h-16 bg-gradient-to-br ${feature.color} rounded-2xl flex items-center justify-center mb-6 shadow-lg`}>
                <svg className="w-8 h-8 text-white" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                  <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M9 12l2 2 4-4m6 2a9 9 0 11-18 0 9 9 0 0118 0z" />
                </svg>
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

      {/* Learning Path Section */}
      <div className="container mx-auto px-4 py-20">
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
            <div key={phaseIndex} className="bg-slate-800/30 border border-slate-700/50 rounded-2xl p-8">
              <h3 className="text-2xl font-bold text-white mb-6 text-center">
                {phase.phase}
              </h3>
              <div className="space-y-4">
                {phase.items.map((item, itemIndex) => (
                  <div key={itemIndex} className="flex items-start">
                    <div className="w-6 h-6 bg-blue-500 rounded-full flex items-center justify-center mr-4 mt-1 flex-shrink-0">
                      <span className="text-xs text-white font-bold">{itemIndex + 1}</span>
                    </div>
                    <p className="text-slate-300 leading-relaxed">{item}</p>
                  </div>
                ))}
              </div>
            </div>
          ))}
        </div>
      </div>

      {/* Testimonials Section */}
      <div className="container mx-auto px-4 py-20">
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
            <div key={index} className="bg-slate-800/30 border border-slate-700/50 rounded-2xl p-8 hover:scale-105 transition-all duration-300">
              <div className="flex items-center mb-6">
                <div className="w-12 h-12 bg-gradient-to-br from-blue-500 to-purple-600 rounded-full flex items-center justify-center text-2xl mr-4">
                  {testimonial.avatar}
                </div>
                <div>
                  <h4 className="text-lg font-semibold text-white">{testimonial.name}</h4>
                  <p className="text-slate-400 text-sm">{testimonial.role}</p>
                </div>
              </div>
              <p className="text-slate-300 leading-relaxed italic">
                "{testimonial.content}"
              </p>
            </div>
          ))}
        </div>
      </div>

      {/* CTA Section */}
      <div className="container mx-auto px-4 py-20">
        <div className="bg-gradient-to-r from-blue-600/20 to-purple-600/20 border border-blue-500/30 rounded-3xl p-12 text-center">
          <h2 className="text-4xl md:text-5xl font-bold text-white mb-6">
            {t.home.cta.title}
          </h2>
          <p className="text-xl text-slate-300 mb-8 max-w-2xl mx-auto">
            {t.home.cta.subtitle}
          </p>
          <div className="flex flex-col sm:flex-row gap-4 justify-center">
            <Link
              href={`/${lang}/js2py`}
              className="inline-flex items-center px-8 py-4 bg-gradient-to-r from-blue-500 to-purple-600 text-white font-semibold rounded-xl hover:from-blue-600 hover:to-purple-700 transition-all duration-300 transform hover:scale-105 shadow-lg"
            >
              {t.home.cta.primary}
              <svg className="w-5 h-5 ml-2" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M13 7l5 5m0 0l-5 5m5-5H6" />
              </svg>
            </Link>
            <Link
              href={`/${lang}/docs`}
              className="inline-flex items-center px-8 py-4 border border-slate-600 text-slate-300 font-semibold rounded-xl hover:bg-slate-800 hover:text-white transition-all duration-300"
            >
              {t.home.cta.secondary}
            </Link>
          </div>
        </div>
      </div>
    </div>
  );
}