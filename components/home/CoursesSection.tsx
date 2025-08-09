import Link from 'next/link';
import { ArrowRight } from 'lucide-react';
import { type SupportedLanguage } from '@/messages';

interface CoursesSectionProps {
  lang: SupportedLanguage;
  translations: any;
  courses: any[];
  isDefaultPage: boolean;
}

export function CoursesSection({ lang, translations, courses, isDefaultPage }: CoursesSectionProps) {
  const t = translations;

  return (
    <div className="py-20" id="courses">
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
              href={`/${lang}/docs/${course.name}`}
              className="group block"
            >
              <div className={`relative overflow-hidden ${course.bgColor} ${course.borderColor} border backdrop-blur-sm rounded-3xl p-8 hover:scale-105 transition-all duration-500`}>
                {/* 免费标识 */}
                <div className="absolute top-4 left-4 z-20">
                  <div className="inline-flex items-center px-3 py-1 bg-gradient-to-r from-green-500 to-emerald-500 rounded-full">
                    <span className="text-xs font-bold text-white">{t.home.courses.freeCourse}</span>
                  </div>
                </div>
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
                    {course.features.map((feature: string, index: number) => (
                      <span
                        key={index}
                        className="px-4 py-2 bg-slate-800/50 backdrop-blur-sm rounded-full text-sm text-slate-300 border border-slate-700/50"
                      >
                        {feature}
                      </span>
                    ))}
                  </div>

                  <div className="flex items-center justify-between">
                    <div className="flex items-center text-blue-400 group-hover:text-blue-300 transition-colors">
                      <span className="mr-2 font-semibold text-lg">
                        {t.home.courses.startLearning}
                      </span>
                      <ArrowRight className="w-6 h-6 transform group-hover:translate-x-2 transition-transform" />
                    </div>
                    {/* 直接进入按钮 */}
                    <div className="px-4 py-2 bg-gradient-to-r from-blue-500 to-purple-600 text-white text-sm font-semibold rounded-lg opacity-0 group-hover:opacity-100 transition-all duration-300 transform translate-x-4 group-hover:translate-x-0">
                      {t.home.courses.enterNow}
                    </div>
                  </div>
                </div>
              </div>
            </Link>
          ))}
        </div>
      </div>
    </div>
  );
} 