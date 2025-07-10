// 统一导出文件 - 根据语言动态加载配置
import { SupportedLanguage } from '@/messages';

// 导入各语言的配置
import { codeExamples as enCodeExamples, type CodeExample as EnCodeExample } from './en/code-examples';
import { languageConfigs as enLanguageConfigs, type LanguageConfig as EnLanguageConfig } from './en/language-configs';

import { codeExamples as zhCnCodeExamples, type CodeExample as ZhCnCodeExample } from './zh-cn/code-examples';
import { languageConfigs as zhCnLanguageConfigs, type LanguageConfig as ZhCnLanguageConfig } from './zh-cn/language-configs';

import { codeExamples as zhTwCodeExamples, type CodeExample as ZhTwCodeExample } from './zh-tw/code-examples';
import { languageConfigs as zhTwLanguageConfigs, type LanguageConfig as ZhTwLanguageConfig } from './zh-tw/language-configs';

// 统一类型定义
export type CodeExample = EnCodeExample | ZhCnCodeExample | ZhTwCodeExample;
export type LanguageConfig = EnLanguageConfig | ZhCnLanguageConfig | ZhTwLanguageConfig;

// 语言配置映射
const languageConfigsMap: Record<SupportedLanguage, LanguageConfig[]> = {
  'en': enLanguageConfigs,
  'zh-cn': zhCnLanguageConfigs,
  'zh-tw': zhTwLanguageConfigs,
};

// 代码示例映射
const codeExamplesMap: Record<SupportedLanguage, Record<string, CodeExample>> = {
  'en': enCodeExamples,
  'zh-cn': zhCnCodeExamples,
  'zh-tw': zhTwCodeExamples,
};

// 获取指定语言的代码示例
export function getCodeExamples(lang: SupportedLanguage): Record<string, CodeExample> {
  return codeExamplesMap[lang] || codeExamplesMap['en'];
}

// 获取指定语言的语言配置
export function getLanguageConfigs(lang: SupportedLanguage): LanguageConfig[] {
  return languageConfigsMap[lang] || languageConfigsMap['en'];
}

// 获取指定语言的语言配置
export function getLanguageConfig(lang: SupportedLanguage, value: string): LanguageConfig | undefined {
  const configs = getLanguageConfigs(lang);
  return configs.find(config => config.value === value);
}

// 获取可用的语言组合
export function getAvailableCombinations(lang: SupportedLanguage): string[] {
  const examples = getCodeExamples(lang);
  return Object.keys(examples);
}

// 根据难度获取示例
export function getExamplesByDifficulty(lang: SupportedLanguage, difficulty: 'beginner' | 'intermediate' | 'advanced'): CodeExample[] {
  const examples = getCodeExamples(lang);
  return Object.values(examples).filter(example => example.difficulty === difficulty);
}

// 根据分类获取示例
export function getExamplesByCategory(lang: SupportedLanguage, category: string): CodeExample[] {
  const examples = getCodeExamples(lang);
  return Object.values(examples).filter(example => example.category === category);
}

// 获取所有语言配置
export function getAllLanguageConfigs(lang: SupportedLanguage): LanguageConfig[] {
  return getLanguageConfigs(lang);
}

// 默认导出（向后兼容）
export const codeExamples = enCodeExamples;
export const languageConfigs = enLanguageConfigs; 