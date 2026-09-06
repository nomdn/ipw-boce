<template>
  <slot />
  <!-- 单例对话框宿主：所有 confirm/open 共用同一实例 -->
  <UiDialog
    :open="dlg.open"
    :title="dlg.title"
    :kind="dlg.kind"
    :message="dlg.message"
    :actions="dlg.actions"
    :show-cancel="dlg.showCancel"
    :cancel-text="dlg.cancelText"
    :confirm-text="dlg.confirmText"
    :closable="dlg.closable"
    :overlay-close="dlg.overlayClose"
    @update:open="onChange"
    @ok="onOk"
    @cancel="onCancel"
  />
</template>

<script setup>
import { provide } from 'vue'
import UiDialog from './UiDialog.vue'
import { dlg, confirmDialog, closeDialog, openDialog } from './dialog.js'

function onOk() { closeDialog(true) }
function onCancel() { closeDialog(false) }
// 点遮罩/×/Esc 走 update:open=false（UiDialog 已发 cancel → onChange 兜底）
function onChange(v) {
  if (!v && dlg.open) closeDialog(false)
}

// 挂到 provide：子组件经 useDialog() 使用
provide('boce-dialog', { confirm: confirmDialog, open: openDialog, state: dlg })
</script>
