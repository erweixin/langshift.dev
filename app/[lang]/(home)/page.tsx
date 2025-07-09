import { InteractiveCodeComparison } from '@/components/interactive-code-comparison';
import { SEOHead } from '@/components/seo-head';
import { courseStructuredData } from '@/lib/seo-structured-data';
import { getTranslations, type SupportedLanguage } from '@/messages';
import Link from 'next/link';
import { ArrowRight, Code, Sparkles, Zap, Target, Users, BookOpen, Rocket, CheckCircle, Star, Play } from 'lucide-react';

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

      {/* Hero Section - 重新设计 */}
      <div className="relative overflow-hidden">
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
                <h3 className="text-lg font-semibold text-white mb-2">代码对比学习</h3>
                <p className="text-slate-400 text-sm">并排对比语法差异，直观理解语言特性</p>
              </div>
              <div className="bg-slate-800/30 backdrop-blur-sm border border-slate-700/50 rounded-2xl p-6 hover:scale-105 transition-all duration-300">
                <div className="w-12 h-12 bg-gradient-to-br from-green-500 to-emerald-500 rounded-xl flex items-center justify-center mb-4 mx-auto">
                  <Target className="w-6 h-6 text-white" />
                </div>
                <h3 className="text-lg font-semibold text-white mb-2">渐进式掌握</h3>
                <p className="text-slate-400 text-sm">从基础到高级，13个模块系统学习</p>
              </div>
              <div className="bg-slate-800/30 backdrop-blur-sm border border-slate-700/50 rounded-2xl p-6 hover:scale-105 transition-all duration-300">
                <div className="w-12 h-12 bg-gradient-to-br from-purple-500 to-pink-500 rounded-xl flex items-center justify-center mb-4 mx-auto">
                  <Zap className="w-6 h-6 text-white" />
                </div>
                <h3 className="text-lg font-semibold text-white mb-2">实战项目</h3>
                <p className="text-slate-400 text-sm">50+真实项目，涵盖多个应用领域</p>
              </div>
            </div>

            {/* CTA 按钮 */}
            <div className="flex flex-col sm:flex-row gap-4 justify-center mb-16">
              <Link
                href={`/${lang}/js2py`}
                className="group inline-flex items-center px-8 py-4 bg-gradient-to-r from-blue-500 to-purple-600 text-white font-semibold rounded-xl hover:from-blue-600 hover:to-purple-700 transition-all duration-300 transform hover:scale-105 shadow-lg hover:shadow-xl"
              >
                <Play className="w-5 h-5 mr-2" />
                {t.home.hero.cta}
                <ArrowRight className="w-5 h-5 ml-2 transform group-hover:translate-x-1 transition-transform" />
              </Link>
              <Link
                href={`/${lang}/docs`}
                className="inline-flex items-center px-8 py-4 border border-slate-600 text-slate-300 font-semibold rounded-xl hover:bg-slate-800 hover:text-white transition-all duration-300 backdrop-blur-sm"
              >
                <BookOpen className="w-5 h-5 mr-2" />
                查看学习路径
              </Link>
            </div>

            {/* 统计数据 */}
            <div className="grid grid-cols-2 md:grid-cols-4 gap-8 max-w-4xl mx-auto">
              <div className="text-center group">
                <div className="text-3xl md:text-4xl font-bold text-white mb-2 group-hover:text-blue-400 transition-colors">
                  {t.home.hero.stats.learners}
                </div>
                <div className="text-slate-400 text-sm">活跃学习者</div>
              </div>
              <div className="text-center group">
                <div className="text-3xl md:text-4xl font-bold text-white mb-2 group-hover:text-green-400 transition-colors">
                  {t.home.hero.stats.languages}
                </div>
                <div className="text-slate-400 text-sm">支持语言</div>
              </div>
              <div className="text-center group">
                <div className="text-3xl md:text-4xl font-bold text-white mb-2 group-hover:text-purple-400 transition-colors">
                  {t.home.hero.stats.modules}
                </div>
                <div className="text-slate-400 text-sm">学习模块</div>
              </div>
              <div className="text-center group">
                <div className="text-3xl md:text-4xl font-bold text-white mb-2 group-hover:text-pink-400 transition-colors">
                  {t.home.hero.stats.projects}
                </div>
                <div className="text-slate-400 text-sm">实战项目</div>
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
              <span className="text-blue-400 text-sm font-medium">交互式体验</span>
            </div>
            <h2 className="text-4xl md:text-5xl lg:text-6xl font-bold text-white mb-6">
              体验代码对比学习
            </h2>
            <p className="text-xl text-slate-400 max-w-3xl mx-auto leading-relaxed">
              选择您熟悉的语言和目标语言，实时查看语法对比和概念映射
            </p>
          </div>
          
          {/* 代码对比组件 */}
          <div className="max-w-7xl mx-auto">
            <div className="relative">
              {/* 装饰性元素 */}
              <div className="absolute -top-4 -left-4 w-8 h-8 bg-gradient-to-br from-blue-500 to-purple-600 rounded-full opacity-60"></div>
              <div className="absolute -bottom-4 -right-4 w-6 h-6 bg-gradient-to-br from-green-500 to-emerald-600 rounded-full opacity-60"></div>
              
              <div className="bg-gradient-to-br from-slate-800/50 to-slate-900/50 backdrop-blur-sm border border-slate-700/50 rounded-3xl p-8 shadow-2xl">
                <InteractiveCodeComparison lang={lang} />
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
              完整的学习路径
            </h2>
            <p className="text-xl text-slate-400 max-w-3xl mx-auto">
              从基础语法到高级特性，13个模块循序渐进
            </p>
          </div>

          <div className="grid md:grid-cols-3 gap-8 max-w-6xl mx-auto">
            {[
              {
                phase: "基础阶段",
                color: "from-blue-500 to-cyan-500",
                items: [
                  "语言介绍与学习方法",
                  "语法对比与概念映射", 
                  "模块系统与包管理",
                  "面向对象与函数式编程",
                  "异步编程与并发处理"
                ]
              },
              {
                phase: "实战阶段",
                color: "from-green-500 to-emerald-500",
                items: [
                  "代码质量与测试",
                  "Web 开发实践",
                  "数据处理与自动化",
                  "综合实战项目"
                ]
              },
              {
                phase: "高级阶段", 
                color: "from-purple-500 to-pink-500",
                items: [
                  "高级语言特性",
                  "常见陷阱与解决方案",
                  "最佳实践与设计模式",
                  "类型系统与静态分析"
                ]
              }
            ].map((phase, phaseIndex) => (
              <div key={phaseIndex} className="relative group">
                <div className="absolute inset-0 bg-gradient-to-br from-slate-800/50 to-slate-900/50 rounded-2xl blur-xl opacity-0 group-hover:opacity-100 transition-opacity duration-500"></div>
                <div className="relative bg-slate-800/30 backdrop-blur-sm border border-slate-700/50 rounded-2xl p-8 hover:scale-105 transition-all duration-300">
                  <div className={`w-16 h-16 bg-gradient-to-br ${phase.color} rounded-2xl flex items-center justify-center mb-6 mx-auto shadow-lg`}>
                    <span className="text-2xl font-bold text-white">{phaseIndex + 1}</span>
                  </div>
                  <h3 className="text-2xl font-bold text-white mb-6 text-center">
                    {phase.phase}
                  </h3>
                  <div className="space-y-4">
                    {phase.items.map((item, itemIndex) => (
                      <div key={itemIndex} className="flex items-start">
                        <div className={`w-6 h-6 bg-gradient-to-br ${phase.color} rounded-full flex items-center justify-center mr-4 mt-1 flex-shrink-0 shadow-sm`}>
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
              选择您的学习路径
            </h2>
            <p className="text-xl text-slate-400 max-w-3xl mx-auto">
              从您熟悉的语言开始，快速掌握新语言的精髓
            </p>
          </div>

          <div className="grid lg:grid-cols-2 gap-8 max-w-6xl mx-auto">
            {courses.map((course) => (
              <Link
                key={course.name}
                href={`/${lang}/${course.name}`}
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
              为什么选择 LangShift.dev？
            </h2>
            <p className="text-xl text-slate-400 max-w-3xl mx-auto">
              专为开发者设计的现代化学习体验
            </p>
          </div>

          <div className="grid md:grid-cols-2 lg:grid-cols-3 gap-8 max-w-7xl mx-auto">
            {[
              {
                icon: <Code className="w-8 h-8" />,
                title: "交互式代码编辑器",
                description: "实时运行代码，即时查看结果。支持多语言语法高亮和智能提示，让学习更直观。",
                color: "from-blue-500 to-cyan-500",
                bgColor: "bg-blue-500/10",
              },
              {
                icon: <Target className="w-8 h-8" />,
                title: "智能语法对比",
                description: "并排对比不同语言的语法差异，自动映射概念关系，快速理解语言特性。",
                color: "from-green-500 to-emerald-500",
                bgColor: "bg-green-500/10",
              },
              {
                icon: <BookOpen className="w-8 h-8" />,
                title: "渐进式学习路径",
                description: "从基础到高级的完整学习体系，13 个模块循序渐进，确保学习效果。",
                color: "from-purple-500 to-pink-500",
                bgColor: "bg-purple-500/10",
              },
              {
                icon: <Zap className="w-8 h-8" />,
                title: "性能监控",
                description: "实时监控代码执行性能，对比不同语言的性能特性，优化开发效率。",
                color: "from-orange-500 to-red-500",
                bgColor: "bg-orange-500/10",
              },
              {
                icon: <Rocket className="w-8 h-8" />,
                title: "实战项目",
                description: "50+ 个真实项目案例，涵盖 Web 开发、数据处理、系统编程等多个领域。",
                color: "from-indigo-500 to-purple-500",
                bgColor: "bg-indigo-500/10",
              },
              {
                icon: <Users className="w-8 h-8" />,
                title: "开发者社区",
                description: "连接全球开发者，分享学习心得，解决技术难题，共同成长。",
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
              开发者说
            </h2>
            <p className="text-xl text-slate-400 max-w-3xl mx-auto">
              听听他们的学习体验
            </p>
          </div>

          <div className="grid md:grid-cols-3 gap-8 max-w-6xl mx-auto">
            {[
              {
                name: "张明",
                role: "全栈开发者",
                content: "通过 LangShift.dev 学习 Python，3 周就能独立开发 Web 应用了。对比学习的方式真的很有效！",
                avatar: "👨‍💻",
                rating: 5
              },
              {
                name: "李华", 
                role: "前端工程师",
                content: "从 JavaScript 到 Rust 的转换学习让我对系统编程有了全新的认识，性能提升明显。",
                avatar: "👩‍💻",
                rating: 5
              },
              {
                name: "王强",
                role: "技术主管", 
                content: "团队使用 LangShift.dev 进行技术栈迁移培训，学习效率提升了 5 倍，强烈推荐！",
                avatar: "👨‍💼",
                rating: 5
              }
            ].map((testimonial, index) => (
              <div key={index} className="bg-slate-800/30 backdrop-blur-sm border border-slate-700/50 rounded-2xl p-8 hover:scale-105 transition-all duration-300">
                <div className="flex items-center mb-6">
                  <div className="w-14 h-14 bg-gradient-to-br from-blue-500 to-purple-600 rounded-full flex items-center justify-center text-2xl mr-4">
                    {testimonial.avatar}
                  </div>
                  <div>
                    <h4 className="text-lg font-semibold text-white">{testimonial.name}</h4>
                    <p className="text-slate-400 text-sm">{testimonial.role}</p>
                    <div className="flex items-center gap-1 mt-1">
                      {[...Array(testimonial.rating)].map((_, i) => (
                        <Star key={i} className="w-4 h-4 fill-yellow-400 text-yellow-400" />
                      ))}
                    </div>
                  </div>
                </div>
                <p className="text-slate-300 leading-relaxed italic">
                  "{testimonial.content}"
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
              准备好开始你的语言学习之旅了吗？
            </h2>
            <p className="text-xl text-slate-300 mb-8 max-w-2xl mx-auto">
              加入 10,000+ 开发者的学习行列
            </p>
            <div className="flex flex-col sm:flex-row gap-4 justify-center">
              <Link
                href={`/${lang}/js2py`}
                className="group inline-flex items-center px-8 py-4 bg-gradient-to-r from-blue-500 to-purple-600 text-white font-semibold rounded-xl hover:from-blue-600 hover:to-purple-700 transition-all duration-300 transform hover:scale-105 shadow-lg"
              >
                <Play className="w-5 h-5 mr-2" />
                免费开始学习
                <ArrowRight className="w-5 h-5 ml-2 transform group-hover:translate-x-1 transition-transform" />
              </Link>
              <Link
                href={`/${lang}/docs`}
                className="inline-flex items-center px-8 py-4 border border-slate-600 text-slate-300 font-semibold rounded-xl hover:bg-slate-800 hover:text-white transition-all duration-300"
              >
                <BookOpen className="w-5 h-5 mr-2" />
                查看学习路径
              </Link>
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}