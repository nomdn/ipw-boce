// 对话框单例状态（供 DialogProvider 绑定 + useDialog 的 confirm/open 使用）
import { reactive } from 'vue'

export const dlg = reactive({
  open: false,
  title: '',
  kind: 'default',       // default | danger | info
  message: null,         // 纯文本正文（优先于插槽）
  actions: true,
  showCancel: true,
  cancelText: '取消',
  confirmText: '确定',
  closable: true,
  overlayClose: true,
  // 解析器：confirm 模式用；open 模式关闭时 resolve
  resolver: null,
})

export function openDialog(opts) {
  return new Promise((resolve) => {
    // 已有未决弹窗时先关闭上一次
    if (dlg.open && dlg.resolver) dlg.resolver(false)
    Object.assign(dlg, {
      open: true, title: '', kind: 'default', message: null,
      actions: true, showCancel: true, cancelText: '取消', confirmText: '确定',
      closable: true, overlayClose: true,
    }, opts)
    dlg.resolver = resolve
  })
}

export function closeDialog(result) {
  if (!dlg.open) return
  dlg.open = false
  const r = dlg.resolver
  dlg.resolver = null
  if (r) r(result)
}

// 确认框：返回 Promise<boolean>
export function confirmDialog(opts) {
  return openDialog({
    kind: 'danger',
    confirmText: '确认删除',
    showCancel: true,
    cancelText: '取消',
    ...opts,
  }).then((v) => v === true)
}
