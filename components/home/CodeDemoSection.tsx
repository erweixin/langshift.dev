import { InteractiveCodeComparison } from '@/components/interactive-code-comparison';
import { Sparkles } from 'lucide-react';
import { type SupportedLanguage } from '@/messages';

interface CodeDemoSectionProps {
  lang: SupportedLanguage;
  translations: any;
}

export function CodeDemoSection({ lang, translations }: CodeDemoSectionProps) {
  const t = translations;

  return (
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
  );
} 