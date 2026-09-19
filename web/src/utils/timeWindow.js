// 时间窗口快捷切换的候选清单（供 components/RangeTabs.vue 使用）。
// 放在这里而不是各页面各写一份：后端对窗口的取值上限不同（节点可用率 days ≤ 90 天，
// 且不超出事件保留期），页面上的按钮必须与后端 clamp 范围对齐，否则点了等于没点。

// 按天统计的窗口（节点可用率）：后端 days = 1 ~ 90，且不超出事件保留期
export const DAYS_WINDOWS = [
  { value: 1, label: '24 小时' },
  { value: 7, label: '7 天' },
  { value: 30, label: '30 天' },
  { value: 90, label: '90 天' },
]

// 公开分享状态页：面向外部访客，窗口偏向"长期可用性"，最远看 30 天
export const PUBLIC_STATUS_WINDOWS = [
  { value: 24, label: '24 小时' },
  { value: 72, label: '3 天' },
  { value: 168, label: '7 天' },
  { value: 720, label: '30 天' },
]
