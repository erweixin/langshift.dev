import { CheckCircle } from 'lucide-react';
import { type SupportedLanguage } from '@/messages';

interface LearningPathSectionProps {
  lang: SupportedLanguage;
  translations: any;
  isDefaultPage: boolean;
}

export function LearningPathSection({ lang, translations, isDefaultPage }: LearningPathSectionProps) {
  const t = translations;

  return (
    <div className="py-20 bg-gradient-to-br from-slate-800/30 to-slate-900/30" id={isDefaultPage ? "learning-path" : undefined}>
      <div className="container mx-auto px-4">
        <div className="text-center mb-16">
          <h2 className="text-4xl md:text-5xl font-bold text-white mb-6">
            {t.home.learningPath.title}
          </h2>
          <p className="text-xl text-slate-400 max-w-3xl mx-auto mb-4">
            {t.home.learningPath.subtitle}
          </p>
          <p className="text-lg text-slate-400 max-w-4xl mx-auto">
            {t.home.learningPath.description}
          </p>
        </div>

        <div className="grid md:grid-cols-3 gap-8 max-w-6xl mx-auto mb-16">
          {t.home.learningPath.modules.map((phase: any, phaseIndex: number) => (
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
                <div className="space-y-4">
                  {phase.items.map((item: any, itemIndex: number) => (
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
        <div className="max-w-6xl mx-auto mb-16">
          <div className="text-center mb-12">
            <h3 className="text-3xl md:text-4xl font-bold text-white mb-4">
              {t.home.learningPath.languageOptimization.title}
            </h3>
            <p className="text-lg text-slate-400 max-w-2xl mx-auto">
              {t.home.learningPath.languageOptimization.subtitle}
            </p>
          </div>
          <div className="grid md:grid-cols-2 gap-8">
            {Object.entries(t.home.learningPath.languageSpecificFeatures).map(([key, feature]: [string, any], index: number) => (
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
                    {feature.highlights.map((highlight: string, highlightIndex: number) => (
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
        <div className="max-w-6xl mx-auto mt-16">
          <div className="relative">
            <div className="absolute inset-0 bg-gradient-to-br from-slate-800/50 to-slate-900/50 rounded-3xl blur-xl opacity-60"></div>
            <div className="relative bg-gradient-to-br from-slate-800/30 to-slate-900/30 backdrop-blur-sm border border-slate-700/50 rounded-3xl p-10">
              <div className="text-center mb-8">
                <div className="w-16 h-16 bg-gradient-to-br from-blue-500 to-purple-600 rounded-2xl flex items-center justify-center mb-6 mx-auto shadow-lg">
                  <span className="text-2xl">💡</span>
                </div>
                <h4 className="text-2xl font-bold text-white mb-4">
                  {t.home.learningPath.learningTipsSection.title}
                </h4>
                <p className="text-slate-400 max-w-2xl mx-auto">
                  {t.home.learningPath.learningTipsSection.subtitle}
                </p>
              </div>
              <div className="grid md:grid-cols-2 gap-6">
                {t.home.learningPath.learningTips.map((tip: string, index: number) => (
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
  );
} 