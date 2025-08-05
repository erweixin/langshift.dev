// English language configurations
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
    description: 'Dynamic typed scripting language'
  },
  {
    value: 'py',
    label: 'Python',
    icon: '🐍',
    color: 'bg-blue-500',
    syntax: 'python',
    description: 'Concise and elegant programming language'
  },
  {
    value: 'rust',
    label: 'Rust',
    icon: '🦀',
    color: 'bg-orange-500',
    syntax: 'rust',
    description: 'Memory-safe systems programming language'
  },
  {
    value: 'cpp',
    label: 'C++',
    icon: '🚀',
    color: 'bg-blue-700',
    syntax: 'cpp',
    description: 'High-performance systems programming language'
  },
  {
    value: 'go',
    label: 'Go',
    icon: '🐹',
    color: 'bg-cyan-500',
    syntax: 'go',
    description: 'Simple and efficient concurrent programming language'
  },
  {
    value: 'swift',
    label: 'Swift',
    icon: '🍎',
    color: 'bg-pink-500',
    syntax: 'swift',
    description: 'Modern, safe, and fast programming language for iOS/macOS'
  },
  {
    value: 'c',
    label: 'C',
    icon: '⚙️',
    color: 'bg-gray-600',
    syntax: 'c',
    description: 'Efficient systems programming language with manual memory management'
  },
  {
    value: 'kt',
    label: 'Kotlin',
    icon: '🟣',
    color: 'bg-purple-500',
    syntax: 'kotlin',
    description: 'Modern, safe JVM programming language, coroutines expert'
  },
]; 