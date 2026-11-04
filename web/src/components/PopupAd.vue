<template>
  <Teleport to="body" :disabled="preview">
    <Transition name="pop" :appear="preview">
      <div v-if="open" class="pop-mask" :class="{ inline: preview }" @click.self="dismiss">
        <div ref="card" class="pop-card" :class="{ noimg: !showImg }" role="dialog" aria-modal="true" :aria-labelledby="titleId" @keydown.tab="trap">
          <button type="button" class="pop-x" :class="{ plain: !showImg }" aria-label="关闭" @click="dismiss">
            <Icon name="X" :size="16" :stroke="2.2" />
          </button>
          <component :is="popup.link ? 'a' : 'div'" v-if="showImg" class="pop-img" v-bind="popup.link ? { href: popup.link, target: '_blank', rel: 'noopener noreferrer' } : {}">
            <img :src="popup.imageUrl" alt="" @error="imgFailed = true" />
          </component>
          <div class="pop-body">
            <h2 v-if="popup.title" :id="titleId">{{ popup.title }}</h2>
            <p v-if="popup.body" class="pop-text">{{ popup.body }}</p>
            <button ref="okBtn" type="button" class="pop-ok" @click="dismiss">我知道了</button>
          </div>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<script setup>
// 弹窗公告：内容由 boce 后台「系统设置 → 弹窗公告」下发；preview=true 时用于后台预览（不记已读、不锁滚动、不遮全屏）
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import Icon from './Icon.vue'
import { markSeen, shouldShow } from '../utils/popup.js'

const props = defineProps({
  popup: { type: Object, default: null },
  delay: { type: Number, default: 600 },
  // 后台预览：行内渲染，始终显示，不记已读、不锁滚动、不监听键盘
  preview: { type: Boolean, default: false },
})

const open = ref(props.preview)
const imgFailed = ref(false)
const card = ref(null)
const okBtn = ref(null)
const titleId = 'pop-title-' + Math.random().toString(36).slice(2, 8)
let timer = null
let prevOverflow = ''

const showImg = computed(() => !!props.popup?.imageUrl && !imgFailed.value)

function lock(on) {
  if (on) {
    prevOverflow = document.body.style.overflow
    document.body.style.overflow = 'hidden'
  } else {
    document.body.style.overflow = prevOverflow
  }
}

function dismiss() {
  if (props.preview || !open.value) return
  markSeen(props.popup)
  open.value = false
  lock(false)
}

function onKey(e) {
  if (e.key === 'Escape') dismiss()
}

// 焦点留在卡片内
function trap(e) {
  if (props.preview) return
  const els = card.value?.querySelectorAll('button, a[href]')
  if (!els?.length) return
  const first = els[0]
  const last = els[els.length - 1]
  if (e.shiftKey && document.activeElement === first) {
    e.preventDefault()
    last.focus()
  } else if (!e.shiftKey && document.activeElement === last) {
    e.preventDefault()
    first.focus()
  }
}

function schedule() {
  if (props.preview) return
  clearTimeout(timer)
  imgFailed.value = false
  if (!props.popup || !shouldShow(props.popup)) return
  timer = setTimeout(async () => {
    open.value = true
    lock(true)
    window.addEventListener('keydown', onKey)
    await nextTick()
    okBtn.value?.focus()
  }, props.delay)
}

watch(() => props.popup?.rev, schedule)
watch(() => props.popup?.imageUrl, () => (imgFailed.value = false))
onMounted(schedule)
onBeforeUnmount(() => {
  clearTimeout(timer)
  window.removeEventListener('keydown', onKey)
  if (open.value && !props.preview) lock(false)
})
</script>

<style scoped>
.pop-mask {
  position: fixed; inset: 0; z-index: 3000; background: rgba(0, 0, 0, 0.5);
  display: flex; align-items: center; justify-content: center; padding: 16px;
}
.pop-mask.inline { position: relative; inset: auto; z-index: auto; background: var(--bg-soft); border-radius: 16px; padding: 28px 16px; min-width: 0; }
.inline .pop-card { width: 100%; max-width: 420px; }
.pop-card {
  position: relative; width: min(420px, 92vw); max-height: 88vh; display: flex; flex-direction: column;
  background: var(--bg-elev); border-radius: 24px; overflow: hidden;
  box-shadow: 0 24px 60px rgba(0, 0, 0, 0.35), 0 2px 8px rgba(0, 0, 0, 0.12);
}
:global([data-theme='dark']) .pop-card { box-shadow: 0 24px 60px rgba(0, 0, 0, 0.6), 0 0 0 1px var(--border-strong); }
.pop-img { display: block; flex-shrink: 0; width: 100%; aspect-ratio: 16 / 9; max-height: 35vh; background: var(--bg-soft); overflow: hidden; }
.pop-img img { width: 100%; height: 100%; object-fit: cover; display: block; }
a.pop-img { cursor: pointer; }
.pop-x {
  position: absolute; top: 12px; right: 12px; z-index: 2; width: 32px; height: 32px; padding: 0; border: 0; border-radius: 50%;
  display: flex; align-items: center; justify-content: center; cursor: pointer; color: #fff;
  background: rgba(0, 0, 0, 0.45); -webkit-backdrop-filter: blur(6px); backdrop-filter: blur(6px); transition: background 0.15s, transform 0.15s;
}
.pop-x:hover { background: rgba(0, 0, 0, 0.65); }
.pop-x:active { transform: scale(0.92); }
.pop-x.plain { color: var(--text-2); background: var(--bg-soft); -webkit-backdrop-filter: none; backdrop-filter: none; }
.pop-x.plain:hover { background: var(--bg-hover); }
.pop-body { padding: 22px 24px 24px; display: flex; flex-direction: column; min-height: 0; }
.noimg .pop-body { padding-top: 28px; }
.pop-body h2 { margin: 0; font-size: 20px; font-weight: 600; letter-spacing: -0.01em; line-height: 1.35; padding-right: 8px; }
.noimg .pop-body h2 { padding-right: 36px; }
.pop-text { margin: 10px 0 0; font-size: 14.5px; line-height: 1.65; color: var(--text-2); white-space: pre-wrap; word-break: break-word; overflow-y: auto; max-height: 36vh; }
.pop-ok {
  margin-top: 20px; height: 46px; border: 0; border-radius: 12px; flex-shrink: 0; cursor: pointer;
  background: var(--primary); color: var(--primary-fg); font: inherit; font-size: 15px; font-weight: 500; transition: opacity 0.15s, transform 0.1s;
}
.pop-ok:hover { opacity: 0.88; }
.pop-ok:active { transform: scale(0.98); }
.pop-ok:focus-visible, .pop-x:focus-visible { outline: 2px solid var(--primary); outline-offset: 2px; }

.pop-enter-active, .pop-leave-active { transition: opacity 0.22s ease; }
.pop-enter-active .pop-card, .pop-leave-active .pop-card { transition: transform 0.26s cubic-bezier(0.2, 0.8, 0.2, 1), opacity 0.22s ease; }
.pop-enter-from, .pop-leave-to { opacity: 0; }
.pop-enter-from .pop-card { transform: translateY(14px) scale(0.96); }
.pop-leave-to .pop-card { transform: translateY(8px) scale(0.97); opacity: 0; }
@media (prefers-reduced-motion: reduce) {
  .pop-enter-active, .pop-leave-active, .pop-enter-active .pop-card, .pop-leave-active .pop-card { transition: none; }
}
@media (max-width: 480px) { .pop-img { max-height: 35vh; } }
</style>
