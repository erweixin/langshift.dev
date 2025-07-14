import { LanguageConfig, LanguageConfigMap } from './LearningPathSection.types';

// 验证语言配置的有效性
export const validateLanguageConfig = (config: LanguageConfig): boolean => {
  return !!(
    config.icon &&
    config.gradient &&
    config.name &&
    typeof config.icon === 'string' &&
    typeof config.gradient === 'string' &&
    typeof config.name === 'string'
  );
};

// 验证渐变色的格式
export const validateGradient = (gradient: string): boolean => {
  // 检查是否符合 Tailwind CSS 渐变格式
  const gradientPattern = /^from-[a-z]+-\d+ to-[a-z]+-\d+$/;
  return gradientPattern.test(gradient);
};

// 动态添加新语言配置
export const addLanguageConfig = (
  languageKey: string,
  config: LanguageConfig,
  existingConfigs: LanguageConfigMap
): LanguageConfigMap => {
  if (!validateLanguageConfig(config)) {
    throw new Error(`Invalid language config for ${languageKey}`);
  }

  if (!validateGradient(config.gradient)) {
    throw new Error(`Invalid gradient format for ${languageKey}: ${config.gradient}`);
  }

  return {
    ...existingConfigs,
    [languageKey]: config
  };
};

// 批量添加语言配置
export const addMultipleLanguageConfigs = (
  configs: Record<string, LanguageConfig>,
  existingConfigs: LanguageConfigMap
): LanguageConfigMap => {
  let updatedConfigs = { ...existingConfigs };

  for (const [key, config] of Object.entries(configs)) {
    try {
      updatedConfigs = addLanguageConfig(key, config, updatedConfigs);
    } catch (error) {
      console.error(`Failed to add language config for ${key}:`, error);
    }
  }

  return updatedConfigs;
};

// 获取语言配置的统计信息
export const getLanguageConfigStats = (configs: LanguageConfigMap) => {
  const languages = Object.keys(configs);
  const gradients = new Set(Object.values(configs).map(config => config.gradient));
  
  return {
    totalLanguages: languages.length,
    uniqueGradients: gradients.size,
    languages: languages,
    gradientTypes: Array.from(gradients)
  };
};

// 检查配置冲突
export const checkConfigConflicts = (configs: LanguageConfigMap) => {
  const conflicts: Array<{ type: string; items: string[] }> = [];
  
  // 检查重复的图标
  const icons = Object.entries(configs).map(([key, config]) => ({ key, icon: config.icon }));
  const iconGroups = icons.reduce((acc, item) => {
    acc[item.icon] = acc[item.icon] || [];
    acc[item.icon].push(item.key);
    return acc;
  }, {} as Record<string, string[]>);
  
  Object.entries(iconGroups).forEach(([icon, keys]) => {
    if (keys.length > 1) {
      conflicts.push({ type: 'duplicate_icon', items: keys });
    }
  });

  // 检查重复的渐变色
  const gradients = Object.entries(configs).map(([key, config]) => ({ key, gradient: config.gradient }));
  const gradientGroups = gradients.reduce((acc, item) => {
    acc[item.gradient] = acc[item.gradient] || [];
    acc[item.gradient].push(item.key);
    return acc;
  }, {} as Record<string, string[]>);
  
  Object.entries(gradientGroups).forEach(([gradient, keys]) => {
    if (keys.length > 1) {
      conflicts.push({ type: 'duplicate_gradient', items: keys });
    }
  });

  return conflicts;
};

// 生成唯一的渐变色
export const generateUniqueGradient = (
  baseColor: string,
  existingGradients: string[]
): string => {
  const colorShades = [300, 400, 500, 600, 700, 800];
  const toColors = ['blue', 'green', 'purple', 'pink', 'red', 'orange', 'yellow', 'teal', 'cyan', 'indigo'];
  
  for (const shade of colorShades) {
    for (const toColor of toColors) {
      const gradient = `from-${baseColor}-${shade} to-${toColor}-${shade}`;
      if (!existingGradients.includes(gradient)) {
        return gradient;
      }
    }
  }
  
  // 如果都冲突了，使用默认的
  return 'from-purple-500 to-pink-500';
};

// 导出语言配置的 JSON 格式
export const exportLanguageConfigs = (configs: LanguageConfigMap): string => {
  return JSON.stringify(configs, null, 2);
};

// 从 JSON 导入语言配置
export const importLanguageConfigs = (jsonString: string): LanguageConfigMap => {
  try {
    const configs = JSON.parse(jsonString);
    
    // 验证所有配置
    for (const [key, config] of Object.entries(configs)) {
      if (!validateLanguageConfig(config as LanguageConfig)) {
        throw new Error(`Invalid config for ${key}`);
      }
    }
    
    return configs;
  } catch (error) {
    throw new Error(`Failed to import language configs: ${error}`);
  }
}; 