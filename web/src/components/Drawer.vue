<template>
  <!-- 侧滑面板（通知等）或底部抽屉（移动端商品详情） -->
  <Teleport to="body">
    <Transition :name="side === 'bottom' ? 'sheet' : 'drawer'">
      <div v-if="modelValue" class="drawer-scrim" @mousedown.self="$emit('update:modelValue', false)">
        <aside class="drawer-panel" :class="side" :style="side === 'bottom' ? {} : { width }">
          <header v-if="title" class="modal-head">
            <h3>{{ title }}</h3>
            <div class="row" style="gap: 4px">
              <slot name="actions" />
              <button class="btn icon" aria-label="关闭" @click="$emit('update:modelValue', false)"><Icon name="X" /></button>
            </div>
          </header>
          <div class="drawer-body"><slot /></div>
        </aside>
      </div>
    </Transition>
  </Teleport>
</template>

<script setup>
import Icon from './Icon.vue'
defineProps({ modelValue: Boolean, title: String, side: { type: String, default: 'right' }, width: { type: String, default: 'min(420px, 92vw)' } })
defineEmits(['update:modelValue'])
</script>

<style>
.drawer-scrim { position: fixed; inset: 0; z-index: 1000; background: var(--scrim); }
.drawer-panel { position: absolute; background: var(--bg-elev); color: var(--text); box-shadow: var(--shadow-lg); display: flex; flex-direction: column; }
.drawer-panel.right { top: 0; right: 0; bottom: 0; border-left: 1px solid var(--border); }
.drawer-panel.bottom { left: 0; right: 0; bottom: 0; max-height: 85vh; border-radius: 20px 20px 0 0; max-width: 640px; margin: 0 auto; }
.drawer-body { flex: 1; overflow-y: auto; padding: 8px 20px 24px; }

.drawer-enter-active, .drawer-leave-active, .sheet-enter-active, .sheet-leave-active { transition: opacity 0.22s var(--ease); }
.drawer-enter-active .drawer-panel, .drawer-leave-active .drawer-panel,
.sheet-enter-active .drawer-panel, .sheet-leave-active .drawer-panel { transition: transform 0.28s var(--ease); }
.drawer-enter-from, .drawer-leave-to, .sheet-enter-from, .sheet-leave-to { opacity: 0; }
.drawer-enter-from .drawer-panel, .drawer-leave-to .drawer-panel { transform: translateX(100%); }
.sheet-enter-from .drawer-panel, .sheet-leave-to .drawer-panel { transform: translateY(100%); }
</style>
