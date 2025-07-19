export const defaultStructuredData = {
  "@context": "https://schema.org",
  "@type": "WebSite",
  "name": "LangShift.dev",
  "description": "编程语言转换学习平台",
  "url": "https://langshift.dev",
  "potentialAction": {
    "@type": "SearchAction",
    "target": "https://langshift.dev/search?q={search_term_string}",
    "query-input": "required name=search_term_string"
  }
};

export const organizationStructuredData = {
  "@context": "https://schema.org",
  "@type": "Organization",
  "name": "LangShift.dev",
  "url": "https://langshift.dev",
  "logo": "https://langshift.dev/logo.png",
  "description": "编程语言转换学习平台",
  "sameAs": [
    "https://github.com/langshift-dev",
    "https://twitter.com/langshift_dev"
  ]
};

export const js2cppCourseData = {
  name: "JavaScript to C++ Tutorial",
  description: "Master C++ from a JavaScript background, focusing on performance, memory management, and systems programming.",
  url: "https://langshift.dev/docs/js2cpp",
  provider: "LangShift.dev",
  courseMode: "online",
  educationalLevel: "Advanced"
};

export const js2goCourseData = {
  name: "JavaScript to Go Tutorial",
  description: "Learn Go from a JavaScript developer perspective, focusing on concurrency, systems programming, and cloud-native development.",
  url: "https://langshift.dev/docs/js2go",
  provider: "LangShift.dev",
  courseMode: "online",
  educationalLevel: "Intermediate"
};

export const js2swiftCourseData = {
  name: "JavaScript to Swift Tutorial",
  description: "Learn Swift from a JavaScript developer perspective, focusing on type safety, iOS development, and protocol-oriented programming.",
  url: "https://langshift.dev/docs/js2swift",
  provider: "LangShift.dev",
  courseMode: "online",
  educationalLevel: "Intermediate"
};

export const js2cCourseData = {
  name: "JavaScript to C Tutorial",
  description: "Learn C programming from a JavaScript developer perspective, mastering memory management, pointer operations, and systems programming.",
  url: "https://langshift.dev/docs/js2c",
  provider: "LangShift.dev",
  courseMode: "online",
  educationalLevel: "Advanced"
};

export const js2kotlinCourseData = {
  name: "JavaScript to Kotlin Tutorial",
  description: "Learn Kotlin programming from a JavaScript developer perspective, mastering coroutines, Android development, and JVM ecosystem.",
  url: "https://langshift.dev/docs/js2kotlin",
  provider: "LangShift.dev",
  courseMode: "online",
  educationalLevel: "Advanced"
}
export const js2javaCourseData = {
  name: "JavaScript to Java Tutorial",
  description: "Learn Java from a JavaScript developer perspective, mastering object-oriented programming, enterprise development, and Spring framework.",
  url: "https://langshift.dev/docs/js2java",
  provider: "LangShift.dev",
  courseMode: "online",
  educationalLevel: "Intermediate"
};

export const courseStructuredData = (courseData: {
  name: string
  description: string
  url: string
  provider: string
  courseMode: string
  educationalLevel: string
}) => ({
  "@context": "https://schema.org",
  "@type": "Course",
  "name": courseData.name,
  "description": courseData.description,
  "provider": {
    "@type": "Organization",
    "name": courseData.provider,
    "sameAs": "https://langshift.dev"
  },
  "courseMode": courseData.courseMode,
  "educationalLevel": courseData.educationalLevel,
  "url": courseData.url,
  // 添加 hasCourseInstance 字段
  "hasCourseInstance": {
    "@type": "CourseInstance",
    "courseMode": courseData.courseMode,
    "inLanguage": "zh-CN",
    "availableLanguage": ["zh-CN", "zh-TW", "en"],
    "startDate": new Date().toISOString(),
    "endDate": new Date(Date.now() + 365 * 24 * 60 * 60 * 1000).toISOString(), // 一年后
    "location": {
      "@type": "VirtualLocation",
      "url": courseData.url
    }
  },
  // 添加 offers 字段
  "offers": {
    "@type": "Offer",
    "price": "0",
    "priceCurrency": "CNY",
    "availability": "https://schema.org/InStock",
    "validFrom": new Date().toISOString(),
    "validThrough": new Date(Date.now() + 365 * 24 * 60 * 60 * 1000).toISOString(), // 一年后
    "url": courseData.url,
    "seller": {
      "@type": "Organization",
      "name": courseData.provider,
      "sameAs": "https://langshift.dev"
    }
  }
}); 