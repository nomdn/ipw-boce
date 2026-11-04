// 明暗主题：浅色 / 深色 / 跟随系统，写到 <html data-theme>
import { ref } from 'vue'

const KEY = 'ipw_boce_theme'
const theme = ref('light') // 实际生效的：light | dark
const mode = ref('system') // 用户选择：light | dark | system

function apply(m) {
  mode.value = m || 'system'
  const sys = window.matchMedia?.('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
  theme.value = mode.value === 'system' ? sys : mode.value
  document.documentElement.dataset.theme = theme.value
  document.querySelector('meta[name="theme-color"]')?.setAttribute('content', theme.value === 'dark' ? '#212121' : '#ffffff')
}

export function initTheme() {
  apply(localStorage.getItem(KEY) || 'system')
  window.matchMedia?.('(prefers-color-scheme: dark)').addEventListener?.('change', () => {
    if (mode.value === 'system') apply('system')
  })
}

export function useTheme() {
  const setMode = (m) => {
    localStorage.setItem(KEY, m)
    apply(m)
  }
  const toggle = () => setMode(theme.value === 'dark' ? 'light' : 'dark')
  return { theme, mode, setMode, toggle }
}
