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
  },
  'js2cpp': {
    'zh-cn': [
      'JavaScript', 'C++', '系统编程', '内存管理', '性能优化',
      'STL', '现代C++', 'C++11', 'C++17', 'C++20', '模板', 'RAII',
      '智能指针', '面向对象', '底层编程'
    ],
    'zh-tw': [
      'JavaScript', 'C++', '系統編程', '內存管理', '性能優化',
      'STL', '現代C++', 'C++11', 'C++17', 'C++20', '模板', 'RAII',
      '智能指針', '面向對象', '底層編程'
    ],
    'en': [
      'JavaScript', 'C++', 'systems programming', 'memory management', 
      'performance optimization', 'STL', 'Modern C++', 'C++11', 'C++17', 
      'C++20', 'templates', 'RAII', 'smart pointers', 'object-oriented', 'low-level programming'
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
  },
  'js2cpp': {
    'module-00-cpp-introduction': {
      'zh-cn': ['C++介绍', '历史', '设计哲学', '编译型', '解释型', '应用场景', '开发环境', '第一个程序', '编译链接'],
      'zh-tw': ['C++介紹', '歷史', '設計哲學', '編譯型', '解釋型', '應用場景', '開發環境', '第一個程式', '編譯連結'],
      'en': ['C++ introduction', 'history', 'design philosophy', 'compiled', 'interpreted', 'use cases', 'development environment', 'first program', 'compilation linking']
    },
    'module-01-syntax-comparison': {
      'zh-cn': ['语法对比', '变量声明', '类型系统', '控制流', '函数', '作用域', '生命周期', '数据类型', '运算符', '命名空间'],
      'zh-tw': ['語法對比', '變數宣告', '類型系統', '控制流', '函式', '作用域', '生命週期', '資料類型', '運算符', '命名空間'],
      'en': ['syntax comparison', 'variable declaration', 'type system', 'control flow', 'functions', 'scope', 'lifetime', 'data types', 'operators', 'namespaces']
    },
    'module-02-memory-management': {
      'zh-cn': ['内存管理', '栈内存', '堆内存', '内存泄漏', 'RAII', '内存布局', '调试内存'],
      'zh-tw': ['記憶體管理', '堆疊記憶體', '堆積記憶體', '記憶體洩漏', 'RAII', '記憶體佈局', '偵錯記憶體'],
      'en': ['memory management', 'stack memory', 'heap memory', 'memory leak', 'RAII', 'memory layout', 'debug memory']
    },
    'module-03-pointers-references': {
      'zh-cn': ['指针', '引用', '指针算术', '函数指针', '指针错误', 'JavaScript引用对比'],
      'zh-tw': ['指標', '引用', '指標算術', '函式指標', '指標錯誤', 'JavaScript引用對比'],
      'en': ['pointers', 'references', 'pointer arithmetic', 'function pointers', 'pointer errors', 'JavaScript reference comparison']
    },
    'module-04-oop-basics': {
      'zh-cn': ['面向对象', '类', '对象', '构造函数', '析构函数', '封装', '继承', '多态', '虚函数', '原型链对比'],
      'zh-tw': ['物件導向', '類別', '物件', '建構函式', '解構函式', '封裝', '繼承', '多型', '虛擬函式', '原型鏈對比'],
      'en': ['object-oriented programming', 'classes', 'objects', 'constructors', 'destructors', 'encapsulation', 'inheritance', 'polymorphism', 'virtual functions', 'prototype chain comparison']
    },
    'module-05-templates-generics': {
      'zh-cn': ['模板', '泛型编程', '函数模板', '类模板', '模板特化', '可变参数模板', '模板元编程', 'JavaScript泛型对比'],
      'zh-tw': ['模板', '泛型程式設計', '函式模板', '類別模板', '模板特化', '可變參數模板', '模板元程式設計', 'JavaScript泛型對比'],
      'en': ['templates', 'generic programming', 'function templates', 'class templates', 'template specialization', 'variadic templates', 'template metaprogramming', 'JavaScript generics comparison']
    },
    'module-06-stl-containers': {
      'zh-cn': ['STL容器', 'vector', 'list', 'deque', 'map', 'set', 'unordered_map', '容器适配器', '迭代器', '容器性能', 'JavaScript数组对象对比'],
      'zh-tw': ['STL容器', 'vector', 'list', 'deque', 'map', 'set', 'unordered_map', '容器適配器', '迭代器', '容器性能', 'JavaScript陣列物件對比'],
      'en': ['STL containers', 'vector', 'list', 'deque', 'map', 'set', 'unordered_map', 'container adapters', 'iterators', 'container performance', 'JavaScript array object comparison']
    },
    'module-07-stl-algorithms': {
      'zh-cn': ['STL算法', '排序算法', '查找算法', '修改算法', '数值算法', 'JavaScript数组方法对比', '算法性能'],
      'zh-tw': ['STL演算法', '排序演算法', '查找演算法', '修改演算法', '數值演算法', 'JavaScript陣列方法對比', '演算法性能'],
      'en': ['STL algorithms', 'sorting algorithms', 'searching algorithms', 'modifying algorithms', 'numeric algorithms', 'JavaScript array methods comparison', 'algorithm performance']
    },
    'module-08-error-handling': {
      'zh-cn': ['错误处理', '异常', 'try-catch-throw', '异常安全', '错误码', 'RAII', 'JavaScript错误处理对比', '异常陷阱'],
      'zh-tw': ['錯誤處理', '例外', 'try-catch-throw', '例外安全', '錯誤碼', 'RAII', 'JavaScript錯誤處理對比', '例外陷阱'],
      'en': ['error handling', 'exceptions', 'try-catch-throw', 'exception safety', 'error codes', 'RAII', 'JavaScript error handling comparison', 'exception pitfalls']
    },
    'module-09-smart-pointers': {
      'zh-cn': ['智能指针', 'unique_ptr', 'shared_ptr', 'weak_ptr', '原始指针', '循环引用', 'JavaScript垃圾回收对比'],
      'zh-tw': ['智能指標', 'unique_ptr', 'shared_ptr', 'weak_ptr', '原始指標', '循環引用', 'JavaScript垃圾回收對比'],
      'en': ['smart pointers', 'unique_ptr', 'shared_ptr', 'weak_ptr', 'raw pointers', 'circular references', 'JavaScript garbage collection comparison']
    },
    'module-10-performance-optimization': {
      'zh-cn': ['性能优化', '编译器优化', '内存布局', '内联函数', '模板优化', '缓存友好', '性能分析工具', 'JavaScript性能对比'],
      'zh-tw': ['效能優化', '編譯器優化', '記憶體佈局', '內聯函式', '模板優化', '快取友好', '效能分析工具', 'JavaScript效能對比'],
      'en': ['performance optimization', 'compiler optimization', 'memory layout', 'inline functions', 'template optimization', 'cache-friendly', 'performance analysis tools', 'JavaScript performance comparison']
    },
    'module-11-modern-cpp-features': {
      'zh-cn': ['现代C++', 'C++11', 'C++14', 'C++17', 'C++20', 'Lambda', '移动语义', '右值引用', 'auto', '范围for', '初始化列表', 'JavaScript现代特性对比'],
      'zh-tw': ['現代C++', 'C++11', 'C++14', 'C++17', 'C++20', 'Lambda', '移動語義', '右值引用', 'auto', '範圍for', '初始化列表', 'JavaScript現代特性對比'],
      'en': ['Modern C++', 'C++11', 'C++14', 'C++17', 'C++20', 'Lambda expressions', 'move semantics', 'rvalue references', 'auto keyword', 'range-based for loop', 'initializer lists', 'JavaScript modern features comparison']
    },
    'module-12-concurrency-multithreading': {
      'zh-cn': ['并发', '多线程', 'std::thread', '互斥锁', '条件变量', '原子操作', '异步编程', '线程池', 'JavaScript异步编程对比'],
      'zh-tw': ['並發', '多執行緒', 'std::thread', '互斥鎖', '條件變數', '原子操作', '非同步程式設計', '執行緒池', 'JavaScript非同步程式設計對比'],
      'en': ['concurrency', 'multithreading', 'std::thread', 'mutexes', 'condition variables', 'atomic operations', 'asynchronous programming', 'thread pool', 'JavaScript async programming comparison']
    },
    'module-13-systems-programming': {
      'zh-cn': ['系统编程', '文件I/O', '网络编程', '进程间通信', '系统调用', '跨平台开发', '底层内存操作', 'JavaScript系统编程对比'],
      'zh-tw': ['系統程式設計', '檔案I/O', '網路程式設計', '行程間通訊', '系統呼叫', '跨平台開發', '低階記憶體操作', 'JavaScript系統程式設計對比'],
      'en': ['systems programming', 'file I/O', 'network programming', 'inter-process communication', 'system calls', 'cross-platform development', 'low-level memory operations', 'JavaScript systems programming comparison']
    },
    'module-14-projects': {
      'zh-cn': ['实战项目', '数据处理系统', '游戏引擎', '系统工具', '网络服务器', '项目架构', '性能优化实践', '部署'],
      'zh-tw': ['實戰專案', '資料處理系統', '遊戲引擎', '系統工具', '網路伺服器', '專案架構', '效能優化實踐', '部署'],
      'en': ['practical projects', 'data processing system', 'game engine', 'system tools', 'network server', 'project architecture', 'performance optimization practices', 'deployment']
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