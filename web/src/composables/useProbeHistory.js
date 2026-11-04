// 拨测"对话"历史：存在本地，侧边栏展示最近 20 条，点击可回看结果
import { ref } from 'vue'

const KEY = 'ipw_probe_history'
const MAX = 20

function load() {
  try {
    return JSON.parse(localStorage.getItem(KEY) || '[]')
  } catch {
    return []
  }
}

const history = ref(load())

function save() {
  try {
    localStorage.setItem(KEY, JSON.stringify(history.value))
  } catch {
    // 超出配额时只保留最近 5 条
    history.value = history.value.slice(0, 5)
    localStorage.setItem(KEY, JSON.stringify(history.value))
  }
}

export function useProbeHistory() {
  const add = (entry) => {
    history.value = [entry, ...history.value.filter((h) => h.id !== entry.id)].slice(0, MAX)
    save()
  }
  const get = (id) => history.value.find((h) => h.id === id)
  const clear = () => {
    history.value = []
    save()
  }
  return { history, add, get, clear }
}
