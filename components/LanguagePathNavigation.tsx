'use client';

import { useState, useCallback, useRef, useEffect } from 'react';
import Link from 'next/link';
import { ChevronDown, Code, ArrowRight } from 'lucide-react';
import { getTranslations, type SupportedLanguage } from '@/messages';
import type { SourceLanguageConfig } from '@/components/header';

interface LanguagePathNavigationProps {
  lang: SupportedLanguage;
  sourceLanguages: readonly SourceLanguageConfig[];
  className?: string;
  isMobile?: boolean;
}



export function LanguagePathNavigation({ lang, sourceLanguages, className = '', isMobile = false }: LanguagePathNavigationProps) {
  const t = getTranslations(lang);
  const [isOpen, setIsOpen] = useState(false);
  const [hoveredLanguage, setHoveredLanguage] = useState<string | null>(null);
  const timeoutRef = useRef<NodeJS.Timeout | null>(null);
  
  const sourceLanguage = sourceLanguages[0]; // 目前只支持从 JavaScript 转换
  const targetLanguages = sourceLanguage.targets;

  // 清理超时
  useEffect(() => {
    return () => {
      if (timeoutRef.current) {
        clearTimeout(timeoutRef.current);
      }
    };
  }, []);

  const handleMouseEnter = useCallback(() => {
    if (!isMobile) {
      // 清除之前的超时
      if (timeoutRef.current) {
        clearTimeout(timeoutRef.current);
        timeoutRef.current = null;
      }
      setIsOpen(true);
    }
  }, [isMobile]);

  const handleMouseLeave = useCallback(() => {
    if (!isMobile) {
      // 添加延迟，避免鼠标快速移动时菜单闪烁
      if (timeoutRef.current) {
        clearTimeout(timeoutRef.current);
      }
      timeoutRef.current = setTimeout(() => {
        setIsOpen(false);
        setHoveredLanguage(null);
      }, 300); // 增加延迟时间，给用户更多时间
    }
  }, [isMobile]);

  const handleClick = useCallback(() => {
    if (isMobile) {
      setIsOpen(!isOpen);
    }
  }, [isMobile, isOpen]);

  const getStatusBadge = (status: 'completed' | 'development') => {
    if (status === 'completed') {
      return (
        <span className="px-2 py-1 text-xs bg-green-500/20 text-green-400 rounded-full border border-green-500/30">
          {t.header.languagePathNav.statusCompleted}
        </span>
      );
    }
    return (
      <span className="px-2 py-1 text-xs bg-yellow-500/20 text-yellow-400 rounded-full border border-yellow-500/30">
        {t.header.languagePathNav.statusDevelopment}
      </span>
    );
  };

  return (
    <div 
      className={`relative ${className}`}
      onMouseEnter={handleMouseEnter}
      onMouseLeave={handleMouseLeave}
    >
      {/* 触发按钮 */}
      <button
        onClick={handleClick}
        className="flex items-center space-x-2 px-4 py-2 bg-slate-800/50 hover:bg-slate-700/50 border border-slate-600/50 rounded-lg text-slate-300 hover:text-white transition-all duration-200 backdrop-blur-sm group"
      >
        <span className="text-base">🎯</span>
        <span className="text-sm font-medium">{t.header.languagePathNav.trigger}</span>
        <ChevronDown className={`w-4 h-4 transition-transform duration-200 ${isOpen ? 'rotate-180' : ''}`} />
      </button>

      {/* 下拉菜单 */}
      {isOpen && (
        <div 
          className={`absolute top-full ${isMobile ? 'left-0' : 'left-0'} mt-1 ${isMobile ? 'w-80' : 'w-96'} bg-slate-800/95 backdrop-blur-md border border-slate-700/50 rounded-xl shadow-2xl overflow-hidden z-50`}
        >
          {/* 菜单头部 */}
          <div className="px-4 py-3 bg-slate-900/50 border-b border-slate-700/50">
            <div className="flex items-center space-x-2">
              <div className={`w-6 h-6 bg-gradient-to-r ${sourceLanguage.gradient} rounded-lg flex items-center justify-center text-sm`}>
                {sourceLanguage.icon}
              </div>
              <span className="text-white font-semibold">{t.header.languagePathNav.languages.javascript}</span>
              <ArrowRight className="w-4 h-4 text-slate-400" />
              <span className="text-slate-300 text-sm">{t.header.languagePathNav.toLanguage}</span>
            </div>
          </div>

          {/* 目标语言列表 */}
          <div className="max-h-96 overflow-y-auto">
            {targetLanguages.map((targetLang) => (
              <Link
                key={targetLang.id}
                href={`/${lang}/docs/${targetLang.path}`}
                className="block px-4 py-3 hover:bg-slate-700/30 transition-colors group border-b border-slate-700/20 last:border-b-0"
                onMouseEnter={() => setHoveredLanguage(targetLang.id)}
                onMouseLeave={() => setHoveredLanguage(null)}
              >
                <div className="flex items-center justify-between">
                  <div className="flex items-center space-x-3">
                    {/* 语言图标 */}
                    <div className={`w-8 h-8 bg-gradient-to-r ${targetLang.gradient} rounded-lg flex items-center justify-center text-lg shadow-lg group-hover:shadow-xl transition-shadow`}>
                      {targetLang.icon}
                    </div>
                    
                    {/* 语言信息 */}
                    <div className="flex-1">
                      <div className="flex items-center space-x-2">
                        <span className="text-white font-semibold group-hover:text-blue-300 transition-colors">
                          {t.header.languagePathNav.languages[targetLang.id as keyof typeof t.header.languagePathNav.languages] || targetLang.name}
                        </span>
                        {getStatusBadge(targetLang.status)}
                      </div>
                      <p className="text-slate-400 text-xs mt-1 group-hover:text-slate-300 transition-colors">
                        {t.header.languagePathNav.descriptions[targetLang.path as keyof typeof t.header.languagePathNav.descriptions]}
                      </p>
                    </div>
                  </div>

                  {/* 箭头指示器 */}
                  <ArrowRight 
                    className={`w-4 h-4 text-slate-400 group-hover:text-blue-400 transition-all duration-200 ${
                      hoveredLanguage === targetLang.id ? 'translate-x-1' : ''
                    }`} 
                  />
                </div>
              </Link>
            ))}
          </div>

          {/* 菜单底部 */}
          <div className="px-4 py-3 bg-slate-900/30 border-t border-slate-700/50">
            <p className="text-slate-400 text-xs text-center">
              {t.header.languagePathNav.footerText}
            </p>
          </div>
        </div>
      )}

      {/* 背景遮罩 - 移动端点击外部关闭 */}
      {isMobile && isOpen && (
        <div
          className="fixed inset-0 z-40"
          onClick={() => setIsOpen(false)}
        />
      )}
    </div>
  );
}