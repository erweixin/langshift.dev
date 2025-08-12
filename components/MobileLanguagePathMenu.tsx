'use client';

import Link from 'next/link';
import { ArrowRight } from 'lucide-react';
import { getTranslations, type SupportedLanguage } from '@/messages';
import type { SourceLanguageConfig } from '@/components/header';

interface MobileLanguagePathMenuProps {
  lang: SupportedLanguage;
  sourceLanguages: readonly SourceLanguageConfig[];
  onNavigate?: () => void; // 点击导航后的回调，用于关闭移动端菜单
}



export function MobileLanguagePathMenu({ lang, sourceLanguages, onNavigate }: MobileLanguagePathMenuProps) {
  const t = getTranslations(lang);

  const getStatusBadge = (status: 'completed' | 'development') => {
    if (status === 'completed') {
      return (
        <span className="w-2 h-2 bg-green-400 rounded-full"></span>
      );
    }
    return (
      <span className="w-2 h-2 bg-yellow-400 rounded-full"></span>
    );
  };

  return (
    <div className="w-full">
      {/* 菜单标题 */}
      <div className="px-4 py-3 border-b border-slate-700/30">
        <h3 className="text-white font-semibold text-base flex items-center space-x-2">
          <span className="text-2xl">🎯</span>
          <span>{t.header.languagePathNav.trigger}</span>
        </h3>
        <p className="text-slate-400 text-sm mt-1">
          {t.header.languagePathNav.mobileSubtitle}
        </p>
      </div>

      {/* 源语言分组 - 直接展示所有内容 */}
      <div className="max-h-96 overflow-y-auto">
        {sourceLanguages.map((sourceLanguage) => (
          <div key={sourceLanguage.id} className="border-b border-slate-700/20 last:border-b-0">
            {/* 源语言头部 */}
            <div className="px-4 py-3 bg-slate-800/30">
              <div className="flex items-center space-x-3">
                <div className={`w-8 h-8 bg-gradient-to-r ${sourceLanguage.gradient} rounded-lg flex items-center justify-center text-base shadow-lg`}>
                  {sourceLanguage.icon}
                </div>
                <div>
                  <div className="text-white font-semibold text-sm">
                    {t.header.languagePathNav.languages[sourceLanguage.id as keyof typeof t.header.languagePathNav.languages] || sourceLanguage.name}
                  </div>
                  <div className="text-slate-400 text-xs">
                    {sourceLanguage.targets.length} {t.header.languagePathNav.targetCount}
                  </div>
                </div>
              </div>
            </div>

            {/* 目标语言网格 */}
            <div className="p-3 bg-slate-900/20">
              <div className="grid grid-cols-2 gap-2">
                {sourceLanguage.targets.map((targetLanguage) => (
                  <Link
                    key={targetLanguage.id}
                    href={`/${lang}/docs/${targetLanguage.path}`}
                    className="flex items-center space-x-2 p-3 bg-slate-800/50 hover:bg-slate-700/50 border border-slate-700/30 hover:border-blue-500/40 rounded-lg transition-all group"
                    onClick={onNavigate}
                  >
                    {/* 目标语言图标 */}
                    <div className={`w-6 h-6 bg-gradient-to-r ${targetLanguage.gradient} rounded flex items-center justify-center text-xs shadow-sm group-hover:shadow-md transition-shadow flex-shrink-0`}>
                      {targetLanguage.icon}
                    </div>
                    
                    {/* 语言名称和状态 */}
                    <div className="flex-1 min-w-0">
                      <div className="flex items-center space-x-2">
                        <span className="text-white text-sm font-medium group-hover:text-blue-300 transition-colors truncate">
                          {t.header.languagePathNav.languages[targetLanguage.id as keyof typeof t.header.languagePathNav.languages] || targetLanguage.name}
                        </span>
                        {getStatusBadge(targetLanguage.status)}
                      </div>
                    </div>
                  </Link>
                ))}
              </div>
            </div>
          </div>
        ))}
      </div>

      {/* 菜单底部 */}
      <div className="px-4 py-3 bg-slate-900/30 border-t border-slate-700/50">
        <p className="text-slate-400 text-xs text-center">
          {t.header.languagePathNav.footerText}
        </p>
      </div>
    </div>
  );
}