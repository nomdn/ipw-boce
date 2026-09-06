<template>
  <!-- 站内信/确认弹窗：ak-ui 风格的可复用对话框（teleport 到 body，覆盖顶层） -->
  <Teleport to="body">
    <Transition name="ud">
      <div
        v-if="open"
        class="ud-overlay"
        role="presentation"
        @mousedown.self="onOverlay"
      >
        <div
          ref="panel"
          class="ud-panel"
          :class="`ud-kind--${kind}`"
          role="dialog"
          aria-modal="true"
          :aria-label="title || '对话框'"
          tabindex="-1"
        >
          <div class="ud-top">
            <span class="ud-kindbar"></span>
            <h3 class="ud-title">{{ title || '提示' }}</h3>
            <button v-if="closable" class="ud-x" type="button" aria-label="关闭" @click="cancel">×</button>
          </div>

          <!-- 正文：message 纯文本优先；否则走默认插槽（可放富内容/表单） -->
          <div class="ud-body">
            <p v-if="message != null" class="ud-msg">{{ message }}</p>
            <slot v-else />
          </div>

          <!-- 底部动作：默认 = 取消/确认；传入 footer 插槽则完全自定义 -->
          <div v-if="!$slots.footer && actions" class="ud-foot">
            <button
              v-if="showCancel"
              class="ak-button ak-button--outline ud-cancel"
              type="button"
              @click="cancel"
            >{{ cancelText }}</button>
            <button
              class="ak-button ud-ok"
              :class="kind === 'danger' ? 'ud-ok--danger' : 'ud-ok--action'"
              type="button"
              @click="ok"
            >{{ confirmText }}</button>
          </div>
          <div v-else-if="$slots.footer" class="ud-foot" @click.stop>
            <slot name="footer" />
          </div>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<script setup>
import { ref, watch, onBeforeUnmount, nextTick } from 'vue'

const props = defineProps({
  open: { type: Boolean, default: false },
  title: { type: String, default: '' },
  // 默认(default)/危险确认(danger)/信息(info)
  kind: { type: String, default: 'default' },
  // 纯文本正文；设置后默认插槽不再渲染
  message: { type: String, default: null },
  actions: { type: Boolean, default: true },
  showCancel: { type: Boolean, default: true },
  cancelText: { type: String, default: '取消' },
  confirmText: { type: String, default: '确定' },
  // 右上角 × 与 Esc/遮罩点击是否可关闭
  closable: { type: Boolean, default: true },
  // 点遮罩是否当作取消
  overlayClose: { type: Boolean, default: true },
})
const emit = defineEmits(['update:open', 'ok', 'cancel'])

const panel = ref(null)
let lastFocus = null

function ok() { emit('ok') }
function cancel() {
  if (!props.closable) return
  emit('cancel')
  emit('update:open', false)
}
function onOverlay() {
  if (props.overlayClose) cancel()
}

// 打开时聚焦面板、记住触发焦点；关闭时归还焦点
watch(() => props.open, async (v) => {
  if (v) {
    lastFocus = document.activeElement
    await nextTick()
    panel.value?.focus?.()
  } else if (lastFocus && document.body.contains(lastFocus)) {
    lastFocus.focus?.()
  }
})

// Esc 关闭 + 焦点锁（轻量：限制 Tab 不出面板）
function onKey(e) {
  if (!props.open) return
  if (e.key === 'Escape' && props.closable) { e.preventDefault(); cancel() }
  if (e.key === 'Tab' && panel.value) {
    const f = panel.value.querySelectorAll('button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])')
    if (!f.length) return
    const first = f[0]
    const last = f[f.length - 1]
    if (e.shiftKey && document.activeElement === first) { e.preventDefault(); last.focus() }
    else if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first.focus() }
  }
}
document.addEventListener('keydown', onKey)
onBeforeUnmount(() => document.removeEventListener('keydown', onKey))
</script>

<style scoped>
.ud-overlay {
  position: fixed;
  inset: 0;
  z-index: 9999;
  display: flex;
  align-items: center;
  justify-content: center;
  background: rgba(6, 9, 12, 0.62);
  backdrop-filter: blur(2px);
}
.ud-panel {
  width: min(520px, calc(100vw - 40px));
  max-height: min(78vh, 640px);
  display: flex;
  flex-direction: column;
  /* 石墨深面：与暗色控制台层级一致（--ak-surface-panel 是亮面板，这里改用 raised 深面） */
  background: var(--ak-surface-raised);
  color: var(--ak-text-primary);
  border: 1px solid rgba(255, 255, 255, 0.14);
  box-shadow: 0 16px 40px rgba(0, 0, 0, 0.5);
  outline: none;
  /* 工业几何：单处剪角，克制 */
  clip-path: polygon(0 0, calc(100% - var(--ak-cut-md, 14px)) 0, 100% var(--ak-cut-md, 14px), 100% 100%, 0 100%);
}
.ud-top {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 12px 16px 10px;
  border-bottom: 1px solid var(--ak-line-hairline);
  flex: none;
}
.ud-kindbar {
  width: 4px;
  align-self: stretch;
  background: var(--ak-signal-info);
}
.ud-kind--danger .ud-kindbar { background: var(--ak-signal-danger); }
.ud-kind--info .ud-kindbar { background: var(--ak-signal-info); }
.ud-title {
  margin: 0;
  flex: 1;
  font-family: var(--ak-font-command, inherit);
  font-size: 1rem;
  letter-spacing: var(--ak-type-wide);
}
.ud-x {
  border: none;
  background: transparent;
  color: var(--ak-text-secondary);
  font-size: 1.3rem;
  line-height: 1;
  cursor: pointer;
  width: 30px;
  height: 30px;
}
.ud-x:hover { color: var(--ak-text-primary); }
.ud-body {
  padding: 16px;
  overflow: auto;
  flex: 1;
  min-height: 0;
}
.ud-msg {
  margin: 0;
  white-space: pre-wrap;
  word-break: break-word;
  line-height: 1.7;
  color: var(--ak-text-primary);
}
.ud-foot {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  padding: 12px 16px;
  border-top: 1px solid var(--ak-line-hairline);
  flex: none;
}
/* ak-button 在弹窗内收缩到内容宽度（ak-ui 默认固定尺寸仅适合主行动） */
.ud-foot :deep(.ak-button) { width: auto; height: auto; padding: 7px 18px; }
/* 动作配色：危险红 / 主行动（accent 强调） */
.ud-ok--danger { background: var(--ak-signal-danger); border-color: var(--ak-signal-danger); color: #fff; }
.ud-ok--danger:hover { filter: brightness(1.08); }
.ud-ok--action { background: var(--ak-signal-action, var(--ak-signal-accent)); border-color: var(--ak-signal-action, var(--ak-signal-accent)); color: #111; }
.ud-ok--action:hover { filter: brightness(1.08); }
.ud-cancel { color: var(--ak-text-secondary); }

/* 过渡 */
.ud-enter-active, .ud-leave-active { transition: opacity var(--ak-motion-fast, .12s) var(--ak-ease-standard), transform var(--ak-motion-fast, .12s) var(--ak-ease-emphasized); }
.ud-enter-from, .ud-leave-to { opacity: 0; }
.ud-enter-from .ud-panel, .ud-leave-to .ud-panel { transform: translateY(-8px) scale(.99); }
</style>
