'use client';

import { getTranslations, type SupportedLanguage } from '@/messages';
import Link from 'next/link';
import { usePathname } from 'next/navigation';
import { useState, useCallback } from 'react';
import { Menu, X, Globe, ChevronDown, Github } from 'lucide-react';
import { trackLanguageSwitch } from '@/components/analytics';
import { LanguagePathNavigation } from '@/components/LanguagePathNavigation';
import { MobileLanguagePathMenu } from '@/components/MobileLanguagePathMenu';

// 源语言配置 - 统一数据结构
const SOURCE_LANGUAGES = [
  {
    id: 'javascript',
    name: 'JavaScript',
    icon: '🚀',
    gradient: 'from-yellow-500 to-orange-500',
    targets: [
      {
        id: 'python',
        name: 'Python',
        icon: '🐍',
        gradient: 'from-green-500 to-emerald-500',
        path: 'js2py',
        status: 'completed' as const,
      },
      {
        id: 'rust',
        name: 'Rust',
        icon: '🦀',
        gradient: 'from-orange-500 to-red-500',
        path: 'js2rust',
        status: 'completed' as const,
      },
      {
        id: 'go',
        name: 'Go',
        icon: '🐹',
        gradient: 'from-cyan-500 to-blue-500',
        path: 'js2go',
        status: 'completed' as const,
      },
      {
        id: 'cpp',
        name: 'C++',
        icon: '🚀',
        gradient: 'from-blue-500 to-indigo-500',
        path: 'js2cpp',
        status: 'completed' as const,
      },
      {
        id: 'swift',
        name: 'Swift',
        icon: '🍎',
        gradient: 'from-pink-500 to-purple-500',
        path: 'js2swift',
        status: 'completed' as const,
      },
      {
        id: 'c',
        name: 'C',
        icon: '⚙️',
        gradient: 'from-gray-500 to-slate-500',
        path: 'js2c',
        status: 'completed' as const,
      },
      {
        id: 'kotlin',
        name: 'Kotlin',
        icon: '🟣',
        gradient: 'from-purple-500 to-violet-500',
        path: 'js2kotlin',
        status: 'completed' as const,
      },
      {
        id: 'java',
        name: 'Java',
        icon: '☕',
        gradient: 'from-red-500 to-rose-500',
        path: 'js2java',
        status: 'completed' as const,
      },
    ]
  },
  {
    id: 'python',
    name: 'Python',
    icon: '🐍',
    gradient: 'from-green-500 to-emerald-500',
    path: 'py2js',
    status: 'completed' as const,
    targets: [
      {
        id: 'javascript',
        name: 'JavaScript',
        icon: '🚀',
        gradient: 'from-yellow-500 to-orange-500',
        path: 'py2js',
        status: 'completed' as const,
      },
      {
        id: 'rust',
        name: 'Rust',
        icon: '🦀',
        gradient: 'from-orange-500 to-red-500',
        path: 'py2rust',
        status: 'completed' as const,
      },
      {
        id: 'go',
        name: 'Go',
        icon: '🐹',
        gradient: 'from-cyan-500 to-blue-500',
        path: 'py2go',
        status: 'completed' as const,
      },
    ]
  }
  // 未来可以添加其他源语言
] as const;

export type SourceLanguageConfig = typeof SOURCE_LANGUAGES[number];
export type TargetLanguageConfig = SourceLanguageConfig['targets'][number];

interface HeaderProps {
  lang: SupportedLanguage;
}

export function Header({ lang }: HeaderProps) {
  const t = getTranslations(lang);
  const pathname = usePathname();
  const [isMenuOpen, setIsMenuOpen] = useState(false);
  const [isLanguageOpen, setIsLanguageOpen] = useState(false);

  const languages = [
    { code: 'en', name: t.header.languageSelector.en, flag: '🇺🇸' },
    { code: 'zh-cn', name: t.header.languageSelector['zh-cn'], flag: '🇨🇳' },
    { code: 'zh-tw', name: t.header.languageSelector['zh-tw'], flag: '🇨🇳' },
  ];

  const currentLanguage = languages.find(l => l.code === lang);

  const navItems: { href: string; label: string }[] = [];

  const getLanguageUrl = useCallback((languageCode: string) => {
    const pathWithoutLang = pathname.replace(/^\/[^\/]+/, '');
    return `/${languageCode}${pathWithoutLang}`;
  }, [pathname]);

  return (
    <header className="fixed top-0 left-0 right-0 z-50 bg-slate-900/80 backdrop-blur-md border-b border-slate-700/50">
      <div className="container mx-auto px-4">
        <div className="flex items-center justify-between h-16 gap-6">
          {/* Logo */}
          <Link 
            href={`/${lang}`}
            className="flex items-center space-x-2 group"
          >
            <div className="w-8 h-8 bg-gradient-to-br from-blue-500 to-purple-600 rounded-lg flex items-center justify-center shadow-lg group-hover:shadow-xl transition-shadow">
              <span className="text-white font-bold text-sm">LS</span>
            </div>
            <span className="text-xl font-bold text-white group-hover:text-blue-400 transition-colors">
              {t.header.logo}
            </span>
          </Link>

          {/* Desktop Navigation */}
          <div className="hidden md:flex items-center space-x-8 grow-1">
            {/* 语言转换导航 - 放在左侧 */}
            {SOURCE_LANGUAGES.map((language) => (
              <LanguagePathNavigation key={language.id} lang={lang} sourceLanguages={language} />
            ))}
            
            {navItems.map((item) => (
              <Link
                key={item.href}
                href={item.href}
                className={`text-slate-300 hover:text-white transition-colors font-medium ${
                  pathname === item.href ? 'text-blue-400' : ''
                }`}
              >
                {item.label}
              </Link>
            ))}
          </div>

          {/* Language Selector & GitHub & Mobile Menu Button */}
          <div className="flex items-center space-x-4">
            {/* GitHub Link */}
            <a
              href="https://github.com/erweixin/langshift.dev"
              target="_blank"
              rel="noopener noreferrer"
              className="hidden sm:flex items-center space-x-2 px-3 py-2 bg-slate-800/50 hover:bg-slate-700/50 border border-slate-600/50 rounded-lg text-slate-300 hover:text-white transition-all duration-200 backdrop-blur-sm group"
              title={t.header.github.tooltip}
            >
              <Github className="w-4 h-4 group-hover:scale-110 transition-transform" />
              <span className="text-sm font-medium hidden sm:inline">{t.header.github.label}</span>
            </a>

            {/* Language Selector */}
            <div className="relative">
              <button
                onClick={() => setIsLanguageOpen(!isLanguageOpen)}
                className="flex items-center space-x-2 px-3 py-2 bg-slate-800/50 hover:bg-slate-700/50 border border-slate-600/50 rounded-lg text-slate-300 hover:text-white transition-all duration-200 backdrop-blur-sm"
              >
                <Globe className="w-4 h-4" />
                <span className="text-sm font-medium">{currentLanguage?.flag}</span>
                <ChevronDown className={`w-4 h-4 transition-transform ${isLanguageOpen ? 'rotate-180' : ''}`} />
              </button>

              {/* Language Dropdown */}
              {isLanguageOpen && (
                <div className="absolute top-full right-0 mt-2 w-48 bg-slate-800/90 backdrop-blur-md border border-slate-700/50 rounded-xl shadow-xl overflow-hidden">
                  {languages.map((language) => (
                    <Link
                      key={language.code}
                      href={getLanguageUrl(language.code)}
                      className={`flex items-center space-x-3 px-4 py-3 text-sm transition-colors ${
                        lang === language.code
                          ? 'bg-blue-500/20 text-blue-400'
                          : 'text-slate-300 hover:bg-slate-700/50 hover:text-white'
                      }`}
                      onClick={() => {
                        setIsLanguageOpen(false);
                        // 追踪语言切换事件
                        if (lang !== language.code) {
                          trackLanguageSwitch(lang, language.code);
                        }
                      }}
                    >
                      <span className="text-lg">{language.flag}</span>
                      <span className="font-medium">{language.name}</span>
                    </Link>
                  ))}
                </div>
              )}
            </div>

            {/* Mobile Menu Button */}
            <button
              onClick={() => setIsMenuOpen(!isMenuOpen)}
              className="md:hidden p-2 text-slate-300 hover:text-white transition-colors"
            >
              {isMenuOpen ? <X className="w-6 h-6" /> : <Menu className="w-6 h-6" />}
            </button>
          </div>
        </div>

        {/* Mobile Navigation */}
        {isMenuOpen && (
          <div className="md:hidden border-t border-slate-700/50">
            {/* 常规导航项 */}
            {navItems.length > 0 && (
              <nav className="flex flex-col py-4">
                {navItems.map((item) => (
                  <Link
                    key={item.href}
                    href={item.href}
                    className={`px-4 py-3 text-slate-300 hover:text-white hover:bg-slate-700/30 transition-colors font-medium ${
                      pathname === item.href ? 'text-blue-400 bg-blue-500/10' : ''
                    }`}
                    onClick={() => setIsMenuOpen(false)}
                  >
                    {item.label}
                  </Link>
                ))}
              </nav>
            )}
            
            {/* 移动端语言转换菜单 */}
            <div className={navItems.length > 0 ? 'border-t border-slate-700/30' : ''}>
              <MobileLanguagePathMenu 
                lang={lang} 
                sourceLanguages={SOURCE_LANGUAGES}
                onNavigate={() => setIsMenuOpen(false)}
              />
            </div>
          </div>
        )}
      </div>

      {/* Backdrop for language dropdown */}
      {isLanguageOpen && (
        <div
          className="fixed inset-0 z-10"
          onClick={() => setIsLanguageOpen(false)}
        />
      )}
    </header>
  );
} 