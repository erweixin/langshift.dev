import en from './en.json';
import zhCn from './zh-cn.json';
import zhTw from './zh-tw.json';

// 翻译类型定义
export type TranslationKeys = typeof en;

// 支持的语言
export type SupportedLanguage = 'en' | 'zh-cn' | 'zh-tw';

// 翻译映射
const translations: Record<SupportedLanguage, TranslationKeys> = {
  en,
  'zh-cn': zhCn,
  'zh-tw': zhTw,
};

// 获取翻译函数
export function getTranslations(lang: SupportedLanguage): TranslationKeys {
  return translations[lang] || translations.en;
}

// 导出翻译对象供直接使用
export { en, zhCn, zhTw }; 