<template>
  <!-- 居中弹窗：遮罩淡入 + 面板缩放进场；Esc / 点遮罩关闭 -->
  <Teleport to="body">
    <Transition name="modal">
      <div v-if="modelValue" class="modal-scrim" @mousedown.self="close">
        <div class="modal-panel" :style="{ width }" role="dialog" aria-modal="true" :aria-label="title">
          <header v-if="title" class="modal-head">
            <h3>{{ title }}</h3>
            <button class="btn icon" aria-label="关闭" @click="close"><Icon name="X" /></button>
          </header>
          <div class="modal-body"><slot /></div>
          <footer v-if="$slots.footer" class="modal-foot"><slot name="footer" /></footer>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<script setup>
import { onBeforeUnmount, watch } from 'vue'
import Icon from './Icon.vue'

const props = defineProps({
  modelValue: Boolean,
  title: String,
  width: { type: String, default: 'min(520px, 94vw)' },
})
const emit = defineEmits(['update:modelValue'])
const close = () => emit('update:modelValue', false)
const onKey = (e) => e.key === 'Escape' && close()
watch(
  () => props.modelValue,
  (v) => (v ? window.addEventListener('keydown', onKey) : window.removeEventListener('keydown', onKey)),
)
onBeforeUnmount(() => window.removeEventListener('keydown', onKey))
</script>

<style>
.modal-scrim {
  position: fixed; inset: 0; z-index: 1000; background: var(--scrim);
  display: flex; align-items: center; justify-content: center; padding: 16px;
  backdrop-filter: blur(2px);
}
.modal-panel {
  background: var(--bg-elev); color: var(--text); border-radius: 18px; box-shadow: var(--shadow-lg);
  max-height: 90vh; display: flex; flex-direction: column; overflow: hidden; border: 1px solid var(--border);
}
.modal-head { display: flex; align-items: center; justify-content: space-between; padding: 16px 16px 4px 22px; }
.modal-head h3 { margin: 0; font-size: 17px; font-weight: 600; }
.modal-body { padding: 10px 22px 18px; overflow-y: auto; }
.modal-foot { display: flex; justify-content: flex-end; gap: 8px; padding: 12px 22px 18px; border-top: 1px solid var(--border); }

.modal-enter-active, .modal-leave-active { transition: opacity 0.2s var(--ease); }
.modal-enter-active .modal-panel, .modal-leave-active .modal-panel { transition: transform 0.22s var(--ease), opacity 0.22s var(--ease); }
.modal-enter-from, .modal-leave-to { opacity: 0; }
.modal-enter-from .modal-panel, .modal-leave-to .modal-panel { transform: translateY(10px) scale(0.97); opacity: 0; }
</style>
