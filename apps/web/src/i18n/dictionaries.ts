import type { Locale } from "./config";

const dictionaries = {
  en: {
    brandTagline: "Move what you already know into what comes next.",
    nav: { today: "Today", map: "Migration map", evidence: "Evidence", goals: "Goals", create: "Create", settings: "Settings" },
    coach: { title: "Coach", open: "Ask Coach", close: "Close Coach", hint: "Give me a hint, not the answer", why: "Why does this step matter?", check: "Check my understanding", placeholder: "Ask about this step…" },
    offline: "You are offline. Drafts stay on this device; nothing is marked submitted until the server confirms it.",
    reconnecting: "Connection restored. Checking for new events…",
    common: { start: "Start", continue: "Continue", back: "Back", save: "Save", cancel: "Cancel", retry: "Try again", loading: "Loading", private: "Private", current: "Current", minutes: "min", add: "Add", review: "Review" },
  },
  "zh-CN": {
    brandTagline: "把已经会的，迁移到下一段成长。",
    nav: { today: "今日一步", map: "迁移地图", evidence: "成长记录", goals: "成长目标", create: "创造", settings: "设置" },
    coach: { title: "Coach", open: "问 Coach", close: "关闭 Coach", hint: "给我提示，不要答案", why: "这一步为什么重要？", check: "检查我的理解", placeholder: "问问当前这一步…" },
    offline: "当前离线。草稿只保存在本机；服务器确认前，任何写操作都不会显示为已提交。",
    reconnecting: "网络已恢复，正在补拉最新事件…",
    common: { start: "开始", continue: "继续", back: "返回", save: "保存", cancel: "取消", retry: "重试", loading: "加载中", private: "私密", current: "当前", minutes: "分钟", add: "新增", review: "回顾" },
  },
} as const;

export type Dictionary = (typeof dictionaries)[Locale];

export function dictionary(locale: Locale): Dictionary {
  return dictionaries[locale];
}
