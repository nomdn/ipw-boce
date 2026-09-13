// useDialog：在任意路由组件内调用，获取确认框 / 内容框（返回 Promise）
import { inject } from 'vue'

export function useDialog() {
  const api = inject('boce-dialog', null)
  // 未包 DialogProvider 时回退到原生 confirm（健壮性兜底）
  if (!api) {
    return {
      // 危险操作（kind === 'danger'，即删除/清空等不可逆动作）必须弹原生 confirm 让用户确认；
      // 其余非交互确认（仅用于展示信息的 open）无法用原生 confirm 表达，直接放行。
      confirm: (opts) => {
        if (typeof opts === 'string') return Promise.resolve(window.confirm(opts))
        if (opts?.kind === 'danger') {
          return Promise.resolve(window.confirm(opts.title || opts.message || '确认执行该操作？'))
        }
        return Promise.resolve(true)
      },
      open: (opts) => Promise.resolve(false),
    }
  }
  return api
}
