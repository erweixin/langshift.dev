import { Code, Target, BookOpen, Zap, Rocket, Users } from 'lucide-react';
import { type SupportedLanguage } from '@/messages';

interface FeaturesSectionProps {
  lang: SupportedLanguage;
  translations: any;
}

export function FeaturesSection({ lang, translations }: FeaturesSectionProps) {
  const t = translations;

  const features = [
    {
      icon: <Code className="w-8 h-8" />,
      title: t.home.features.codeEditor.title,
      description: t.home.features.codeEditor.description,
      color: "from-blue-500 to-cyan-500",
      bgColor: "bg-blue-500/10",
    },
    {
      icon: <Target className="w-8 h-8" />,
      title: t.home.features.syntaxComparison.title,
      description: t.home.features.syntaxComparison.description,
      color: "from-green-500 to-emerald-500",
      bgColor: "bg-green-500/10",
    },
    {
      icon: <BookOpen className="w-8 h-8" />,
      title: t.home.features.learningPath.title,
      description: t.home.features.learningPath.description,
      color: "from-purple-500 to-pink-500",
      bgColor: "bg-purple-500/10",
    },
    {
      icon: <Zap className="w-8 h-8" />,
      title: t.home.features.performance.title,
      description: t.home.features.performance.description,
      color: "from-orange-500 to-red-500",
      bgColor: "bg-orange-500/10",
    },
    {
      icon: <Rocket className="w-8 h-8" />,
      title: t.home.features.projects.title,
      description: t.home.features.projects.description,
      color: "from-indigo-500 to-purple-500",
      bgColor: "bg-indigo-500/10",
    },
    {
      icon: <Users className="w-8 h-8" />,
      title: t.home.features.community.title,
      description: t.home.features.community.description,
      color: "from-teal-500 to-cyan-500",
      bgColor: "bg-teal-500/10",
    },
  ];

  return (
    <div className="py-20 bg-gradient-to-br from-slate-800/30 to-slate-900/30">
      <div className="container mx-auto px-4">
        <div className="text-center mb-16">
          <h2 className="text-4xl md:text-5xl font-bold text-white mb-6">
            {t.home.features.title}
          </h2>
          <p className="text-xl text-slate-400 max-w-3xl mx-auto">
            {t.home.features.subtitle}
          </p>
        </div>

        <div className="grid md:grid-cols-2 lg:grid-cols-3 gap-8 max-w-7xl mx-auto">
          {features.map((feature, index) => (
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
  );
} 