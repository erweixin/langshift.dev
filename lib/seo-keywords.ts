// SEO 关键词映射配置
// 为不同页面和模块定义合适的关键词

export interface SEOKeywords {
  [key: string]: string[];
}

// 基础关键词（所有页面通用）
export const baseKeywords = {
  'zh-cn': [
    '编程语言', '语言学习', '开发者', '代码对比', '语法转换', 
    '在线学习', '编程教育', '语言迁移', '技能提升'
  ],
  'zh-tw': [
    '程式語言', '語言學習', '開發者', '程式碼對比', '語法轉換',
    '線上學習', '程式設計教育', '語言遷移', '技能提升'
  ],
  'en': [
    'programming languages', 'language learning', 'developers', 
    'code comparison', 'syntax conversion', 'online learning', 
    'programming education', 'language migration', 'skill development'
  ]
};

// 课程特定关键词
export const courseKeywords = {
  'js2py': {
    'zh-cn': [
      'JavaScript', 'Python', '前端开发', '后端开发', '全栈开发',
      'Web开发', '数据处理', '自动化脚本', '类型注解', '异步编程',
      '面向对象', '函数式编程', '模块系统', '包管理', '测试驱动开发'
    ],
    'zh-tw': [
      'JavaScript', 'Python', '前端開發', '後端開發', '全棧開發',
      'Web開發', '資料處理', '自動化腳本', '型別註解', '非同步程式設計',
      '物件導向', '函數式程式設計', '模組系統', '套件管理', '測試驅動開發'
    ],
    'en': [
      'JavaScript', 'Python', 'frontend development', 'backend development', 
      'full-stack development', 'web development', 'data processing', 
      'automation scripts', 'type annotations', 'async programming',
      'object-oriented', 'functional programming', 'module system', 
      'package management', 'test-driven development'
    ]
  },
  'js2rust': {
    'zh-cn': [
      'JavaScript', 'Rust', '系统编程', '内存安全', '并发编程',
      '性能优化', 'WebAssembly', '零成本抽象', '所有权系统',
      '生命周期', '错误处理', '类型系统', '特征系统', '宏编程'
    ],
    'zh-tw': [
      'JavaScript', 'Rust', '系統程式設計', '記憶體安全', '並發程式設計',
      '效能優化', 'WebAssembly', '零成本抽象', '所有權系統',
      '生命週期', '錯誤處理', '型別系統', '特徵系統', '巨集程式設計'
    ],
    'en': [
      'JavaScript', 'Rust', 'systems programming', 'memory safety', 
      'concurrent programming', 'performance optimization', 'WebAssembly',
      'zero-cost abstractions', 'ownership system', 'lifetimes', 
      'error handling', 'type system', 'traits', 'macros'
    ]
  }
};

// 模块特定关键词
export const moduleKeywords = {
  'js2py': {
    'module-00-python-introduction': {
      'zh-cn': ['Python介绍', '环境搭建', 'pip', 'venv', 'pyenv', '开发工具', 'IDE配置'],
      'zh-tw': ['Python介紹', '環境搭建', 'pip', 'venv', 'pyenv', '開發工具', 'IDE配置'],
      'en': ['Python introduction', 'environment setup', 'pip', 'venv', 'pyenv', 'development tools', 'IDE configuration']
    },
    'module-01-syntax-comparison': {
      'zh-cn': ['语法对比', '变量声明', '数据类型', '控制流', '函数定义', '语法映射'],
      'zh-tw': ['語法對比', '變數宣告', '資料類型', '控制流', '函數定義', '語法映射'],
      'en': ['syntax comparison', 'variable declaration', 'data types', 'control flow', 'function definition', 'syntax mapping']
    },
    'module-02-module-system': {
      'zh-cn': ['模块系统', '包管理', '项目组织', '导入导出', '命名空间'],
      'zh-tw': ['模組系統', '套件管理', '專案組織', '匯入匯出', '命名空間'],
      'en': ['module system', 'package management', 'project organization', 'import export', 'namespace']
    },
    'module-03-oop-functional': {
      'zh-cn': ['面向对象', '函数式编程', '类', '继承', '多态', '装饰器'],
      'zh-tw': ['物件導向', '函數式程式設計', '類別', '繼承', '多型', '裝飾器'],
      'en': ['object-oriented', 'functional programming', 'classes', 'inheritance', 'polymorphism', 'decorators']
    },
    'module-04-async-programming': {
      'zh-cn': ['异步编程', 'async/await', '事件循环', '协程', '并发处理'],
      'zh-tw': ['非同步程式設計', 'async/await', '事件迴圈', '協程', '並發處理'],
      'en': ['async programming', 'async/await', 'event loop', 'coroutines', 'concurrency']
    },
    'module-05-quality-testing-typing': {
      'zh-cn': ['代码质量', '单元测试', '类型注解', 'mypy', 'pytest', '调试技巧'],
      'zh-tw': ['程式碼品質', '單元測試', '型別註解', 'mypy', 'pytest', '除錯技巧'],
      'en': ['code quality', 'unit testing', 'type annotations', 'mypy', 'pytest', 'debugging']
    },
    'module-06-web-development': {
      'zh-cn': ['Web开发', 'FastAPI', '后端API', 'RESTful', '数据库', '中间件'],
      'zh-tw': ['Web開發', 'FastAPI', '後端API', 'RESTful', '資料庫', '中介軟體'],
      'en': ['web development', 'FastAPI', 'backend API', 'RESTful', 'database', 'middleware']
    },
    'module-07-data-automation': {
      'zh-cn': ['数据处理', 'pandas', '自动化脚本', '数据分析', '爬虫', 'Excel处理'],
      'zh-tw': ['資料處理', 'pandas', '自動化腳本', '資料分析', '爬蟲', 'Excel處理'],
      'en': ['data processing', 'pandas', 'automation scripts', 'data analysis', 'web scraping', 'Excel processing']
    },
    'module-08-projects': {
      'zh-cn': ['实战项目', '综合应用', '项目开发', '最佳实践', '项目部署'],
      'zh-tw': ['實戰專案', '綜合應用', '專案開發', '最佳實踐', '專案部署'],
      'en': ['real projects', 'comprehensive application', 'project development', 'best practices', 'project deployment']
    },
    'module-09-advanced-topics': {
      'zh-cn': ['进阶主题', 'AI应用', '机器学习', '深度学习', '自动化', '学习资源'],
      'zh-tw': ['進階主題', 'AI應用', '機器學習', '深度學習', '自動化', '學習資源'],
      'en': ['advanced topics', 'AI applications', 'machine learning', 'deep learning', 'automation', 'learning resources']
    },
    'module-10-common-pitfalls': {
      'zh-cn': ['常见陷阱', '错误处理', '调试技巧', '最佳实践', '避免错误'],
      'zh-tw': ['常見陷阱', '錯誤處理', '除錯技巧', '最佳實踐', '避免錯誤'],
      'en': ['common pitfalls', 'error handling', 'debugging techniques', 'best practices', 'error prevention']
    },
    'module-11-pythonic-code': {
      'zh-cn': ['Pythonic代码', '代码风格', 'PEP8', '代码优化', 'Python最佳实践'],
      'zh-tw': ['Pythonic程式碼', '程式碼風格', 'PEP8', '程式碼優化', 'Python最佳實踐'],
      'en': ['pythonic code', 'code style', 'PEP8', 'code optimization', 'Python best practices']
    },
    'module-12-type-annotations': {
      'zh-cn': ['类型注解', '类型提示', 'mypy', '静态类型检查', '类型系统'],
      'zh-tw': ['型別註解', '型別提示', 'mypy', '靜態型別檢查', '型別系統'],
      'en': ['type annotations', 'type hints', 'mypy', 'static type checking', 'type system']
    }
  },
  'js2rust': {
    'module-00-rust-introduction': {
      'zh-cn': ['Rust介绍', '内存安全', '零成本抽象', '系统编程', '开发环境'],
      'zh-tw': ['Rust介紹', '記憶體安全', '零成本抽象', '系統程式設計', '開發環境'],
      'en': ['Rust introduction', 'memory safety', 'zero-cost abstractions', 'systems programming', 'development environment']
    },
    'module-01-syntax-comparison': {
      'zh-cn': ['语法对比', '变量绑定', '数据类型', '控制流', '函数定义'],
      'zh-tw': ['語法對比', '變數綁定', '資料類型', '控制流', '函數定義'],
      'en': ['syntax comparison', 'variable binding', 'data types', 'control flow', 'function definition']
    },
    'module-02-module-system': {
      'zh-cn': ['模块系统', '包管理', 'Cargo', '项目组织', '可见性'],
      'zh-tw': ['模組系統', '套件管理', 'Cargo', '專案組織', '可見性'],
      'en': ['module system', 'package management', 'Cargo', 'project organization', 'visibility']
    },
    'module-03-ownership-memory': {
      'zh-cn': ['所有权系统', '内存管理', '借用检查', '生命周期', '内存安全'],
      'zh-tw': ['所有權系統', '記憶體管理', '借用檢查', '生命週期', '記憶體安全'],
      'en': ['ownership system', 'memory management', 'borrow checker', 'lifetimes', 'memory safety']
    },
    'module-04-concurrency-async': {
      'zh-cn': ['并发编程', '异步编程', 'async/await', '线程安全', '消息传递'],
      'zh-tw': ['並發程式設計', '非同步程式設計', 'async/await', '執行緒安全', '訊息傳遞'],
      'en': ['concurrent programming', 'async programming', 'async/await', 'thread safety', 'message passing']
    },
    'module-05-type-system-traits': {
      'zh-cn': ['类型系统', '特征系统', '泛型', '约束', '实现'],
      'zh-tw': ['型別系統', '特徵系統', '泛型', '約束', '實作'],
      'en': ['type system', 'traits', 'generics', 'bounds', 'implementations']
    },
    'module-06-error-handling': {
      'zh-cn': ['错误处理', 'Result类型', 'Option类型', '错误传播', '异常处理'],
      'zh-tw': ['錯誤處理', 'Result型別', 'Option型別', '錯誤傳播', '異常處理'],
      'en': ['error handling', 'Result type', 'Option type', 'error propagation', 'exception handling']
    },
    'module-07-web-development': {
      'zh-cn': ['Web开发', 'Actix-web', '后端API', 'WebAssembly', '前端集成'],
      'zh-tw': ['Web開發', 'Actix-web', '後端API', 'WebAssembly', '前端整合'],
      'en': ['web development', 'Actix-web', 'backend API', 'WebAssembly', 'frontend integration']
    },
    'module-08-systems-programming': {
      'zh-cn': ['系统编程', '底层编程', 'FFI', '性能优化', '系统调用'],
      'zh-tw': ['系統程式設計', '底層程式設計', 'FFI', '效能優化', '系統呼叫'],
      'en': ['systems programming', 'low-level programming', 'FFI', 'performance optimization', 'system calls']
    },
    'module-09-projects': {
      'zh-cn': ['实战项目', '综合应用', '项目开发', '最佳实践', '性能优化'],
      'zh-tw': ['實戰專案', '綜合應用', '專案開發', '最佳實踐', '效能優化'],
      'en': ['real projects', 'comprehensive application', 'project development', 'best practices', 'performance optimization']
    },
    'module-10-common-pitfalls': {
      'zh-cn': ['常见陷阱', '借用检查', '生命周期', '错误处理', '调试技巧'],
      'zh-tw': ['常見陷阱', '借用檢查', '生命週期', '錯誤處理', '除錯技巧'],
      'en': ['common pitfalls', 'borrow checker', 'lifetimes', 'error handling', 'debugging techniques']
    },
    'module-11-idiomatic-rust-style': {
      'zh-cn': ['Rust风格', '代码规范', '最佳实践', '惯用写法', '代码优化'],
      'zh-tw': ['Rust風格', '程式碼規範', '最佳實踐', '慣用寫法', '程式碼優化'],
      'en': ['Rust style', 'code conventions', 'best practices', 'idiomatic code', 'code optimization']
    }
  }
};

// 页面类型关键词
export const pageTypeKeywords = {
  'index': {
    'zh-cn': ['课程介绍', '学习路径', '课程大纲', '学习目标'],
    'zh-tw': ['課程介紹', '學習路徑', '課程大綱', '學習目標'],
    'en': ['course introduction', 'learning path', 'course outline', 'learning objectives']
  },
  'intro': {
    'zh-cn': ['入门指南', '快速开始', '环境配置', '学习准备'],
    'zh-tw': ['入門指南', '快速開始', '環境配置', '學習準備'],
    'en': ['getting started', 'quick start', 'environment setup', 'learning preparation']
  }
};

/**
 * 生成页面的 SEO 关键词
 * @param course 课程名称 (js2py, js2rust)
 * @param moduleName 模块名称
 * @param lang 语言
 * @param pageType 页面类型
 * @returns 关键词数组
 */
export function generatePageKeywords(
  course?: string,
  moduleName?: string,
  lang: string = 'zh-cn',
  pageType?: string
): string[] {
  const keywords: string[] = [];
  
  // 添加基础关键词
  keywords.push(...baseKeywords[lang as keyof typeof baseKeywords] || baseKeywords['zh-cn']);
  
  // 添加课程特定关键词
  if (course && courseKeywords[course as keyof typeof courseKeywords]) {
    keywords.push(...courseKeywords[course as keyof typeof courseKeywords][lang as keyof typeof courseKeywords['js2py']] || []);
  }
  
  // 添加模块特定关键词
  if (course && moduleName && moduleKeywords[course as keyof typeof moduleKeywords]) {
    const courseModules = moduleKeywords[course as keyof typeof moduleKeywords];
    const moduleData = courseModules[moduleName as keyof typeof courseModules];
    if (moduleData && typeof moduleData === 'object' && lang in moduleData) {
      keywords.push(...(moduleData as any)[lang] || []);
    }
  }
  
  // 添加页面类型关键词
  if (pageType && pageTypeKeywords[pageType as keyof typeof pageTypeKeywords]) {
    keywords.push(...pageTypeKeywords[pageType as keyof typeof pageTypeKeywords][lang as keyof typeof pageTypeKeywords['index']] || []);
  }
  
  // 去重并限制数量
  return [...new Set(keywords)].slice(0, 15);
}

/**
 * 根据页面路径生成关键词
 * @param slug 页面路径
 * @param lang 语言
 * @returns 关键词数组
 */
export function generateKeywordsFromSlug(slug?: string[], lang: string = 'zh-cn'): string[] {
  if (!slug || slug.length === 0) {
    return generatePageKeywords(undefined, undefined, lang, 'index');
  }
  
  const course = slug[0]; // 第一个路径段通常是课程名称
  const moduleName = slug.length > 1 ? slug[1] : undefined;
  
  return generatePageKeywords(course, moduleName, lang);
} 