// 繁體中文語言配置
export interface LanguageConfig {
  value: string;
  label: string;
  icon: string;
  color: string;
  syntax: string;
  description?: string;
}

export const languageConfigs: LanguageConfig[] = [
  {
    value: 'js',
    label: 'JavaScript',
    icon: '⚡',
    color: 'bg-yellow-500',
    syntax: 'javascript',
    description: '動態類型腳本語言'
  },
  {
    value: 'py',
    label: 'Python',
    icon: '🐍',
    color: 'bg-blue-500',
    syntax: 'python',
    description: '簡潔優雅的編程語言'
  },
  {
    value: 'rust',
    label: 'Rust',
    icon: '🦀',
    color: 'bg-orange-500',
    syntax: 'rust',
    description: '記憶體安全的系統編程語言'
  },
]; 