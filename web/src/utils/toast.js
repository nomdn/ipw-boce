// 轻量提示与确认框（黑白风格，替代 NutUI 的 toast / dialog）
import { reactive } from 'vue'

export const ui = reactive({ toasts: [], confirm: null })
let seq = 0

function push(kind, text, ms = 2600) {
  const id = ++seq
  ui.toasts.push({ id, kind, text })
  setTimeout(() => {
    const i = ui.toasts.findIndex((t) => t.id === id)
    if (i >= 0) ui.toasts.splice(i, 1)
  }, ms)
}

export const toastOk = (m) => push('ok', m)
export const toastErr = (e) => push('err', e?.message || String(e), 3600)
export const toastText = (m) => push('info', m)

// confirm：Promise<boolean>
export function confirm(content, title = '请确认', { okText = '确定', danger = false } = {}) {
  return new Promise((resolve) => {
    ui.confirm = {
      title,
      content,
      okText,
      danger,
      done: (v) => {
        ui.confirm = null
        resolve(v)
      },
    }
  })
}

// 执行异步操作，失败自动提示
export async function run(fn, okMsg) {
  try {
    const r = await fn()
    if (okMsg) toastOk(okMsg)
    return r
  } catch (e) {
    toastErr(e)
    throw e
  }
}
