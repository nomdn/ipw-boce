import { ref, computed } from 'vue'

// 主题：html[data-theme="light" | "dark"]，默认亮色
// 样式侧见 styles/theme.css：:root 是暗色基线，html[data-theme="light"] 覆盖成亮色。
// 首屏由 index.html 的内联脚本提前打上 data-theme，避免暗→亮闪一下；这里只管后续切换。
export const STORAGE_KEY = 'boce-theme'

export const THEMES = ['light', 'dark']

const theme = ref(readInitial())

function readInitial() {
  try {
    const saved = localStorage.getItem(STORAGE_KEY)
    if (THEMES.includes(saved)) return saved
  } catch {
    /* 隐私模式 / 禁用存储：忽略，走默认 */
  }
  return 'light'
}

// applyTheme 切换并持久化；非法值回落到亮色
export function applyTheme(next) {
  const value = THEMES.includes(next) ? next : 'light'
  theme.value = value
  document.documentElement.setAttribute('data-theme', value)
  try {
    localStorage.setItem(STORAGE_KEY, value)
  } catch {
    /* 存不下就只在本次会话生效 */
  }
}

// syncFromDocument 让内存状态与首屏内联脚本的结果对齐（避免两者不一致）
export function syncFromDocument() {
  const current = document.documentElement.getAttribute('data-theme')
  theme.value = THEMES.includes(current) ? current : 'light'
}

// readChartTheme 读当前主题下用于 echarts 的配色（信号色 + 网格/轴/提示框）。
// echarts 画在 canvas 上，吃不到 CSS 变量，只能在主题切换后重新读一次再重算 option。
export function readChartTheme() {
  let cs = null
  try {
    cs = getComputedStyle(document.documentElement)
  } catch {
    cs = null
  }
  const pick = (name, fallback) => (cs ? cs.getPropertyValue(name).trim() || fallback : fallback)
  return {
    cyan: pick('--chart-cyan', '#2a9df4'),
    yellow: pick('--chart-yellow', '#ffd802'),
    green: pick('--chart-green', '#46c47c'),
    red: pick('--chart-red', '#e33b3b'),
    orange: pick('--chart-orange', '#f6540e'),
    gray: pick('--chart-gray', '#7a8a96'),
    // 单序列柱状图主色：暗色沿用信号黄，亮色换蓝（theme.css 定义）
    bar: pick('--chart-bar', '#ffd802'),
    grid: pick('--chart-grid', '#1f2a33'),
    axis: pick('--chart-axis', 'rgba(240, 240, 235, 0.45)'),
    label: pick('--chart-label', 'rgba(248, 248, 245, 0.72)'),
    tipBg: pick('--chart-tooltip-bg', '#101316'),
    tipBorder: pick('--chart-tooltip-border', 'rgba(255, 255, 255, 0.2)'),
    tipText: pick('--chart-tooltip-text', '#f8f8f5'),
    tint: pick('--chart-tint', 'rgba(255, 255, 255, 0.04)'),
    tintStrong: pick('--chart-tint-strong', 'rgba(255, 255, 255, 0.18)'),
  }
}

export function useTheme() {
  return {
    theme,
    isLight: computed(() => theme.value === 'light'),
    set: applyTheme,
    toggle: () => applyTheme(theme.value === 'light' ? 'dark' : 'light'),
  }
}
