<template>
  <!-- 全局提示（顶部居中，ChatGPT 式小胶囊）+ 确认框 -->
  <Teleport to="body">
    <div class="toasts" aria-live="polite">
      <TransitionGroup name="toast">
        <div v-for="t in ui.toasts" :key="t.id" class="toast" :class="t.kind">
          <Icon :name="t.kind === 'err' ? 'CircleAlert' : t.kind === 'ok' ? 'CircleCheck' : 'Bell'" :size="16" />
          <span>{{ t.text }}</span>
        </div>
      </TransitionGroup>
    </div>
  </Teleport>
  <Modal :model-value="!!ui.confirm" :title="ui.confirm?.title" width="min(420px, 92vw)" @update:model-value="(v) => !v && ui.confirm?.done(false)">
    <p class="muted" style="margin: 0; white-space: pre-wrap">{{ ui.confirm?.content }}</p>
    <template #footer>
      <button class="btn" @click="ui.confirm?.done(false)">取消</button>
      <button class="btn primary" :class="{ 'is-danger': ui.confirm?.danger }" @click="ui.confirm?.done(true)">{{ ui.confirm?.okText || '确定' }}</button>
    </template>
  </Modal>
</template>

<script setup>
import { ui } from '../utils/toast.js'
import Icon from './Icon.vue'
import Modal from './Modal.vue'
</script>

<style>
.toasts { position: fixed; top: 16px; left: 50%; transform: translateX(-50%); z-index: 2000; display: flex; flex-direction: column; align-items: center; gap: 8px; pointer-events: none; }
.toast {
  display: inline-flex; align-items: center; gap: 8px; max-width: min(520px, 92vw); padding: 10px 16px;
  border-radius: 999px; background: var(--primary); color: var(--primary-fg); font-size: 14px; box-shadow: var(--shadow-lg);
}
.toast.err { background: var(--danger); color: #fff; }
.toast-enter-active, .toast-leave-active { transition: opacity 0.22s var(--ease), transform 0.22s var(--ease); }
.toast-enter-from { opacity: 0; transform: translateY(-10px) scale(0.96); }
.toast-leave-to { opacity: 0; transform: translateY(-6px); }
.toast-move { transition: transform 0.22s var(--ease); }
.btn.primary.is-danger { background: var(--danger); border-color: var(--danger); color: #fff; }
</style>
