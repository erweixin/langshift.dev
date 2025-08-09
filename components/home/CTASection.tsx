import Link from 'next/link';
import { ArrowRight, Play, BookOpen } from 'lucide-react';
import { type SupportedLanguage } from '@/messages';

interface CTASectionProps {
  lang: SupportedLanguage;
  translations: any;
  isDefaultPage: boolean;
}

export function CTASection({ lang, translations, isDefaultPage }: CTASectionProps) {
  const t = translations;

  return (
    <div className="py-20">
      <div className="container mx-auto px-4">
        <div className="bg-gradient-to-r from-blue-600/20 to-purple-600/20 border border-blue-500/30 rounded-3xl p-12 text-center backdrop-blur-sm relative overflow-hidden">
          {/* 免费标识动画背景 */}
          <div className="absolute top-0 left-0 w-full h-full">
            <div className="absolute top-4 left-4 w-24 h-24 bg-green-400/10 rounded-full animate-pulse"></div>
            <div className="absolute top-4 right-4 w-32 h-32 bg-emerald-400/10 rounded-full animate-pulse delay-1000"></div>
            <div className="absolute bottom-4 left-1/3 w-20 h-20 bg-green-500/10 rounded-full animate-pulse delay-2000"></div>
          </div>
          
          {/* 免费标识 */}
          <div className="relative z-10 mb-6">
            <div className="inline-flex items-center px-6 py-3 bg-gradient-to-r from-green-500/30 to-emerald-500/30 border border-green-500/50 rounded-full backdrop-blur-sm">
              <div className="w-3 h-3 bg-green-400 rounded-full mr-3 animate-pulse"></div>
              <span className="text-green-300 font-bold text-lg">{t.home.cta.completelyFree}</span>
            </div>
          </div>

          <h2 className="text-4xl md:text-5xl font-bold text-white mb-6 relative z-10">
            {t.home.cta.title}
          </h2>
          <p className="text-xl text-slate-300 mb-8 max-w-2xl mx-auto relative z-10">
            {t.home.cta.subtitle}
          </p>
          <div className="flex flex-col sm:flex-row gap-4 justify-center relative z-10">
            <Link
              href="#courses"
              className="group inline-flex items-center px-8 py-4 bg-gradient-to-r from-blue-500 to-purple-600 text-white font-semibold rounded-xl hover:from-blue-600 hover:to-purple-700 transition-all duration-300 transform hover:scale-105 shadow-lg relative overflow-hidden"
            >
              {/* 免费闪光效果 */}
              <div className="absolute top-0 right-0 w-20 h-6 bg-gradient-to-r from-green-400 to-emerald-400 rounded-bl-xl flex items-center justify-center">
                <span className="text-xs font-bold text-white">{t.home.cta.freeBadge || t.home.hero.freeBadge}</span>
              </div>
              <Play className="w-5 h-5 mr-2" />
              {t.home.cta.primary}
              <ArrowRight className="w-5 h-5 ml-2 transform group-hover:translate-x-1 transition-transform" />
            </Link>
            <Link
              href="#learning-path"
              className="inline-flex items-center px-8 py-4 border border-green-500/50 text-green-300 font-semibold rounded-xl hover:bg-green-500/10 hover:text-green-200 transition-all duration-300 backdrop-blur-sm"
            >
              <BookOpen className="w-5 h-5 mr-2" />
              {t.home.cta.secondary}
            </Link>
          </div>
        </div>
      </div>
    </div>
  );
} 