import { Header } from '@/components/header';
import { HeroSection } from './HeroSection';
import { CodeDemoSection } from './CodeDemoSection';
import { LearningPathSection } from './LearningPathSection';
import { CoursesSection } from './CoursesSection';
import { FeaturesSection } from './FeaturesSection';
import { TestimonialsSection } from './TestimonialsSection';
import { CTASection } from './CTASection';
import { type SupportedLanguage } from '@/messages';

interface HomePageProps {
  lang: SupportedLanguage;
  translations: any;
  courses: any[];
  isDefaultPage: boolean;
  structuredData?: string;
  languageRedirectScript?: string;
}

export function HomePage({ 
  lang, 
  translations, 
  courses, 
  isDefaultPage, 
  structuredData,
  languageRedirectScript 
}: HomePageProps) {
  return (
    <div className="min-h-screen bg-gradient-to-br from-slate-900 via-slate-800 to-slate-900">
      <Header lang={lang} />
      
      {/* 结构化数据 */}
      {structuredData && (
        <script
          type="application/ld+json"
          dangerouslySetInnerHTML={{
            __html: structuredData
          }}
        />
      )}

      {/* 语言重定向脚本 - 仅在默认页面显示 */}
      {languageRedirectScript && isDefaultPage && (
        <script
          dangerouslySetInnerHTML={{
            __html: languageRedirectScript
          }}
        />
      )}

      {/* Hero Section */}
      <HeroSection lang={lang} translations={translations} isDefaultPage={isDefaultPage} />

      {/* 代码对比演示区域 */}
      <CodeDemoSection lang={lang} translations={translations} />

      {/* 课程选择区域 */}
      <CoursesSection lang={lang} translations={translations} courses={courses} isDefaultPage={isDefaultPage} />
      
      {/* 学习路径展示 */}
      <LearningPathSection lang={lang} translations={translations} isDefaultPage={isDefaultPage} />

      {/* 核心特性展示 */}
      <FeaturesSection lang={lang} translations={translations} />

      {/* 用户评价 */}
      <TestimonialsSection lang={lang} translations={translations} />

      {/* 最终 CTA */}
      <CTASection lang={lang} translations={translations} isDefaultPage={isDefaultPage} />
    </div>
  );
} 