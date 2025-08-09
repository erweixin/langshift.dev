import Link from 'next/link';
import { ArrowRight, Code, Target, Zap, Play, BookOpen } from 'lucide-react';
import { type SupportedLanguage } from '@/messages';

interface HeroSectionProps {
  lang: SupportedLanguage;
  translations: any;
  isDefaultPage: boolean;
}

export function HeroSection({ lang, translations, isDefaultPage }: HeroSectionProps) {
  const t = translations;

  return (
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
          {/* 免费标识 */}
          <div className="mb-6">
            <div className="inline-flex items-center px-6 py-3 bg-gradient-to-r from-green-500/20 to-emerald-500/20 border border-green-500/30 rounded-full backdrop-blur-sm">
              <div className="w-3 h-3 bg-green-400 rounded-full mr-3 animate-pulse"></div>
              <span className="text-green-300 font-semibold text-lg">{t.home.hero.freeOpenSource}</span>
            </div>
          </div>

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
              href="#courses"
              className="group inline-flex items-center px-8 py-4 bg-gradient-to-r from-blue-500 to-purple-600 text-white font-semibold rounded-xl hover:from-blue-600 hover:to-purple-700 transition-all duration-300 transform hover:scale-105 shadow-lg hover:shadow-xl relative overflow-hidden"
            >
              {/* 免费标识动画 */}
              <div className="absolute top-0 right-0 w-20 h-6 bg-gradient-to-r from-green-400 to-emerald-400 rounded-bl-xl flex items-center justify-center">
                <span className="text-xs font-bold text-white">{t.home.hero.freeBadge}</span>
              </div>
              <Play className="w-5 h-5 mr-2" />
              {t.home.hero.cta}
              <ArrowRight className="w-5 h-5 ml-2 transform group-hover:translate-x-1 transition-transform" />
            </Link>
            <Link
              href="#learning-path"
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
  );
} 