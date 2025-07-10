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
        <div className="bg-gradient-to-r from-blue-600/20 to-purple-600/20 border border-blue-500/30 rounded-3xl p-12 text-center backdrop-blur-sm">
          <h2 className="text-4xl md:text-5xl font-bold text-white mb-6">
            {t.home.cta.title}
          </h2>
          <p className="text-xl text-slate-300 mb-8 max-w-2xl mx-auto">
            {t.home.cta.subtitle}
          </p>
          <div className="flex flex-col sm:flex-row gap-4 justify-center">
            <Link
              href={isDefaultPage ? "#courses" : `/${lang}/docs/js2py`}
              className="group inline-flex items-center px-8 py-4 bg-gradient-to-r from-blue-500 to-purple-600 text-white font-semibold rounded-xl hover:from-blue-600 hover:to-purple-700 transition-all duration-300 transform hover:scale-105 shadow-lg"
            >
              <Play className="w-5 h-5 mr-2" />
              {t.home.cta.primary}
              <ArrowRight className="w-5 h-5 ml-2 transform group-hover:translate-x-1 transition-transform" />
            </Link>
            <Link
              href={isDefaultPage ? "#learning-path" : `/${lang}/docs`}
              className="inline-flex items-center px-8 py-4 border border-slate-600 text-slate-300 font-semibold rounded-xl hover:bg-slate-800 hover:text-white transition-all duration-300"
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