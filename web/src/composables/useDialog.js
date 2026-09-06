// useDialog：在任意路由组件内调用，获取确认框 / 内容框（返回 Promise）
import { inject } from 'vue'

export function useDialog() {
  const api = inject('boce-dialog', null)
  // 未包 DialogProvider 时回退到原生 confirm（健壮性兜底）
  if (!api) {
    return {
      confirm: (opts) => Promise.resolve(typeof opts === 'string' ? window.confirm(opts) : (opts?.danger ? window.confirm(opts.title || '') : true)),
      open: (opts) => Promise.resolve(false),
    }
  }
  return api
}
