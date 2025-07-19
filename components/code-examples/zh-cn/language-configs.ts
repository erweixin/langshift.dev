// 简体中文语言配置
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
    description: '动态类型脚本语言'
  },
  {
    value: 'py',
    label: 'Python',
    icon: '🐍',
    color: 'bg-blue-500',
    syntax: 'python',
    description: '简洁优雅的编程语言'
  },
  {
    value: 'rust',
    label: 'Rust',
    icon: '🦀',
    color: 'bg-orange-500',
    syntax: 'rust',
    description: '内存安全的系统编程语言'
  },
  {
    value: 'cpp',
    label: 'C++',
    icon: '🚀',
    color: 'bg-blue-700',
    syntax: 'cpp',
    description: '高性能的系统编程语言'
  },
  {
    value: 'go',
    label: 'Go',
    icon: '🐹',
    color: 'bg-cyan-500',
    syntax: 'go',
    description: '简洁高效的并发编程语言'
  },
  {
    value: 'swift',
    label: 'Swift',
    icon: '🍎',
    color: 'bg-pink-500',
    syntax: 'swift',
    description: '现代、安全、快速的 iOS/macOS 编程语言'
  },
  {
    value: 'c',
    label: 'C',
    icon: '⚙️',
    color: 'bg-gray-600',
    syntax: 'c',
    description: '高效的系统编程语言，内存管理专家'
  },
  {
    value: 'java',
    label: 'Java',
    icon: '☕',
    color: 'bg-red-600',
    syntax: 'java',
    description: '企业级面向对象编程语言'
  },
]; 