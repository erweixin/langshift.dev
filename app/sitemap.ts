import { MetadataRoute } from 'next'

export default function sitemap(): MetadataRoute.Sitemap {
  const baseUrl = process.env.NEXT_PUBLIC_SITE_URL || 'https://langshift.dev'
  const currentDate = new Date()
  
  // 基础页面
  const basePages = [
    {
      url: baseUrl,
      lastModified: currentDate,
      changeFrequency: 'daily' as const,
      priority: 1,
    },
    {
      url: `${baseUrl}/zh-cn`,
      lastModified: currentDate,
      changeFrequency: 'daily' as const,
      priority: 0.95,
    },
    {
      url: `${baseUrl}/zh-tw`,
      lastModified: currentDate,
      changeFrequency: 'daily' as const,
      priority: 0.95,
    },
    {
      url: `${baseUrl}/en`,
      lastModified: currentDate,
      changeFrequency: 'daily' as const,
      priority: 0.95,
    },
  ]

  // 语言转换课程配置
  const languageCourses = [
    {
      name: 'js2py',
      title: 'JavaScript 到 Python 转换学习',
      priority: 0.9,
      modules: [
        'intro',
        'module-00-python-introduction',
        'module-01-syntax-comparison',
        'module-02-module-system',
        'module-03-oop-functional',
        'module-04-async-programming',
        'module-05-quality-testing-typing',
        'module-06-web-development',
        'module-07-data-automation',
        'module-08-projects',
        'module-09-advanced-topics',
        'module-10-common-pitfalls',
        'module-11-pythonic-code',
        'module-12-type-annotations',
      ],
    },
    {
      name: 'js2rust',
      title: 'JavaScript 到 Rust 转换学习',
      priority: 0.9,
      modules: [
        'intro',
        'module-00-rust-introduction',
        'module-01-syntax-comparison',
        'module-02-module-system',
        'module-03-ownership-memory',
        'module-04-concurrency-async',
        'module-05-type-system-traits',
        'module-06-error-handling',
        'module-07-web-development',
        'module-08-systems-programming',
        'module-09-projects',
        'module-10-common-pitfalls',
        'module-11-idiomatic-rust-style',
      ],
    },
    {
      name: 'js2cpp',
      title: 'JavaScript 到 C++ 转换学习',
      priority: 0.9,
      modules: [
        'module-00-cpp-introduction',
        'module-01-syntax-comparison',
        'module-02-memory-management',
        'module-03-pointers-references',
        'module-04-oop-basics',
        'module-05-templates-generics',
        'module-06-stl-containers',
        'module-07-stl-algorithms',
        'module-08-error-handling',
        'module-09-smart-pointers',
        'module-10-performance-optimization',
        'module-11-modern-cpp-features',
        'module-12-concurrency-multithreading',
        'module-13-systems-programming',
        'module-14-projects',
      ],
    },
    {
      name: 'js2go',
      title: 'JavaScript 到 Go 转换学习',
      priority: 0.9,
      modules: [
        'module-00-go-introduction',
        'module-01-syntax-comparison',
        'module-02-package-system',
        'module-03-types-interfaces',
        'module-04-concurrency-goroutines',
        'module-05-channels-select',
        'module-06-error-handling',
        'module-07-web-development',
        'module-08-microservices',
        'module-09-cloud-native',
        'module-10-testing-debugging',
        'module-11-projects',
        'module-12-common-pitfalls',
        'module-13-idiomatic-go',
      ],
    },
    {
      name: 'js2swift',
      title: 'JavaScript 到 Swift 转换学习',
      priority: 0.9,
      modules: [
        'module-00-swift-introduction',
        'module-01-syntax-comparison',
        'module-02-types-optionals',
        'module-03-functions-closures',
        'module-04-collections',
        'module-05-control-flow',
        'module-06-classes-structures',
        'module-07-protocols-extensions',
        'module-08-enums-pattern-matching',
        'module-09-generics',
        'module-10-memory-management',
        'module-11-error-handling',
        'module-12-concurrency',
        'module-13-swiftui-framework',
        'module-14-ios-development',
        'module-15-projects',
        'module-16-common-pitfalls',
        'module-17-idiomatic-swift',
        'module-18-advanced-topics',
        'module-19-performance-optimization',
      ],
    },
  ]

  // 课程首页
  const coursePages = languageCourses.flatMap(course => [
    // 英文版本
    {
      url: `${baseUrl}/en/docs/${course.name}`,
      lastModified: currentDate,
      changeFrequency: 'weekly' as const,
      priority: course.priority,
    },
    // 简体中文版本
    {
      url: `${baseUrl}/zh-cn/docs/${course.name}`,
      lastModified: currentDate,
      changeFrequency: 'weekly' as const,
      priority: course.priority,
    },
    // 繁体中文版本
    {
      url: `${baseUrl}/zh-tw/docs/${course.name}`,
      lastModified: currentDate,
      changeFrequency: 'weekly' as const,
      priority: course.priority,
    },
  ])

  // 课程模块页面
  const modulePages = languageCourses.flatMap(course =>
    course.modules.flatMap(module => [
      // 英文版本
      {
        url: `${baseUrl}/en/docs/${course.name}/${module}`,
        lastModified: currentDate,
        changeFrequency: 'monthly' as const,
        priority: 0.8,
      },
      // 简体中文版本
      {
        url: `${baseUrl}/zh-cn/docs/${course.name}/${module}`,
        lastModified: currentDate,
        changeFrequency: 'monthly' as const,
        priority: 0.8,
      },
      // 繁体中文版本
      {
        url: `${baseUrl}/zh-tw/docs/${course.name}/${module}`,
        lastModified: currentDate,
        changeFrequency: 'monthly' as const,
        priority: 0.8,
      },
    ])
  )

  return [...basePages, ...coursePages, ...modulePages]
} 