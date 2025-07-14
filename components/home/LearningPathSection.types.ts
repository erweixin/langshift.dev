// 语言配置接口
export interface LanguageConfig {
  icon: string;
  gradient: string;
  name: string;
  description?: string;
}

// 语言配置映射类型
export type LanguageConfigMap = Record<string, LanguageConfig>;

// 学习路径阶段接口
export interface LearningPhase {
  phase: string;
  description: string;
  duration: string;
  items: LearningItem[];
}

// 学习项目接口
export interface LearningItem {
  title: string;
  description: string;
  focus: string[];
}

// 语言特定功能接口
export interface LanguageSpecificFeature {
  title: string;
  highlights: string[];
}

// 语言特定功能映射类型
export type LanguageSpecificFeaturesMap = Record<string, LanguageSpecificFeature>;

// 学习路径数据接口
export interface LearningPathData {
  title: string;
  subtitle: string;
  description: string;
  modules: LearningPhase[];
  languageSpecificFeatures: LanguageSpecificFeaturesMap;
  learningTips: string[];
  languageOptimization: {
    title: string;
    subtitle: string;
  };
  learningTipsSection: {
    title: string;
    subtitle: string;
  };
}

// 组件属性接口
export interface LearningPathSectionProps {
  lang: string;
  translations: {
    home: {
      learningPath: LearningPathData;
    };
  };
  isDefaultPage: boolean;
} 