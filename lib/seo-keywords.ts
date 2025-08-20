// SEO 关键词映射配置
// 为不同页面和模块定义合适的关键词

export interface SEOKeywords {
  [key: string]: string[];
}

// 基础关键词（所有页面通用）- 优化版本
export const baseKeywords = {
  'zh-cn': [
    // 高搜索量核心关键词
    '编程语言学习', '编程教程', '代码学习', '编程入门', '编程基础',
    '在线编程学习', '编程教育平台', '编程技能提升', '编程语言对比',
    // 长尾关键词
    '如何学习编程', '编程语言选择', '编程学习路径', '编程学习方法',
    '编程语言转换', '代码语法对比', '编程语言迁移', '开发者技能提升',
    // 热门搜索词
    '编程培训', '编程课程', '编程实战', '编程项目', '编程练习'
  ],
  'zh-tw': [
    // 高搜索量核心关键词
    '程式語言學習', '程式設計教程', '程式碼學習', '程式設計入門', '程式設計基礎',
    '線上程式設計學習', '程式設計教育平台', '程式設計技能提升', '程式語言對比',
    // 长尾关键词
    '如何學習程式設計', '程式語言選擇', '程式設計學習路徑', '程式設計學習方法',
    '程式語言轉換', '程式碼語法對比', '程式語言遷移', '開發者技能提升',
    // 热门搜索词
    '程式設計培訓', '程式設計課程', '程式設計實戰', '程式設計專案', '程式設計練習'
  ],
  'en': [
    // 高搜索量核心关键词
    'programming language learning', 'coding tutorials', 'learn to code', 'programming for beginners', 'coding basics',
    'online programming courses', 'programming education platform', 'coding skills development', 'programming language comparison',
    // 长尾关键词
    'how to learn programming', 'programming language selection', 'coding learning path', 'programming learning methods',
    'programming language conversion', 'code syntax comparison', 'programming language migration', 'developer skills improvement',
    // 热门搜索词
    'coding bootcamp', 'programming courses', 'coding projects', 'programming practice', 'learn programming online'
  ]
};

// 课程特定关键词 - 优化版本
export const courseKeywords = {
  'js2py': {
    'zh-cn': [
      // 高搜索量核心关键词
      'JavaScript转Python', 'JS转Python教程', 'Python编程教程', 'Python入门教程',
      'Python基础教程', 'Python学习', 'Python编程', 'Python开发',
      // 长尾关键词
      'JavaScript开发者学Python', '前端转Python', 'Web开发转Python', 'Python后端开发',
      'Python数据处理', 'Python自动化', 'Python爬虫', 'Python机器学习',
      // 热门搜索词
      'Python培训', 'Python课程', 'Python实战', 'Python项目', 'Python练习',
      'Python面试', 'Python就业', 'Python薪资', 'Python前景'
    ],
    'zh-tw': [
      // 高搜索量核心关键词
      'JavaScript轉Python', 'JS轉Python教程', 'Python程式設計教程', 'Python入門教程',
      'Python基礎教程', 'Python學習', 'Python程式設計', 'Python開發',
      // 长尾关键词
      'JavaScript開發者學Python', '前端轉Python', 'Web開發轉Python', 'Python後端開發',
      'Python資料處理', 'Python自動化', 'Python爬蟲', 'Python機器學習',
      // 热门搜索词
      'Python培訓', 'Python課程', 'Python實戰', 'Python專案', 'Python練習',
      'Python面試', 'Python就業', 'Python薪資', 'Python前景'
    ],
    'en': [
      // 高搜索量核心关键词
      'JavaScript to Python', 'JS to Python tutorial', 'Python programming tutorial', 'Python for beginners',
      'Python basics tutorial', 'learn Python', 'Python programming', 'Python development',
      // 长尾关键词
      'JavaScript developer learning Python', 'frontend to Python', 'Web development to Python', 'Python backend development',
      'Python data processing', 'Python automation', 'Python web scraping', 'Python machine learning',
      // 热门搜索词
      'Python training', 'Python courses', 'Python projects', 'Python practice', 'Python interview',
      'Python career', 'Python salary', 'Python job opportunities'
    ]
  },
  'js2rust': {
    'zh-cn': [
      // 高搜索量核心关键词
      'JavaScript转Rust', 'JS转Rust教程', 'Rust编程教程', 'Rust入门教程',
      'Rust基础教程', 'Rust学习', 'Rust编程', 'Rust开发',
      // 长尾关键词
      'JavaScript开发者学Rust', '前端转Rust', 'Web开发转Rust', 'Rust系统编程',
      'Rust内存安全', 'Rust并发编程', 'Rust性能优化', 'Rust WebAssembly',
      // 热门搜索词
      'Rust培训', 'Rust课程', 'Rust实战', 'Rust项目', 'Rust练习',
      'Rust面试', 'Rust就业', 'Rust薪资', 'Rust前景'
    ],
    'zh-tw': [
      // 高搜索量核心关键词
      'JavaScript轉Rust', 'JS轉Rust教程', 'Rust程式設計教程', 'Rust入門教程',
      'Rust基礎教程', 'Rust學習', 'Rust程式設計', 'Rust開發',
      // 长尾关键词
      'JavaScript開發者學Rust', '前端轉Rust', 'Web開發轉Rust', 'Rust系統程式設計',
      'Rust記憶體安全', 'Rust並發程式設計', 'Rust效能優化', 'Rust WebAssembly',
      // 热门搜索词
      'Rust培訓', 'Rust課程', 'Rust實戰', 'Rust專案', 'Rust練習',
      'Rust面試', 'Rust就業', 'Rust薪資', 'Rust前景'
    ],
    'en': [
      // 高搜索量核心关键词
      'JavaScript to Rust', 'JS to Rust tutorial', 'Rust programming tutorial', 'Rust for beginners',
      'Rust basics tutorial', 'learn Rust', 'Rust programming', 'Rust development',
      // 长尾关键词
      'JavaScript developer learning Rust', 'frontend to Rust', 'Web development to Rust', 'Rust systems programming',
      'Rust memory safety', 'Rust concurrent programming', 'Rust performance optimization', 'Rust WebAssembly',
      // 热门搜索词
      'Rust training', 'Rust courses', 'Rust projects', 'Rust practice', 'Rust interview',
      'Rust career', 'Rust salary', 'Rust job opportunities'
    ]
  },
  'js2cpp': {
    'zh-cn': [
      // 高搜索量核心关键词
      'JavaScript转C++', 'JS转C++教程', 'C++编程教程', 'C++入门教程',
      'C++基础教程', 'C++学习', 'C++编程', 'C++开发',
      // 长尾关键词
      'JavaScript开发者学C++', '前端转C++', 'Web开发转C++', 'C++系统编程',
      'C++内存管理', 'C++性能优化', 'C++游戏开发', 'C++底层编程',
      // 热门搜索词
      'C++培训', 'C++课程', 'C++实战', 'C++项目', 'C++练习',
      'C++面试', 'C++就业', 'C++薪资', 'C++前景'
    ],
    'zh-tw': [
      // 高搜索量核心关键词
      'JavaScript轉C++', 'JS轉C++教程', 'C++程式設計教程', 'C++入門教程',
      'C++基礎教程', 'C++學習', 'C++程式設計', 'C++開發',
      // 长尾关键词
      'JavaScript開發者學C++', '前端轉C++', 'Web開發轉C++', 'C++系統程式設計',
      'C++記憶體管理', 'C++效能優化', 'C++遊戲開發', 'C++底層程式設計',
      // 热门搜索词
      'C++培訓', 'C++課程', 'C++實戰', 'C++專案', 'C++練習',
      'C++面試', 'C++就業', 'C++薪資', 'C++前景'
    ],
    'en': [
      // 高搜索量核心关键词
      'JavaScript to C++', 'JS to C++ tutorial', 'C++ programming tutorial', 'C++ for beginners',
      'C++ basics tutorial', 'learn C++', 'C++ programming', 'C++ development',
      // 长尾关键词
      'JavaScript developer learning C++', 'frontend to C++', 'Web development to C++', 'C++ systems programming',
      'C++ memory management', 'C++ performance optimization', 'C++ game development', 'C++ low-level programming',
      // 热门搜索词
      'C++ training', 'C++ courses', 'C++ projects', 'C++ practice', 'C++ interview',
      'C++ career', 'C++ salary', 'C++ job opportunities'
    ]
  },
  'js2go': {
    'zh-cn': [
      // 高搜索量核心关键词
      'JavaScript转Go', 'JS转Go教程', 'Go编程教程', 'Go入门教程',
      'Go基础教程', 'Go学习', 'Go编程', 'Go开发', 'Golang教程',
      // 长尾关键词
      'JavaScript开发者学Go', '前端转Go', 'Web开发转Go', 'Go并发编程',
      'Go微服务', 'Go云原生', 'Go性能优化', 'Go Web开发',
      // 热门搜索词
      'Go培训', 'Go课程', 'Go实战', 'Go项目', 'Go练习',
      'Go面试', 'Go就业', 'Go薪资', 'Go前景'
    ],
    'zh-tw': [
      // 高搜索量核心关键词
      'JavaScript轉Go', 'JS轉Go教程', 'Go程式設計教程', 'Go入門教程',
      'Go基礎教程', 'Go學習', 'Go程式設計', 'Go開發', 'Golang教程',
      // 长尾关键词
      'JavaScript開發者學Go', '前端轉Go', 'Web開發轉Go', 'Go並發程式設計',
      'Go微服務', 'Go雲原生', 'Go效能優化', 'Go Web開發',
      // 热门搜索词
      'Go培訓', 'Go課程', 'Go實戰', 'Go專案', 'Go練習',
      'Go面試', 'Go就業', 'Go薪資', 'Go前景'
    ],
    'en': [
      // 高搜索量核心关键词
      'JavaScript to Go', 'JS to Go tutorial', 'Go programming tutorial', 'Go for beginners',
      'Go basics tutorial', 'learn Go', 'Go programming', 'Go development', 'Golang tutorial',
      // 长尾关键词
      'JavaScript developer learning Go', 'frontend to Go', 'Web development to Go', 'Go concurrent programming',
      'Go microservices', 'Go cloud-native', 'Go performance optimization', 'Go web development',
      // 热门搜索词
      'Go training', 'Go courses', 'Go projects', 'Go practice', 'Go interview',
      'Go career', 'Go salary', 'Go job opportunities'
    ]
  },
  'js2swift': {
    'zh-cn': [
      // 高搜索量核心关键词
      'JavaScript转Swift', 'JS转Swift教程', 'Swift编程教程', 'Swift入门教程',
      'Swift基础教程', 'Swift学习', 'Swift编程', 'Swift开发', 'iOS开发教程',
      // 长尾关键词
      'JavaScript开发者学Swift', '前端转Swift', 'Web开发转Swift', 'Swift iOS开发',
      'Swift移动开发', 'Swift类型安全', 'SwiftUI教程', 'Swift性能优化',
      // 热门搜索词
      'Swift培训', 'Swift课程', 'Swift实战', 'Swift项目', 'Swift练习',
      'Swift面试', 'Swift就业', 'Swift薪资', 'Swift前景'
    ],
    'zh-tw': [
      // 高搜索量核心关键词
      'JavaScript轉Swift', 'JS轉Swift教程', 'Swift程式設計教程', 'Swift入門教程',
      'Swift基礎教程', 'Swift學習', 'Swift程式設計', 'Swift開發', 'iOS開發教程',
      // 长尾关键词
      'JavaScript開發者學Swift', '前端轉Swift', 'Web開發轉Swift', 'Swift iOS開發',
      'Swift移動開發', 'Swift型別安全', 'SwiftUI教程', 'Swift效能優化',
      // 热门搜索词
      'Swift培訓', 'Swift課程', 'Swift實戰', 'Swift專案', 'Swift練習',
      'Swift面試', 'Swift就業', 'Swift薪資', 'Swift前景'
    ],
    'en': [
      // 高搜索量核心关键词
      'JavaScript to Swift', 'JS to Swift tutorial', 'Swift programming tutorial', 'Swift for beginners',
      'Swift basics tutorial', 'learn Swift', 'Swift programming', 'Swift development', 'iOS development tutorial',
      // 长尾关键词
      'JavaScript developer learning Swift', 'frontend to Swift', 'Web development to Swift', 'Swift iOS development',
      'Swift mobile development', 'Swift type safety', 'SwiftUI tutorial', 'Swift performance optimization',
      // 热门搜索词
      'Swift training', 'Swift courses', 'Swift projects', 'Swift practice', 'Swift interview',
      'Swift career', 'Swift salary', 'Swift job opportunities'
    ]
  },
  'js2c': {
    'zh-cn': [
      // 高搜索量核心关键词
      'JavaScript转C语言', 'JS转C教程', 'C语言编程教程', 'C语言入门教程',
      'C语言基础教程', 'C语言学习', 'C语言编程', 'C语言开发',
      // 长尾关键词
      'JavaScript开发者学C语言', '前端转C语言', 'Web开发转C语言', 'C语言系统编程',
      'C语言内存管理', 'C语言指针', 'C语言底层编程', 'C语言性能优化',
      // 热门搜索词
      'C语言培训', 'C语言课程', 'C语言实战', 'C语言项目', 'C语言练习',
      'C语言面试', 'C语言就业', 'C语言薪资', 'C语言前景'
    ],
    'zh-tw': [
      // 高搜索量核心关键词
      'JavaScript轉C語言', 'JS轉C教程', 'C語言程式設計教程', 'C語言入門教程',
      'C語言基礎教程', 'C語言學習', 'C語言程式設計', 'C語言開發',
      // 长尾关键词
      'JavaScript開發者學C語言', '前端轉C語言', 'Web開發轉C語言', 'C語言系統程式設計',
      'C語言記憶體管理', 'C語言指標', 'C語言底層程式設計', 'C語言效能優化',
      // 热门搜索词
      'C語言培訓', 'C語言課程', 'C語言實戰', 'C語言專案', 'C語言練習',
      'C語言面試', 'C語言就業', 'C語言薪資', 'C語言前景'
    ],
    'en': [
      // 高搜索量核心关键词
      'JavaScript to C', 'JS to C tutorial', 'C programming tutorial', 'C for beginners',
      'C basics tutorial', 'learn C programming', 'C programming', 'C development',
      // 长尾关键词
      'JavaScript developer learning C', 'frontend to C', 'Web development to C', 'C systems programming',
      'C memory management', 'C pointers', 'C low-level programming', 'C performance optimization',
      // 热门搜索词
      'C training', 'C courses', 'C projects', 'C practice', 'C interview',
      'C career', 'C salary', 'C job opportunities'
    ]
  },
  'js2kotlin': {
    'zh-cn': [
      // 高搜索量核心关键词
      'JavaScript转Kotlin', 'JS转Kotlin教程', 'Kotlin编程教程', 'Kotlin入门教程',
      'Kotlin基础教程', 'Kotlin学习', 'Kotlin编程', 'Kotlin开发',
      // 长尾关键词
      'JavaScript开发者学Kotlin', '前端转Kotlin', 'Web开发转Kotlin', 'Kotlin协程编程',
      'Kotlin Android开发', 'Kotlin函数式编程', 'Kotlin JVM生态', 'Kotlin跨平台开发',
      // 热门搜索词
      'Kotlin培训', 'Kotlin课程', 'Kotlin实战', 'Kotlin项目', 'Kotlin练习',
      'Kotlin面试', 'Kotlin就业', 'Kotlin薪资', 'Kotlin前景'
    ],
    'zh-tw': [
      // 高搜索量核心关键词
      'JavaScript轉Kotlin', 'JS轉Kotlin教程', 'Kotlin程式設計教程', 'Kotlin入門教程',
      'Kotlin基礎教程', 'Kotlin學習', 'Kotlin程式設計', 'Kotlin開發',
      // 长尾关键词
      'JavaScript開發者學Kotlin', '前端轉Kotlin', 'Web開發轉Kotlin', 'Kotlin協程程式設計',
      'Kotlin Android開發', 'Kotlin函數式程式設計', 'Kotlin JVM生態', 'Kotlin跨平台開發',
      // 热门搜索词
      'Kotlin培訓', 'Kotlin課程', 'Kotlin實戰', 'Kotlin專案', 'Kotlin練習',
      'Kotlin面試', 'Kotlin就業', 'Kotlin薪資', 'Kotlin前景'
    ],
    'en': [
      // 高搜索量核心关键词
      'JavaScript to Kotlin', 'JS to Kotlin tutorial', 'Kotlin programming tutorial', 'Kotlin for beginners',
      'Kotlin basics tutorial', 'learn Kotlin', 'Kotlin programming', 'Kotlin development',
      // 长尾关键词
      'JavaScript developer learning Kotlin', 'frontend to Kotlin', 'Web development to Kotlin', 'Kotlin coroutines',
      'Kotlin Android development', 'Kotlin functional programming', 'Kotlin JVM ecosystem', 'Kotlin multiplatform',
      // 热门搜索词
      'Kotlin training', 'Kotlin courses', 'Kotlin projects', 'Kotlin practice', 'Kotlin interview',
      'Kotlin career', 'Kotlin salary', 'Kotlin job opportunities'
    ]
  },
  'py2js': {
    'zh-cn': [
      // 高搜索量核心关键词
      'Python转JavaScript', 'Python转JS教程', 'Python转前端', 'JavaScript前端开发',
      'Python转Web开发', 'Python学JavaScript', 'Python前端转换', 'Python全栈开发',
      // 长尾关键词
      'Python开发者学JavaScript', 'Python后端转前端', 'Python转前端开发', 'Python转异步编程',
      'Python转浏览器开发', 'Python转Node.js', 'Python转React', 'Python转Vue',
      // 热门搜索词
      'Python转前端培训', 'Python转JavaScript课程', 'Python全栈教程', 'Python前端学习',
      'Python转前端面试', 'Python转前端就业', 'Python转前端薪资', 'Python转前端发展'
    ],
    'zh-tw': [
      // 高搜索量核心关键词
      'Python轉JavaScript', 'Python轉JS教程', 'Python轉前端', 'JavaScript前端開發',
      'Python轉Web開發', 'Python學JavaScript', 'Python前端轉換', 'Python全端開發',
      // 长尾关键词
      'Python開發者學JavaScript', 'Python後端轉前端', 'Python轉前端開發', 'Python轉非同步程式設計',
      'Python轉瀏覽器開發', 'Python轉Node.js', 'Python轉React', 'Python轉Vue',
      // 热门搜索词
      'Python轉前端培訓', 'Python轉JavaScript課程', 'Python全端教程', 'Python前端學習',
      'Python轉前端面試', 'Python轉前端就業', 'Python轉前端薪資', 'Python轉前端發展'
    ],
    'en': [
      // 高搜索量核心关键词
      'Python to JavaScript', 'Python to JS tutorial', 'Python to frontend', 'JavaScript frontend development',
      'Python to web development', 'Python learn JavaScript', 'Python frontend conversion', 'Python full-stack development',
      // 长尾关键词
      'Python developer learning JavaScript', 'Python backend to frontend', 'Python to frontend development', 'Python to async programming',
      'Python to browser development', 'Python to Node.js', 'Python to React', 'Python to Vue',
      // 热门搜索词
      'Python to frontend training', 'Python to JavaScript courses', 'Python full-stack tutorial', 'Python frontend learning',
      'Python to frontend interview', 'Python to frontend career', 'Python to frontend salary', 'Python to frontend growth'
    ]
  }
};

// 模块特定关键词 - 优化版本
export const moduleKeywords = {
  'js2py': {
    'module-00-python-introduction': {
      'zh-cn': [
        // 高搜索量核心关键词
        'Python介绍', 'Python环境搭建', 'Python安装教程', 'Python开发环境配置',
        'Python入门指南', 'Python学习路线', 'Python基础教程', 'Python开发工具',
        // 长尾关键词
        'JavaScript开发者Python入门', '前端转Python环境配置', 'Python IDE配置教程',
        'Python pip安装教程', 'Python venv虚拟环境', 'Python pyenv版本管理',
        // 热门搜索词
        'Python培训', 'Python课程', 'Python实战', 'Python项目', 'Python练习'
      ],
      'zh-tw': [
        // 高搜索量核心关键词
        'Python介紹', 'Python環境搭建', 'Python安裝教程', 'Python開發環境配置',
        'Python入門指南', 'Python學習路線', 'Python基礎教程', 'Python開發工具',
        // 长尾关键词
        'JavaScript開發者Python入門', '前端轉Python環境配置', 'Python IDE配置教程',
        'Python pip安裝教程', 'Python venv虛擬環境', 'Python pyenv版本管理',
        // 热门搜索词
        'Python培訓', 'Python課程', 'Python實戰', 'Python專案', 'Python練習'
      ],
      'en': [
        // 高搜索量核心关键词
        'Python introduction', 'Python environment setup', 'Python installation tutorial', 'Python development environment',
        'Python getting started', 'Python learning path', 'Python basics tutorial', 'Python development tools',
        // 长尾关键词
        'JavaScript developer Python introduction', 'frontend to Python environment setup', 'Python IDE configuration',
        'Python pip installation', 'Python venv virtual environment', 'Python pyenv version management',
        // 热门搜索词
        'Python training', 'Python courses', 'Python projects', 'Python practice', 'Python learning'
      ]
    },
    'module-01-syntax-comparison': {
      'zh-cn': [
        // 高搜索量核心关键词
        'Python语法对比', 'JavaScript转Python语法', 'Python变量声明', 'Python数据类型',
        'Python控制流', 'Python函数定义', 'Python语法映射', 'Python基础语法',
        // 长尾关键词
        'JavaScript开发者Python语法', '前端转Python语法对比', 'Python变量类型声明',
        'Python循环语句教程', 'Python条件语句教程', 'Python函数参数教程',
        // 热门搜索词
        'Python语法教程', 'Python基础教程', 'Python入门教程', 'Python学习', 'Python编程'
      ],
      'zh-tw': [
        // 高搜索量核心关键词
        'Python語法對比', 'JavaScript轉Python語法', 'Python變數宣告', 'Python資料類型',
        'Python控制流', 'Python函數定義', 'Python語法映射', 'Python基礎語法',
        // 长尾关键词
        'JavaScript開發者Python語法', '前端轉Python語法對比', 'Python變數類型宣告',
        'Python迴圈語句教程', 'Python條件語句教程', 'Python函數參數教程',
        // 热门搜索词
        'Python語法教程', 'Python基礎教程', 'Python入門教程', 'Python學習', 'Python程式設計'
      ],
      'en': [
        // 高搜索量核心关键词
        'Python syntax comparison', 'JavaScript to Python syntax', 'Python variable declaration', 'Python data types',
        'Python control flow', 'Python function definition', 'Python syntax mapping', 'Python basic syntax',
        // 长尾关键词
        'JavaScript developer Python syntax', 'frontend to Python syntax comparison', 'Python variable type declaration',
        'Python loop statements tutorial', 'Python conditional statements tutorial', 'Python function parameters tutorial',
        // 热门搜索词
        'Python syntax tutorial', 'Python basics tutorial', 'Python getting started', 'Python learning', 'Python programming'
      ]
    },
    'module-02-module-system': {
      'zh-cn': [
        // 高搜索量核心关键词
        'Python模块系统', 'Python包管理', 'Python项目组织', 'Python导入导出',
        'Python命名空间', 'Python包结构', 'Python模块导入', 'Python包安装',
        // 长尾关键词
        'JavaScript开发者Python模块', '前端转Python包管理', 'Python pip包管理教程',
        'Python虚拟环境管理', 'Python项目结构组织', 'Python模块化开发',
        // 热门搜索词
        'Python模块教程', 'Python包管理教程', 'Python项目教程', 'Python开发教程', 'Python学习'
      ],
      'zh-tw': [
        // 高搜索量核心关键词
        'Python模組系統', 'Python套件管理', 'Python專案組織', 'Python匯入匯出',
        'Python命名空間', 'Python套件結構', 'Python模組匯入', 'Python套件安裝',
        // 长尾关键词
        'JavaScript開發者Python模組', '前端轉Python套件管理', 'Python pip套件管理教程',
        'Python虛擬環境管理', 'Python專案結構組織', 'Python模組化開發',
        // 热门搜索词
        'Python模組教程', 'Python套件管理教程', 'Python專案教程', 'Python開發教程', 'Python學習'
      ],
      'en': [
        // 高搜索量 core keywords
        'Python module system', 'Python package management', 'Python project organization', 'Python import export',
        'Python namespace', 'Python package structure', 'Python module import', 'Python package installation',
        // 长尾关键词
        'JavaScript developer Python modules', 'frontend to Python package management', 'Python pip package management',
        'Python virtual environment management', 'Python project structure organization', 'Python modular development',
        // 热门搜索词
        'Python module tutorial', 'Python package management tutorial', 'Python project tutorial', 'Python development tutorial', 'Python learning'
      ]
    },
    'module-03-oop-functional': {
      'zh-cn': [
        // 高搜索量核心关键词
        'Python面向对象', 'Python函数式编程', 'Python类定义', 'Python继承',
        'Python多态', 'Python装饰器', 'Python类方法', 'Python对象编程',
        // 长尾关键词
        'JavaScript开发者Python面向对象', '前端转Python类编程', 'Python类构造函数教程',
        'Python装饰器模式教程', 'Python函数式编程教程', 'Python继承多态教程',
        // 热门搜索词
        'Python面向对象教程', 'Python类教程', 'Python函数式编程教程', 'Python编程教程', 'Python学习'
      ],
      'zh-tw': [
        // 高搜索量核心关键词
        'Python物件導向', 'Python函數式程式設計', 'Python類別定義', 'Python繼承',
        'Python多型', 'Python裝飾器', 'Python類別方法', 'Python物件程式設計',
        // 长尾关键词
        'JavaScript開發者Python物件導向', '前端轉Python類別程式設計', 'Python類別建構函式教程',
        'Python裝飾器模式教程', 'Python函數式程式設計教程', 'Python繼承多型教程',
        // 热门搜索词
        'Python物件導向教程', 'Python類別教程', 'Python函數式程式設計教程', 'Python程式設計教程', 'Python學習'
      ],
      'en': [
        // 高搜索量核心关键词
        'Python object-oriented', 'Python functional programming', 'Python class definition', 'Python inheritance',
        'Python polymorphism', 'Python decorators', 'Python class methods', 'Python object programming',
        // 长尾关键词
        'JavaScript developer Python object-oriented', 'frontend to Python class programming', 'Python class constructor tutorial',
        'Python decorator pattern tutorial', 'Python functional programming tutorial', 'Python inheritance polymorphism tutorial',
        // 热门搜索词
        'Python object-oriented tutorial', 'Python class tutorial', 'Python functional programming tutorial', 'Python programming tutorial', 'Python learning'
      ]
    },
    'module-04-async-programming': {
      'zh-cn': [
        // 高搜索量核心关键词
        'Python异步编程', 'Python async await', 'Python事件循环', 'Python协程',
        'Python并发处理', 'Python异步IO', 'Python异步函数', 'Python异步编程教程',
        // 长尾关键词
        'JavaScript开发者Python异步', '前端转Python异步编程', 'Python async await教程',
        'Python协程编程教程', 'Python异步IO教程', 'Python并发编程教程',
        // 热门搜索词
        'Python异步编程教程', 'Python async教程', 'Python协程教程', 'Python编程教程', 'Python学习'
      ],
      'zh-tw': [
        // 高搜索量核心关键词
        'Python非同步程式設計', 'Python async await', 'Python事件迴圈', 'Python協程',
        'Python並發處理', 'Python非同步IO', 'Python非同步函數', 'Python非同步程式設計教程',
        // 长尾关键词
        'JavaScript開發者Python非同步', '前端轉Python非同步程式設計', 'Python async await教程',
        'Python協程程式設計教程', 'Python非同步IO教程', 'Python並發程式設計教程',
        // 热门搜索词
        'Python非同步程式設計教程', 'Python async教程', 'Python協程教程', 'Python程式設計教程', 'Python學習'
      ],
      'en': [
        // 高搜索量核心关键词
        'Python async programming', 'Python async await', 'Python event loop', 'Python coroutines',
        'Python concurrent processing', 'Python async IO', 'Python async functions', 'Python async programming tutorial',
        // 长尾关键词
        'JavaScript developer Python async', 'frontend to Python async programming', 'Python async await tutorial',
        'Python coroutine programming tutorial', 'Python async IO tutorial', 'Python concurrent programming tutorial',
        // 热门搜索词
        'Python async programming tutorial', 'Python async tutorial', 'Python coroutine tutorial', 'Python programming tutorial', 'Python learning'
      ]
    },
    'module-05-quality-testing-typing': {
      'zh-cn': [
        // 高搜索量核心关键词
        'Python代码质量', 'Python单元测试', 'Python类型注解', 'Python mypy',
        'Python pytest', 'Python调试技巧', 'Python代码规范', 'Python测试驱动开发',
        // 长尾关键词
        'JavaScript开发者Python测试', '前端转Python代码质量', 'Python类型检查教程',
        'Python单元测试教程', 'Python代码规范教程', 'Python调试技巧教程',
        // 热门搜索词
        'Python测试教程', 'Python代码质量教程', 'Python调试教程', 'Python编程教程', 'Python学习'
      ],
      'zh-tw': [
        // 高搜索量核心关键词
        'Python程式碼品質', 'Python單元測試', 'Python型別註解', 'Python mypy',
        'Python pytest', 'Python除錯技巧', 'Python程式碼規範', 'Python測試驅動開發',
        // 长尾关键词
        'JavaScript開發者Python測試', '前端轉Python程式碼品質', 'Python型別檢查教程',
        'Python單元測試教程', 'Python程式碼規範教程', 'Python除錯技巧教程',
        // 热门搜索词
        'Python測試教程', 'Python程式碼品質教程', 'Python除錯教程', 'Python程式設計教程', 'Python學習'
      ],
      'en': [
        // 高搜索量核心关键词
        'Python code quality', 'Python unit testing', 'Python type annotations', 'Python mypy',
        'Python pytest', 'Python debugging techniques', 'Python code standards', 'Python test-driven development',
        // 长尾关键词
        'JavaScript developer Python testing', 'frontend to Python code quality', 'Python type checking tutorial',
        'Python unit testing tutorial', 'Python code standards tutorial', 'Python debugging techniques tutorial',
        // 热门搜索词
        'Python testing tutorial', 'Python code quality tutorial', 'Python debugging tutorial', 'Python programming tutorial', 'Python learning'
      ]
    },
    'module-06-web-development': {
      'zh-cn': [
        // 高搜索量核心关键词
        'Python Web开发', 'Python FastAPI', 'Python后端API', 'Python RESTful',
        'Python数据库', 'Python中间件', 'Python Web框架', 'Python后端开发',
        // 长尾关键词
        'JavaScript开发者Python Web', '前端转Python后端开发', 'Python FastAPI教程',
        'Python RESTful API教程', 'Python数据库操作教程', 'Python Web开发教程',
        // 热门搜索词
        'Python Web开发教程', 'Python后端教程', 'Python API教程', 'Python编程教程', 'Python学习'
      ],
      'zh-tw': [
        // 高搜索量核心关键词
        'Python Web開發', 'Python FastAPI', 'Python後端API', 'Python RESTful',
        'Python資料庫', 'Python中介軟體', 'Python Web框架', 'Python後端開發',
        // 长尾关键词
        'JavaScript開發者Python Web', '前端轉Python後端開發', 'Python FastAPI教程',
        'Python RESTful API教程', 'Python資料庫操作教程', 'Python Web開發教程',
        // 热门搜索词
        'Python Web開發教程', 'Python後端教程', 'Python API教程', 'Python程式設計教程', 'Python學習'
      ],
      'en': [
        // 高搜索量核心关键词
        'Python web development', 'Python FastAPI', 'Python backend API', 'Python RESTful',
        'Python database', 'Python middleware', 'Python web framework', 'Python backend development',
        // 长尾关键词
        'JavaScript developer Python web', 'frontend to Python backend development', 'Python FastAPI tutorial',
        'Python RESTful API tutorial', 'Python database operations tutorial', 'Python web development tutorial',
        // 热门搜索词
        'Python web development tutorial', 'Python backend tutorial', 'Python API tutorial', 'Python programming tutorial', 'Python learning'
      ]
    },
    'module-07-data-automation': {
      'zh-cn': [
        // 高搜索量核心关键词
        'Python数据处理', 'Python pandas', 'Python自动化脚本', 'Python数据分析',
        'Python爬虫', 'Python Excel处理', 'Python数据处理教程', 'Python自动化教程',
        // 长尾关键词
        'JavaScript开发者Python数据处理', '前端转Python数据分析', 'Python pandas教程',
        'Python爬虫教程', 'Python Excel操作教程', 'Python自动化脚本教程',
        // 热门搜索词
        'Python数据处理教程', 'Python数据分析教程', 'Python爬虫教程', 'Python编程教程', 'Python学习'
      ],
      'zh-tw': [
        // 高搜索量核心关键词
        'Python資料處理', 'Python pandas', 'Python自動化腳本', 'Python資料分析',
        'Python爬蟲', 'Python Excel處理', 'Python資料處理教程', 'Python自動化教程',
        // 长尾关键词
        'JavaScript開發者Python資料處理', '前端轉Python資料分析', 'Python pandas教程',
        'Python爬蟲教程', 'Python Excel操作教程', 'Python自動化腳本教程',
        // 热门搜索词
        'Python資料處理教程', 'Python資料分析教程', 'Python爬蟲教程', 'Python程式設計教程', 'Python學習'
      ],
      'en': [
        // 高搜索量核心关键词
        'Python data processing', 'Python pandas', 'Python automation scripts', 'Python data analysis',
        'Python web scraping', 'Python Excel processing', 'Python data processing tutorial', 'Python automation tutorial',
        // 长尾关键词
        'JavaScript developer Python data processing', 'frontend to Python data analysis', 'Python pandas tutorial',
        'Python web scraping tutorial', 'Python Excel operations tutorial', 'Python automation scripts tutorial',
        // 热门搜索词
        'Python data processing tutorial', 'Python data analysis tutorial', 'Python web scraping tutorial', 'Python programming tutorial', 'Python learning'
      ]
    },
    'module-08-projects': {
      'zh-cn': [
        // 高搜索量核心关键词
        'Python实战项目', 'Python综合应用', 'Python项目开发', 'Python最佳实践',
        'Python项目部署', 'Python项目实战', 'Python项目教程', 'Python实战教程',
        // 长尾关键词
        'JavaScript开发者Python项目', '前端转Python实战项目', 'Python Web项目教程',
        'Python数据分析项目', 'Python自动化项目', 'Python项目部署教程',
        // 热门搜索词
        'Python项目教程', 'Python实战教程', 'Python项目实战', 'Python编程教程', 'Python学习'
      ],
      'zh-tw': [
        // 高搜索量核心关键词
        'Python實戰專案', 'Python綜合應用', 'Python專案開發', 'Python最佳實踐',
        'Python專案部署', 'Python專案實戰', 'Python專案教程', 'Python實戰教程',
        // 长尾关键词
        'JavaScript開發者Python專案', '前端轉Python實戰專案', 'Python Web專案教程',
        'Python資料分析專案', 'Python自動化專案', 'Python專案部署教程',
        // 热门搜索词
        'Python專案教程', 'Python實戰教程', 'Python專案實戰', 'Python程式設計教程', 'Python學習'
      ],
      'en': [
        // 高搜索量核心关键词
        'Python real projects', 'Python comprehensive application', 'Python project development', 'Python best practices',
        'Python project deployment', 'Python project practice', 'Python project tutorial', 'Python practice tutorial',
        // 长尾关键词
        'JavaScript developer Python projects', 'frontend to Python real projects', 'Python web project tutorial',
        'Python data analysis projects', 'Python automation projects', 'Python project deployment tutorial',
        // 热门搜索词
        'Python project tutorial', 'Python practice tutorial', 'Python project practice', 'Python programming tutorial', 'Python learning'
      ]
    },
    'module-09-advanced-topics': {
      'zh-cn': [
        // 高搜索量核心关键词
        'Python进阶主题', 'Python AI应用', 'Python机器学习', 'Python深度学习',
        'Python自动化', 'Python学习资源', 'Python高级教程', 'Python进阶教程',
        // 长尾关键词
        'JavaScript开发者Python进阶', '前端转Python AI应用', 'Python机器学习教程',
        'Python深度学习教程', 'Python自动化教程', 'Python高级编程教程',
        // 热门搜索词
        'Python进阶教程', 'Python高级教程', 'Python AI教程', 'Python编程教程', 'Python学习'
      ],
      'zh-tw': [
        // 高搜索量核心关键词
        'Python進階主題', 'Python AI應用', 'Python機器學習', 'Python深度學習',
        'Python自動化', 'Python學習資源', 'Python高級教程', 'Python進階教程',
        // 长尾关键词
        'JavaScript開發者Python進階', '前端轉Python AI應用', 'Python機器學習教程',
        'Python深度學習教程', 'Python自動化教程', 'Python高級程式設計教程',
        // 热门搜索词
        'Python進階教程', 'Python高級教程', 'Python AI教程', 'Python程式設計教程', 'Python學習'
      ],
      'en': [
        // 高搜索量核心关键词
        'Python advanced topics', 'Python AI applications', 'Python machine learning', 'Python deep learning',
        'Python automation', 'Python learning resources', 'Python advanced tutorial', 'Python advanced topics tutorial',
        // 长尾关键词
        'JavaScript developer Python advanced', 'frontend to Python AI applications', 'Python machine learning tutorial',
        'Python deep learning tutorial', 'Python automation tutorial', 'Python advanced programming tutorial',
        // 热门搜索词
        'Python advanced tutorial', 'Python advanced topics tutorial', 'Python AI tutorial', 'Python programming tutorial', 'Python learning'
      ]
    },
    'module-10-common-pitfalls': {
      'zh-cn': [
        // 高搜索量核心关键词
        'Python常见陷阱', 'Python错误处理', 'Python调试技巧', 'Python最佳实践',
        'Python避免错误', 'Python常见问题', 'Python错误解决', 'Python调试教程',
        // 长尾关键词
        'JavaScript开发者Python陷阱', '前端转Python常见问题', 'Python错误处理教程',
        'Python调试技巧教程', 'Python最佳实践教程', 'Python问题解决教程',
        // 热门搜索词
        'Python错误教程', 'Python调试教程', 'Python问题解决', 'Python编程教程', 'Python学习'
      ],
      'zh-tw': [
        // 高搜索量核心关键词
        'Python常見陷阱', 'Python錯誤處理', 'Python除錯技巧', 'Python最佳實踐',
        'Python避免錯誤', 'Python常見問題', 'Python錯誤解決', 'Python除錯教程',
        // 长尾关键词
        'JavaScript開發者Python陷阱', '前端轉Python常見問題', 'Python錯誤處理教程',
        'Python除錯技巧教程', 'Python最佳實踐教程', 'Python問題解決教程',
        // 热门搜索词
        'Python錯誤教程', 'Python除錯教程', 'Python問題解決', 'Python程式設計教程', 'Python學習'
      ],
      'en': [
        // 高搜索量核心关键词
        'Python common pitfalls', 'Python error handling', 'Python debugging techniques', 'Python best practices',
        'Python error prevention', 'Python common issues', 'Python error solving', 'Python debugging tutorial',
        // 长尾关键词
        'JavaScript developer Python pitfalls', 'frontend to Python common issues', 'Python error handling tutorial',
        'Python debugging techniques tutorial', 'Python best practices tutorial', 'Python problem solving tutorial',
        // 热门搜索词
        'Python error tutorial', 'Python debugging tutorial', 'Python problem solving', 'Python programming tutorial', 'Python learning'
      ]
    },
    'module-11-pythonic-code': {
      'zh-cn': [
        // 高搜索量核心关键词
        'Pythonic代码', 'Python代码风格', 'Python PEP8', 'Python代码优化',
        'Python最佳实践', 'Python代码规范', 'Python编程风格', 'Python代码质量',
        // 长尾关键词
        'JavaScript开发者Pythonic代码', '前端转Python代码风格', 'Python PEP8规范教程',
        'Python代码优化教程', 'Python编程风格教程', 'Python代码质量教程',
        // 热门搜索词
        'Python代码风格教程', 'Python PEP8教程', 'Python代码优化教程', 'Python编程教程', 'Python学习'
      ],
      'zh-tw': [
        // 高搜索量核心关键词
        'Pythonic程式碼', 'Python程式碼風格', 'Python PEP8', 'Python程式碼優化',
        'Python最佳實踐', 'Python程式碼規範', 'Python程式設計風格', 'Python程式碼品質',
        // 长尾关键词
        'JavaScript開發者Pythonic程式碼', '前端轉Python程式碼風格', 'Python PEP8規範教程',
        'Python程式碼優化教程', 'Python程式設計風格教程', 'Python程式碼品質教程',
        // 热门搜索词
        'Python程式碼風格教程', 'Python PEP8教程', 'Python程式碼優化教程', 'Python程式設計教程', 'Python學習'
      ],
      'en': [
        // 高搜索量核心关键词
        'Pythonic code', 'Python code style', 'Python PEP8', 'Python code optimization',
        'Python best practices', 'Python code standards', 'Python programming style', 'Python code quality',
        // 长尾关键词
        'JavaScript developer Pythonic code', 'frontend to Python code style', 'Python PEP8 standards tutorial',
        'Python code optimization tutorial', 'Python programming style tutorial', 'Python code quality tutorial',
        // 热门搜索词
        'Python code style tutorial', 'Python PEP8 tutorial', 'Python code optimization tutorial', 'Python programming tutorial', 'Python learning'
      ]
    },
    'module-12-type-annotations': {
      'zh-cn': [
        // 高搜索量核心关键词
        'Python类型注解', 'Python类型提示', 'Python mypy', 'Python静态类型检查',
        'Python类型系统', 'Python类型安全', 'Python类型检查', 'Python类型注解教程',
        // 长尾关键词
        'JavaScript开发者Python类型注解', '前端转Python类型检查', 'Python mypy教程',
        'Python静态类型检查教程', 'Python类型系统教程', 'Python类型安全教程',
        // 热门搜索词
        'Python类型注解教程', 'Python类型检查教程', 'Python mypy教程', 'Python编程教程', 'Python学习'
      ],
      'zh-tw': [
        // 高搜索量核心关键词
        'Python型別註解', 'Python型別提示', 'Python mypy', 'Python靜態型別檢查',
        'Python型別系統', 'Python型別安全', 'Python型別檢查', 'Python型別註解教程',
        // 长尾关键词
        'JavaScript開發者Python型別註解', '前端轉Python型別檢查', 'Python mypy教程',
        'Python靜態型別檢查教程', 'Python型別系統教程', 'Python型別安全教程',
        // 热门搜索词
        'Python型別註解教程', 'Python型別檢查教程', 'Python mypy教程', 'Python程式設計教程', 'Python學習'
      ],
      'en': [
        // 高搜索量核心关键词
        'Python type annotations', 'Python type hints', 'Python mypy', 'Python static type checking',
        'Python type system', 'Python type safety', 'Python type checking', 'Python type annotations tutorial',
        // 长尾关键词
        'JavaScript developer Python type annotations', 'frontend to Python type checking', 'Python mypy tutorial',
        'Python static type checking tutorial', 'Python type system tutorial', 'Python type safety tutorial',
        // 热门搜索词
        'Python type annotations tutorial', 'Python type checking tutorial', 'Python mypy tutorial', 'Python programming tutorial', 'Python learning'
      ]
    }
  },
  'py2js': {
    'module-00-javascript-introduction': {
      'zh-cn': [
        // 高搜索量核心关键词
        'JavaScript介绍', 'JavaScript环境搭建', 'JavaScript安装教程', 'JavaScript开发环境配置',
        'JavaScript入门指南', 'JavaScript学习路线', 'JavaScript基础教程', 'JavaScript开发工具',
        // 长尾关键词
        'Python开发者JavaScript入门', '后端转前端环境配置', 'JavaScript IDE配置教程',
        'JavaScript npm安装教程', 'JavaScript node环境配置', 'JavaScript浏览器开发',
        // 热门搜索词
        'JavaScript培训', 'JavaScript课程', 'JavaScript实战', 'JavaScript项目', 'JavaScript练习'
      ],
      'zh-tw': [
        // 高搜索量核心关键词
        'JavaScript介紹', 'JavaScript環境搭建', 'JavaScript安裝教程', 'JavaScript開發環境配置',
        'JavaScript入門指南', 'JavaScript學習路線', 'JavaScript基礎教程', 'JavaScript開發工具',
        // 长尾关键词
        'Python開發者JavaScript入門', '後端轉前端環境配置', 'JavaScript IDE配置教程',
        'JavaScript npm安裝教程', 'JavaScript node環境配置', 'JavaScript瀏覽器開發',
        // 热门搜索词
        'JavaScript培訓', 'JavaScript課程', 'JavaScript實戰', 'JavaScript專案', 'JavaScript練習'
      ],
      'en': [
        // 高搜索量核心关键词
        'JavaScript introduction', 'JavaScript environment setup', 'JavaScript installation tutorial', 'JavaScript development environment',
        'JavaScript getting started', 'JavaScript learning path', 'JavaScript basics tutorial', 'JavaScript development tools',
        // 长尾关键词
        'Python developer JavaScript introduction', 'backend to frontend environment setup', 'JavaScript IDE configuration',
        'JavaScript npm installation', 'JavaScript node environment setup', 'JavaScript browser development',
        // 热门搜索词
        'JavaScript training', 'JavaScript courses', 'JavaScript projects', 'JavaScript practice', 'JavaScript learning'
      ]
    },
    'module-01-syntax-comparison': {
      'zh-cn': [
        // 高搜索量核心关键词
        'JavaScript语法对比', 'Python转JavaScript语法', 'JavaScript变量声明', 'JavaScript数据类型',
        'JavaScript控制流', 'JavaScript函数定义', 'JavaScript语法映射', 'JavaScript基础语法',
        // 长尾关键词
        'Python开发者JavaScript语法', '后端转前端语法对比', 'JavaScript变量类型声明',
        'JavaScript循环语句教程', 'JavaScript条件语句教程', 'JavaScript函数参数教程',
        // 热门搜索词
        'JavaScript语法教程', 'JavaScript基础教程', 'JavaScript入门教程', 'JavaScript学习', 'JavaScript编程'
      ],
      'zh-tw': [
        // 高搜索量核心关键词
        'JavaScript語法對比', 'Python轉JavaScript語法', 'JavaScript變數宣告', 'JavaScript資料類型',
        'JavaScript控制流', 'JavaScript函數定義', 'JavaScript語法映射', 'JavaScript基礎語法',
        // 长尾关键词
        'Python開發者JavaScript語法', '後端轉前端語法對比', 'JavaScript變數類型宣告',
        'JavaScript迴圈語句教程', 'JavaScript條件語句教程', 'JavaScript函數參數教程',
        // 热门搜索词
        'JavaScript語法教程', 'JavaScript基礎教程', 'JavaScript入門教程', 'JavaScript學習', 'JavaScript程式設計'
      ],
      'en': [
        // 高搜索量核心关键词
        'JavaScript syntax comparison', 'Python to JavaScript syntax', 'JavaScript variable declaration', 'JavaScript data types',
        'JavaScript control flow', 'JavaScript function definition', 'JavaScript syntax mapping', 'JavaScript basic syntax',
        // 长尾关键词
        'Python developer JavaScript syntax', 'backend to frontend syntax comparison', 'JavaScript variable type declaration',
        'JavaScript loop statements tutorial', 'JavaScript conditional statements tutorial', 'JavaScript function parameters tutorial',
        // 热门搜索词
        'JavaScript syntax tutorial', 'JavaScript basics tutorial', 'JavaScript getting started', 'JavaScript learning', 'JavaScript programming'
      ]
    },
    'module-02-dynamic-typing': {
      'zh-cn': [
        // 高搜索量核心关键词
        'JavaScript动态类型', 'JavaScript类型系统', 'JavaScript类型转换', 'JavaScript类型检查',
        'JavaScript类型安全', 'JavaScript类型判断', 'JavaScript动态类型系统', 'JavaScript弱类型',
        // 长尾关键词
        'Python开发者JavaScript类型', '后端转前端类型系统', 'JavaScript类型转换教程',
        'JavaScript类型判断教程', 'JavaScript动态类型教程', 'JavaScript类型安全教程',
        // 热门搜索词
        'JavaScript类型教程', 'JavaScript类型系统教程', 'JavaScript类型检查教程', 'JavaScript编程教程', 'JavaScript学习'
      ],
      'zh-tw': [
        // 高搜索量核心关键词
        'JavaScript動態型別', 'JavaScript型別系統', 'JavaScript型別轉換', 'JavaScript型別檢查',
        'JavaScript型別安全', 'JavaScript型別判斷', 'JavaScript動態型別系統', 'JavaScript弱型別',
        // 长尾关键词
        'Python開發者JavaScript型別', '後端轉前端型別系統', 'JavaScript型別轉換教程',
        'JavaScript型別判斷教程', 'JavaScript動態型別教程', 'JavaScript型別安全教程',
        // 热门搜索词
        'JavaScript型別教程', 'JavaScript型別系統教程', 'JavaScript型別檢查教程', 'JavaScript程式設計教程', 'JavaScript學習'
      ],
      'en': [
        // 高搜索量核心关键词
        'JavaScript dynamic typing', 'JavaScript type system', 'JavaScript type conversion', 'JavaScript type checking',
        'JavaScript type safety', 'JavaScript type detection', 'JavaScript dynamic type system', 'JavaScript weak typing',
        // 长尾关键词
        'Python developer JavaScript types', 'backend to frontend type system', 'JavaScript type conversion tutorial',
        'JavaScript type detection tutorial', 'JavaScript dynamic typing tutorial', 'JavaScript type safety tutorial',
        // 热门搜索词
        'JavaScript type tutorial', 'JavaScript type system tutorial', 'JavaScript type checking tutorial', 'JavaScript programming tutorial', 'JavaScript learning'
      ]
    },
    'module-03-functions-scope': {
      'zh-cn': [
        // 高搜索量核心关键词
        'JavaScript函数', 'JavaScript作用域', 'JavaScript闭包', 'JavaScript this',
        'JavaScript箭头函数', 'JavaScript函数式编程', 'JavaScript高阶函数', 'JavaScript回调函数',
        // 长尾关键词
        'Python开发者JavaScript函数', '后端转前端函数编程', 'JavaScript闭包教程',
        'JavaScript this绑定教程', 'JavaScript箭头函数教程', 'JavaScript函数式编程教程',
        // 热门搜索词
        'JavaScript函数教程', 'JavaScript作用域教程', 'JavaScript闭包教程', 'JavaScript编程教程', 'JavaScript学习'
      ],
      'zh-tw': [
        // 高搜索量核心关键词
        'JavaScript函式', 'JavaScript作用域', 'JavaScript閉包', 'JavaScript this',
        'JavaScript箭頭函式', 'JavaScript函式式程式設計', 'JavaScript高階函式', 'JavaScript回呼函式',
        // 长尾关键词
        'Python開發者JavaScript函式', '後端轉前端函式程式設計', 'JavaScript閉包教程',
        'JavaScript this綁定教程', 'JavaScript箭頭函式教程', 'JavaScript函式式程式設計教程',
        // 热门搜索词
        'JavaScript函式教程', 'JavaScript作用域教程', 'JavaScript閉包教程', 'JavaScript程式設計教程', 'JavaScript學習'
      ],
      'en': [
        // 高搜索量核心关键词
        'JavaScript functions', 'JavaScript scope', 'JavaScript closures', 'JavaScript this',
        'JavaScript arrow functions', 'JavaScript functional programming', 'JavaScript higher-order functions', 'JavaScript callback functions',
        // 长尾关键词
        'Python developer JavaScript functions', 'backend to frontend functional programming', 'JavaScript closures tutorial',
        'JavaScript this binding tutorial', 'JavaScript arrow functions tutorial', 'JavaScript functional programming tutorial',
        // 热门搜索词
        'JavaScript functions tutorial', 'JavaScript scope tutorial', 'JavaScript closures tutorial', 'JavaScript programming tutorial', 'JavaScript learning'
      ]
    },
    'module-04-asynchronous-programming': {
      'zh-cn': [
        // 高搜索量核心关键词
        'JavaScript异步编程', 'JavaScript Promise', 'JavaScript async await', 'JavaScript回调函数',
        'JavaScript事件循环', 'JavaScript异步函数', 'JavaScript并发处理', 'JavaScript异步IO',
        // 长尾关键词
        'Python开发者JavaScript异步', '后端转前端异步编程', 'JavaScript Promise教程',
        'JavaScript async await教程', 'JavaScript事件循环教程', 'JavaScript异步编程教程',
        // 热门搜索词
        'JavaScript异步编程教程', 'JavaScript Promise教程', 'JavaScript async教程', 'JavaScript编程教程', 'JavaScript学习'
      ],
      'zh-tw': [
        // 高搜索量核心关键词
        'JavaScript非同步程式設計', 'JavaScript Promise', 'JavaScript async await', 'JavaScript回呼函式',
        'JavaScript事件迴圈', 'JavaScript非同步函式', 'JavaScript並發處理', 'JavaScript非同步IO',
        // 长尾关键词
        'Python開發者JavaScript非同步', '後端轉前端非同步程式設計', 'JavaScript Promise教程',
        'JavaScript async await教程', 'JavaScript事件迴圈教程', 'JavaScript非同步程式設計教程',
        // 热门搜索词
        'JavaScript非同步程式設計教程', 'JavaScript Promise教程', 'JavaScript async教程', 'JavaScript程式設計教程', 'JavaScript學習'
      ],
      'en': [
        // 高搜索量核心关键词
        'JavaScript async programming', 'JavaScript Promise', 'JavaScript async await', 'JavaScript callback functions',
        'JavaScript event loop', 'JavaScript async functions', 'JavaScript concurrent processing', 'JavaScript async IO',
        // 长尾关键词
        'Python developer JavaScript async', 'backend to frontend async programming', 'JavaScript Promise tutorial',
        'JavaScript async await tutorial', 'JavaScript event loop tutorial', 'JavaScript async programming tutorial',
        // 热门搜索词
        'JavaScript async programming tutorial', 'JavaScript Promise tutorial', 'JavaScript async tutorial', 'JavaScript programming tutorial', 'JavaScript learning'
      ]
    },
    'module-05-frontend-concepts': {
      'zh-cn': [
        // 高搜索量核心关键词
        'JavaScript前端开发', 'JavaScript DOM操作', 'JavaScript浏览器API', 'JavaScript前端概念',
        'JavaScript Web开发', 'JavaScript客户端编程', 'JavaScript前端技术', 'JavaScript浏览器开发',
        // 长尾关键词
        'Python开发者前端开发', '后端转前端概念', 'JavaScript DOM操作教程',
        'JavaScript浏览器API教程', 'JavaScript前端开发教程', 'JavaScript Web开发教程',
        // 热门搜索词
        'JavaScript前端教程', 'JavaScript DOM教程', 'JavaScript浏览器教程', 'JavaScript编程教程', 'JavaScript学习'
      ],
      'zh-tw': [
        // 高搜索量核心关键词
        'JavaScript前端開發', 'JavaScript DOM操作', 'JavaScript瀏覽器API', 'JavaScript前端概念',
        'JavaScript Web開發', 'JavaScript客戶端程式設計', 'JavaScript前端技術', 'JavaScript瀏覽器開發',
        // 长尾关键词
        'Python開發者前端開發', '後端轉前端概念', 'JavaScript DOM操作教程',
        'JavaScript瀏覽器API教程', 'JavaScript前端開發教程', 'JavaScript Web開發教程',
        // 热门搜索词
        'JavaScript前端教程', 'JavaScript DOM教程', 'JavaScript瀏覽器教程', 'JavaScript程式設計教程', 'JavaScript學習'
      ],
      'en': [
        // 高搜索量核心关键词
        'JavaScript frontend development', 'JavaScript DOM manipulation', 'JavaScript browser API', 'JavaScript frontend concepts',
        'JavaScript web development', 'JavaScript client-side programming', 'JavaScript frontend technology', 'JavaScript browser development',
        // 长尾关键词
        'Python developer frontend development', 'backend to frontend concepts', 'JavaScript DOM manipulation tutorial',
        'JavaScript browser API tutorial', 'JavaScript frontend development tutorial', 'JavaScript web development tutorial',
        // 热门搜索词
        'JavaScript frontend tutorial', 'JavaScript DOM tutorial', 'JavaScript browser tutorial', 'JavaScript programming tutorial', 'JavaScript learning'
      ]
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

// 页面类型关键词 - 优化版本
export const pageTypeKeywords = {
  'index': {
    'zh-cn': [
      // 高搜索量核心关键词
      '编程语言学习平台', '编程教程网站', '在线编程学习', '编程教育平台',
      '编程语言对比学习', '编程技能提升平台', '开发者学习平台', '编程培训平台',
      // 长尾关键词
      'JavaScript转其他语言学习', '前端开发者技能提升', '编程语言迁移学习',
      '编程语言转换教程', '多语言编程学习', '编程语言对比教程',
      // 热门搜索词
      '编程学习网站', '编程教程平台', '编程培训网站', '编程教育网站', '编程学习平台'
    ],
    'zh-tw': [
      // 高搜索量核心关键词
      '程式語言學習平台', '程式設計教程網站', '線上程式設計學習', '程式設計教育平台',
      '程式語言對比學習', '程式設計技能提升平台', '開發者學習平台', '程式設計培訓平台',
      // 长尾关键词
      'JavaScript轉其他語言學習', '前端開發者技能提升', '程式語言遷移學習',
      '程式語言轉換教程', '多語言程式設計學習', '程式語言對比教程',
      // 热门搜索词
      '程式設計學習網站', '程式設計教程平台', '程式設計培訓網站', '程式設計教育網站', '程式設計學習平台'
    ],
    'en': [
      // 高搜索量核心关键词
      'programming language learning platform', 'coding tutorial website', 'online programming learning', 'programming education platform',
      'programming language comparison learning', 'coding skills improvement platform', 'developer learning platform', 'programming training platform',
      // 长尾关键词
      'JavaScript to other languages learning', 'frontend developer skills improvement', 'programming language migration learning',
      'programming language conversion tutorial', 'multi-language programming learning', 'programming language comparison tutorial',
      // 热门搜索词
      'programming learning website', 'coding tutorial platform', 'programming training website', 'programming education website', 'programming learning platform'
    ]
  },
  'intro': {
    'zh-cn': [
      // 高搜索量核心关键词
      '编程入门指南', '编程快速开始', '编程环境配置', '编程学习准备',
      '编程新手教程', '编程基础入门', '编程学习路径', '编程入门教程',
      // 长尾关键词
      'JavaScript开发者编程入门', '前端转其他语言入门', '编程语言选择指南',
      '编程学习环境搭建', '编程开发工具配置', '编程学习资源推荐',
      // 热门搜索词
      '编程入门教程', '编程基础教程', '编程学习教程', '编程新手指南', '编程入门学习'
    ],
    'zh-tw': [
      // 高搜索量核心关键词
      '程式設計入門指南', '程式設計快速開始', '程式設計環境配置', '程式設計學習準備',
      '程式設計新手教程', '程式設計基礎入門', '程式設計學習路徑', '程式設計入門教程',
      // 长尾关键词
      'JavaScript開發者程式設計入門', '前端轉其他語言入門', '程式語言選擇指南',
      '程式設計學習環境搭建', '程式設計開發工具配置', '程式設計學習資源推薦',
      // 热门搜索词
      '程式設計入門教程', '程式設計基礎教程', '程式設計學習教程', '程式設計新手指南', '程式設計入門學習'
    ],
    'en': [
      // 高搜索量核心关键词
      'programming getting started guide', 'programming quick start', 'programming environment setup', 'programming learning preparation',
      'programming beginner tutorial', 'programming basics introduction', 'programming learning path', 'programming introduction tutorial',
      // 长尾关键词
      'JavaScript developer programming introduction', 'frontend to other languages introduction', 'programming language selection guide',
      'programming learning environment setup', 'programming development tools configuration', 'programming learning resources recommendation',
      // 热门搜索词
      'programming introduction tutorial', 'programming basics tutorial', 'programming learning tutorial', 'programming beginner guide', 'programming introduction learning'
    ]
  }
};

/**
 * 生成页面的 SEO 关键词 - 优化版本
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
  
  // 去重并限制数量 - 优化版本
  const uniqueKeywords = [...new Set(keywords)];
  
  // 优先返回高搜索量关键词，然后补充长尾关键词
  const highPriorityKeywords = uniqueKeywords.filter(keyword => 
    keyword.includes('教程') || 
    keyword.includes('学习') || 
    keyword.includes('培训') || 
    keyword.includes('课程') ||
    keyword.includes('tutorial') ||
    keyword.includes('learning') ||
    keyword.includes('training') ||
    keyword.includes('course')
  );
  
  const otherKeywords = uniqueKeywords.filter(keyword => 
    !highPriorityKeywords.includes(keyword)
  );
  
  // 返回最多25个关键词，优先高搜索量关键词
  return [...highPriorityKeywords, ...otherKeywords].slice(0, 25);
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