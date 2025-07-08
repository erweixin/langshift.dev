import { SEOHead } from '@/components/seo-head';
import { courseStructuredData } from '@/lib/seo-structured-data';
import Link from 'next/link';

interface HomePageProps {
  params: Promise<{ lang: string }>;
}

export default async function HomePage({ params }: HomePageProps) {
  const lang = (await params).lang;
  const isChinese = lang === 'zh-cn' || lang === 'zh-tw';
  
  const seoTitle = isChinese 
    ? 'LangShift.dev - 编程语言转换学习平台' 
    : 'LangShift.dev - Programming Language Learning Platform';
  const seoDescription = isChinese
    ? 'LangShift.dev 是专门为开发者设计的编程语言转换学习平台。通过对比不同编程语言的语法特性和概念映射，帮助开发者快速掌握新语言。支持 JavaScript 到 Python、Rust 等多种语言转换学习。'
    : 'LangShift.dev is a programming language learning platform designed for developers. Learn new languages through syntax comparison and concept mapping. Support JavaScript to Python, Rust and more.';

  const courses = [
    {
      name: 'js2py',
      title: isChinese ? 'JavaScript 到 Python' : 'JavaScript to Python',
      description: isChinese 
        ? '从 JavaScript 开发者视角学习 Python，掌握语法转换和概念映射'
        : 'Learn Python from a JavaScript developer perspective',
      icon: '🐍',
      color: 'bg-green-500',
    },
    {
      name: 'js2rust',
      title: isChinese ? 'JavaScript 到 Rust' : 'JavaScript to Rust',
      description: isChinese 
        ? '从 JavaScript 开发者视角学习 Rust，理解内存安全和系统编程'
        : 'Learn Rust from a JavaScript developer perspective',
      icon: '🦀',
      color: 'bg-orange-500',
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
        title={seoTitle}
        description={seoDescription}
        keywords={isChinese 
          ? ['编程语言', '语言学习', 'JavaScript', 'Python', 'Rust', '开发者', '代码对比', '语法转换', '在线学习']
          : ['programming languages', 'language learning', 'JavaScript', 'Python', 'Rust', 'developers', 'code comparison', 'syntax conversion', 'online learning']
        }
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
            "name": isChinese ? "编程语言转换课程" : "Programming Language Conversion Courses",
            "description": seoDescription,
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

      <div className="container mx-auto px-4 py-16">
        {/* 头部区域 */}
        <div className="text-center mb-16">
          <h1 className="text-5xl md:text-7xl font-bold text-white mb-6">
            <span className="bg-gradient-to-r from-blue-400 to-purple-600 bg-clip-text text-transparent">
              LangShift.dev
            </span>
          </h1>
          <p className="text-xl md:text-2xl text-slate-300 mb-8 max-w-3xl mx-auto">
            {isChinese 
              ? '专门为开发者设计的编程语言转换学习平台'
              : 'Programming language learning platform designed for developers'
            }
          </p>
          <p className="text-lg text-slate-400 max-w-2xl mx-auto">
            {isChinese 
              ? '通过对比不同编程语言的语法特性和概念映射，帮助开发者快速掌握新语言'
              : 'Learn new languages through syntax comparison and concept mapping'
            }
          </p>
        </div>

        {/* 课程列表 */}
        <div className="grid md:grid-cols-2 gap-8 max-w-4xl mx-auto">
          {courses.map((course) => (
            <Link
              key={course.name}
              href={`/${lang}/${course.name}`}
              className="group block"
            >
              <div className="bg-slate-800/50 backdrop-blur-sm border border-slate-700 rounded-xl p-8 hover:bg-slate-800/70 transition-all duration-300 hover:scale-105">
                <div className="flex items-center mb-4">
                  <div className={`w-12 h-12 ${course.color} rounded-lg flex items-center justify-center text-2xl mr-4`}>
                    {course.icon}
                  </div>
                  <h2 className="text-2xl font-bold text-white group-hover:text-blue-400 transition-colors">
                    {course.title}
                  </h2>
                </div>
                <p className="text-slate-300 text-lg leading-relaxed">
                  {course.description}
                </p>
                <div className="mt-6 flex items-center text-blue-400 group-hover:text-blue-300 transition-colors">
                  <span className="mr-2">
                    {isChinese ? '开始学习' : 'Start Learning'}
                  </span>
                  <svg className="w-5 h-5 transform group-hover:translate-x-1 transition-transform" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                    <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M13 7l5 5m0 0l-5 5m5-5H6" />
                  </svg>
                </div>
              </div>
            </Link>
          ))}
        </div>

        {/* 特色功能 */}
        <div className="mt-20 text-center">
          <h2 className="text-3xl font-bold text-white mb-12">
            {isChinese ? '平台特色' : 'Platform Features'}
          </h2>
          <div className="grid md:grid-cols-3 gap-8 max-w-5xl mx-auto">
            <div className="text-center">
              <div className="w-16 h-16 bg-blue-500/20 rounded-full flex items-center justify-center mx-auto mb-4">
                <svg className="w-8 h-8 text-blue-400" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                  <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M9 12l2 2 4-4m6 2a9 9 0 11-18 0 9 9 0 0118 0z" />
                </svg>
              </div>
              <h3 className="text-xl font-semibold text-white mb-2">
                {isChinese ? '交互式代码编辑器' : 'Interactive Code Editor'}
              </h3>
              <p className="text-slate-400">
                {isChinese 
                  ? '实时运行代码，即时查看结果'
                  : 'Run code in real-time and see results instantly'
                }
              </p>
            </div>
            <div className="text-center">
              <div className="w-16 h-16 bg-green-500/20 rounded-full flex items-center justify-center mx-auto mb-4">
                <svg className="w-8 h-8 text-green-400" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                  <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M8 9l3 3-3 3m5 0h3M5 20h14a2 2 0 002-2V6a2 2 0 00-2-2H5a2 2 0 00-2 2v12a2 2 0 002 2z" />
                </svg>
              </div>
              <h3 className="text-xl font-semibold text-white mb-2">
                {isChinese ? '语法对比学习' : 'Syntax Comparison'}
              </h3>
              <p className="text-slate-400">
                {isChinese 
                  ? '并排对比不同语言的语法差异'
                  : 'Side-by-side syntax comparison between languages'
                }
              </p>
            </div>
            <div className="text-center">
              <div className="w-16 h-16 bg-purple-500/20 rounded-full flex items-center justify-center mx-auto mb-4">
                <svg className="w-8 h-8 text-purple-400" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                  <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M12 6.253v13m0-13C10.832 5.477 9.246 5 7.5 5S4.168 5.477 3 6.253v13C4.168 18.477 5.754 18 7.5 18s3.332.477 4.5 1.253m0-13C13.168 5.477 14.754 5 16.5 5c1.746 0 3.332.477 4.5 1.253v13C19.832 18.477 18.246 18 16.5 18c-1.746 0-3.332.477-4.5 1.253" />
                </svg>
              </div>
              <h3 className="text-xl font-semibold text-white mb-2">
                {isChinese ? '渐进式学习路径' : 'Progressive Learning Path'}
              </h3>
              <p className="text-slate-400">
                {isChinese 
                  ? '从基础到高级的完整学习体系'
                  : 'Complete learning system from basics to advanced'
                }
              </p>
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}