'use client';

/**
 * 交互式代码对比组件 - 国际化版本
 * 
 * 支持的语言：
 * - 简体中文 (zh-cn)
 * - 繁体中文 (zh-tw) 
 * - 英文 (en)
 * 
 * 国际化键：
 * - t.home.interactiveCodeComparison.title: 组件标题
 * - t.home.interactiveCodeComparison.realtime: 实时标签
 * - t.home.interactiveCodeComparison.sourceLanguage: 源语言占位符
 * - t.home.interactiveCodeComparison.targetLanguage: 目标语言占位符
 * - t.home.interactiveCodeComparison.generatingComparison: 生成对比提示
 * - t.home.interactiveCodeComparison.noComparisonContent: 无内容标题
 * - t.home.interactiveCodeComparison.noComparisonDescription: 无内容描述
 * - t.home.interactiveCodeComparison.recommendedCombinations: 推荐组合标题
 * - t.home.interactiveCodeComparison.difficulty.*: 难度等级
 * - t.home.interactiveCodeComparison.copyCode: 复制代码提示
 * - t.home.interactiveCodeComparison.codeCopied: 代码已复制提示
 */

import React, { useState, useEffect } from 'react';
import { getTranslations, SupportedLanguage } from '@/messages';
import { Prism as SyntaxHighlighter } from 'react-syntax-highlighter';
import { oneDark } from 'react-syntax-highlighter/dist/esm/styles/prism';
import { 
  getCodeExamples,
  getLanguageConfigs,
  getLanguageConfig,
  getAvailableCombinations,
  type CodeExample 
} from './code-examples';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Separator } from '@/components/ui/separator';
import { Code, Loader2, Monitor, Sparkles, Zap, TrendingUp, Layers, GitBranch, ChevronRight, Copy, Check } from 'lucide-react';
import { cn } from '@/lib/utils';

export function InteractiveCodeComparison({ lang }: { lang: SupportedLanguage }) {
  const t = getTranslations(lang);
  const [initialLanguage, setInitialLanguage] = useState<string>('js');
  const [targetLanguage, setTargetLanguage] = useState<string>('py');
  const [currentComparison, setCurrentComparison] = useState<CodeExample | null>(null);
  const [isLoading, setIsLoading] = useState<boolean>(false);
  const [copiedCode, setCopiedCode] = useState<string | null>(null);

  // 自动切换对比内容
  useEffect(() => {
    setIsLoading(true);
    
    const timer = setTimeout(() => {
      const codeExamples = getCodeExamples(lang);
      const key = `${initialLanguage}-${targetLanguage}`;
      const reverseKey = `${targetLanguage}-${initialLanguage}`;

      if (codeExamples[key]) {
        setCurrentComparison(codeExamples[key]);
      } else if (codeExamples[reverseKey]) {
        const reversedExample = codeExamples[reverseKey];
        setCurrentComparison({
          leftCode: reversedExample.rightCode,
          rightCode: reversedExample.leftCode,
          leftLanguage: reversedExample.rightLanguage,
          rightLanguage: reversedExample.leftLanguage,
          titleLeft: reversedExample.titleRight,
          titleRight: reversedExample.titleLeft,
          description: reversedExample.description,
          tags: reversedExample.tags,
          difficulty: reversedExample.difficulty,
          category: reversedExample.category,
        });
      } else {
        setCurrentComparison(null);
      }
      setIsLoading(false);
    }, 200);

    return () => clearTimeout(timer);
  }, [initialLanguage, targetLanguage, lang]);

  const handleLanguageChange = (type: 'initial' | 'target', value: string) => {
    if (type === 'initial') {
      setInitialLanguage(value);
    } else {
      setTargetLanguage(value);
    }
  };

  const copyToClipboard = async (code: string, side: 'left' | 'right') => {
    try {
      await navigator.clipboard.writeText(code);
      setCopiedCode(side);
      setTimeout(() => setCopiedCode(null), 2000);
    } catch (err) {
      console.error('Failed to copy code:', err);
    }
  };

  const initialConfig = getLanguageConfig(lang, initialLanguage);
  const targetConfig = getLanguageConfig(lang, targetLanguage);

  // 如果配置不存在，使用默认值
  if (!initialConfig || !targetConfig) {
    return <div>Loading...</div>;
  }

  return (
    <div className="space-y-4">
      {/* 紧凑的语言选择器 */}
      <div className="bg-black/40 backdrop-blur-xl border border-white/10 rounded-xl p-4 shadow-2xl">
        <div className="flex items-center justify-between mb-3">
          <div className="flex items-center gap-2">
            <div className="p-1.5 bg-gradient-to-br from-blue-500/20 to-purple-500/20 rounded-lg border border-blue-500/30">
              <Code className="w-4 h-4 text-blue-400" />
            </div>
            <span className="text-sm font-semibold text-white">{t.home.interactiveCodeComparison.title}</span>
          </div>
          <Badge variant="outline" className="bg-emerald-500/10 border-emerald-500/30 text-emerald-400 text-xs px-2 py-1">
            <Zap className="w-3 h-3 mr-1" />
            {t.home.interactiveCodeComparison.realtime}
          </Badge>
        </div>

        <div className="flex items-center gap-3 flex-col lg:flex-row">
          {/* 初始语言选择 */}
          <div className="flex-1">
            <Select value={initialLanguage} onValueChange={(value) => handleLanguageChange('initial', value)}>
              <SelectTrigger className="h-9 bg-white/5 border-white/10 text-white hover:bg-white/10 focus:ring-2 focus:ring-blue-500/50 transition-all duration-200 text-sm">
                <SelectValue placeholder={t.home.interactiveCodeComparison.sourceLanguage} />
              </SelectTrigger>
              <SelectContent className="bg-black/95 border-white/10 backdrop-blur-xl">
                {getLanguageConfigs(lang).map((config) => (
                  <SelectItem key={config.value} value={config.value} className="text-white hover:bg-white/10 focus:bg-white/10">
                    <span className="flex items-center gap-2">
                      <span className="text-base">{config.icon}</span>
                      <span className="font-medium">{config.label}</span>
                    </span>
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>

          {/* 转换指示器 */}
          <div className="flex items-center gap-2 px-3 py-1.5 bg-gradient-to-r from-blue-500/20 to-emerald-500/20 rounded-lg border border-white/10">
            <div className={cn("w-2 h-2 rounded-full", initialConfig.color)}></div>
            <ChevronRight className="w-4 h-4 text-white/60" />
            <div className={cn("w-2 h-2 rounded-full", targetConfig.color)}></div>
          </div>

          {/* 目标语言选择 */}
          <div className="flex-1">
            <Select value={targetLanguage} onValueChange={(value) => handleLanguageChange('target', value)}>
              <SelectTrigger className="h-9 bg-white/5 border-white/10 text-white hover:bg-white/10 focus:ring-2 focus:ring-emerald-500/50 transition-all duration-200 text-sm">
                <SelectValue placeholder={t.home.interactiveCodeComparison.targetLanguage} />
              </SelectTrigger>
              <SelectContent className="bg-black/95 border-white/10 backdrop-blur-xl">
                {getLanguageConfigs(lang).map((config) => (
                  <SelectItem key={config.value} value={config.value} className="text-white hover:bg-white/10 focus:bg-white/10">
                    <span className="flex items-center gap-2">
                      <span className="text-base">{config.icon}</span>
                      <span className="font-medium">{config.label}</span>
                    </span>
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
        </div>
      </div>

      {/* 代码对比展示 */}
      <div className="relative">
        {isLoading ? (
          <div className="bg-black/40 backdrop-blur-xl border border-white/10 rounded-xl p-8 text-center shadow-2xl">
            <div className="relative">
              <Loader2 className="w-12 h-12 mx-auto mb-4 animate-spin text-blue-400" />
              <div className="absolute inset-0 bg-gradient-to-r from-blue-500/20 to-emerald-500/20 rounded-full blur-2xl opacity-20 animate-pulse"></div>
            </div>
            <p className="text-white font-medium">{t.home.interactiveCodeComparison.generatingComparison}</p>
          </div>
        ) : currentComparison ? (
          <div className="bg-black/40 backdrop-blur-xl border border-white/10 rounded-xl shadow-2xl overflow-hidden">
            {/* 头部信息栏 */}
            <div className="bg-gradient-to-r from-white/5 to-white/10 border-b border-white/10 p-3">
              <div className="flex items-center justify-between">
                <div className="flex items-center gap-3 flex-col lg:flex-row">
                  <div className="flex items-center gap-2">
                    <div className={cn("w-2 h-2 rounded-full", initialConfig.color)}></div>
                    <span className="text-sm font-medium text-white">{initialConfig.label}</span>
                  </div>
                  <ChevronRight className="w-4 h-4 text-white/40" />
                  <div className="flex items-center gap-2">
                    <div className={cn("w-2 h-2 rounded-full", targetConfig.color)}></div>
                    <span className="text-sm font-medium text-white">{targetConfig.label}</span>
                  </div>
                </div>
                <div className="flex items-center gap-2">
                  {currentComparison.difficulty && (
                    <Badge 
                      variant={currentComparison.difficulty as any}
                      className="text-xs px-2 py-0.5 font-medium"
                    >
                      {currentComparison.difficulty === 'beginner' ? t.home.interactiveCodeComparison.difficulty.beginner :
                       currentComparison.difficulty === 'intermediate' ? t.home.interactiveCodeComparison.difficulty.intermediate : 
                       t.home.interactiveCodeComparison.difficulty.advanced}
                    </Badge>
                  )}
                  {currentComparison.category && (
                    <Badge variant="outline" className="text-xs px-2 py-0.5 bg-white/5 border-white/20 text-white/80">
                      <Layers className="w-3 h-3 mr-1" />
                      {currentComparison.category}
                    </Badge>
                  )}
                </div>
              </div>
            </div>

            <div className="flex flex-col lg:flex-row">
              {/* 左侧代码块 */}
              <div className="flex-1 p-3 min-w-0">
                <div className="flex items-center justify-between mb-2">
                  <div className="flex items-center gap-2">
                    <div className="p-1 bg-gradient-to-br from-blue-500/20 to-blue-600/20 rounded border border-blue-500/30">
                      <span className="text-sm">{initialConfig.icon}</span>
                    </div>
                    <h4 className="text-sm font-semibold text-white truncate">{currentComparison.titleLeft}</h4>
                  </div>
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => copyToClipboard(currentComparison.leftCode, 'left')}
                    className="h-6 w-6 p-0 text-white/60 hover:text-white hover:bg-white/10"
                    title={t.home.interactiveCodeComparison.copyCode}
                  >
                    {copiedCode === 'left' ? <Check className="w-3 h-3" /> : <Copy className="w-3 h-3" />}
                  </Button>
                </div>
                
                <div className="relative group">
                  <div className="absolute inset-0 bg-gradient-to-r from-blue-500/5 to-transparent opacity-0 group-hover:opacity-100 transition-opacity duration-300 rounded-lg pointer-events-none"></div>
                  <div className="max-h-[280px] overflow-auto">
                    <SyntaxHighlighter
                      language={currentComparison.leftLanguage}
                      style={oneDark}
                      customStyle={{
                        margin: 0,
                        borderRadius: '8px',
                        fontSize: '12px',
                        lineHeight: '1.4',
                        background: '#0a0a0a',
                        border: '1px solid rgba(255,255,255,0.1)',
                        minHeight: '260px',
                      }}
                      showLineNumbers={true}
                      wrapLines={true}
                      lineNumberStyle={{
                        color: 'rgba(255,255,255,0.4)',
                        fontSize: '10px',
                        minWidth: '2em',
                        paddingRight: '0.8em',
                      }}
                    >
                      {currentComparison.leftCode}
                    </SyntaxHighlighter>
                  </div>
                </div>
              </div>

              <Separator orientation="vertical" className="bg-gradient-to-b from-white/10 to-transparent hidden lg:block" />

              {/* 右侧代码块 */}
              <div className="flex-1 p-3 min-w-0">
                <div className="flex items-center justify-between mb-2">
                  <div className="flex items-center gap-2">
                    <div className="p-1 bg-gradient-to-br from-emerald-500/20 to-emerald-600/20 rounded border border-emerald-500/30">
                      <span className="text-sm">{targetConfig.icon}</span>
                    </div>
                    <h4 className="text-sm font-semibold text-white truncate">{currentComparison.titleRight}</h4>
                  </div>
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => copyToClipboard(currentComparison.rightCode, 'right')}
                    className="h-6 w-6 p-0 text-white/60 hover:text-white hover:bg-white/10"
                    title={t.home.interactiveCodeComparison.copyCode}
                  >
                    {copiedCode === 'right' ? <Check className="w-3 h-3" /> : <Copy className="w-3 h-3" />}
                  </Button>
                </div>
                
                <div className="relative group">
                  <div className="absolute inset-0 bg-gradient-to-r from-emerald-500/5 to-transparent opacity-0 group-hover:opacity-100 transition-opacity duration-300 rounded-lg pointer-events-none"></div>
                  <div className="max-h-[280px] overflow-auto">
                    <SyntaxHighlighter
                      language={currentComparison.rightLanguage}
                      style={oneDark}
                      customStyle={{
                        margin: 0,
                        borderRadius: '8px',
                        fontSize: '12px',
                        lineHeight: '1.4',
                        background: '#0a0a0a',
                        border: '1px solid rgba(255,255,255,0.1)',
                        minHeight: '260px',
                      }}
                      showLineNumbers={true}
                      wrapLines={true}
                      lineNumberStyle={{
                        color: 'rgba(255,255,255,0.4)',
                        fontSize: '10px',
                        minWidth: '2em',
                        paddingRight: '0.8em',
                      }}
                    >
                      {currentComparison.rightCode}
                    </SyntaxHighlighter>
                  </div>
                </div>
              </div>
            </div>
          </div>
        ) : (
          <div className="bg-black/40 backdrop-blur-xl border border-white/10 rounded-xl p-6 text-center shadow-2xl">
            <div className="mb-4">
              <div className="relative inline-block">
                <Monitor className="w-12 h-12 mx-auto mb-3 text-white/40" />
                <div className="absolute inset-0 bg-gradient-to-r from-white/10 to-white/5 rounded-full blur-xl opacity-30"></div>
              </div>
              <h3 className="text-lg font-semibold mb-1 text-white">{t.home.interactiveCodeComparison.noComparisonContent}</h3>
              <p className="text-white/60 text-sm">{t.home.interactiveCodeComparison.noComparisonDescription}</p>
            </div>
            
            {/* 推荐组合 */}
            <div className="mt-4">
              <div className="flex items-center justify-center gap-2 mb-3">
                <TrendingUp className="w-4 h-4 text-white/40" />
                <h4 className="text-sm font-medium text-white/60">{t.home.interactiveCodeComparison.recommendedCombinations}</h4>
              </div>
              <div className="grid grid-cols-2 sm:grid-cols-3 gap-2 max-w-xl mx-auto">
                {getAvailableCombinations(lang).slice(0, 6).map((key) => {
                  const [left, right] = key.split('-');
                  const leftConfig = getLanguageConfig(lang, left);
                  const rightConfig = getLanguageConfig(lang, right);
                  const example = getCodeExamples(lang)[key];
                  
                  // 跳过无效的配置
                  if (!leftConfig || !rightConfig) {
                    return null;
                  }
                  
                  return (
                    <Button
                      key={key}
                      variant="outline"
                      onClick={() => {
                        setInitialLanguage(left);
                        setTargetLanguage(right);
                      }}
                      className="flex flex-col items-center gap-1 p-2 bg-white/5 hover:bg-white/10 border-white/10 text-xs group h-auto transition-all duration-200 hover:scale-105"
                    >
                      <div className="flex items-center gap-1">
                        <span className="text-sm">{leftConfig.icon}</span>
                        <ChevronRight className="w-3 h-3 text-white/40" />
                        <span className="text-sm">{rightConfig.icon}</span>
                      </div>
                      {example?.difficulty && (
                        <Badge variant={example.difficulty as any} className="text-xs px-1 py-0.5">
                          {example.difficulty === 'beginner' ? t.home.interactiveCodeComparison.difficulty.beginner :
                           example.difficulty === 'intermediate' ? t.home.interactiveCodeComparison.difficulty.intermediate : 
                           t.home.interactiveCodeComparison.difficulty.advanced}
                        </Badge>
                      )}
                    </Button>
                  );
                })}
              </div>
            </div>
          </div>
        )}
      </div>

      {/* 标签展示 */}
      {currentComparison?.tags && (
        <div className="flex items-center justify-center gap-2">
          <GitBranch className="w-4 h-4 text-white/40" />
          <div className="flex flex-wrap gap-1.5 justify-center">
            {currentComparison.tags.map((tag, index) => (
              <Badge
                key={index}
                variant="outline"
                className="px-2 py-0.5 bg-white/5 text-white/70 text-xs border-white/10 hover:bg-white/10 transition-all duration-200 hover:scale-105"
              >
                {tag}
              </Badge>
            ))}
          </div>
        </div>
      )}
    </div>
  );
}