<template>
  <!-- 人机验证：image = 图形验证码（点图片换一张）；turnstile = Cloudflare Turnstile 组件 -->
  <div v-if="mode === 'image'" class="cap-row">
    <input v-model.trim="code" class="inp big" placeholder="图形验证码" maxlength="6" autocomplete="off" @keydown.enter.prevent="$emit('enter')" />
    <button type="button" class="cap-img" title="看不清？换一张" @click="refresh">
      <img v-if="img" :src="img" alt="验证码" />
      <Icon v-else name="RefreshCw" :size="16" class="spin" />
    </button>
  </div>
  <div v-else-if="mode === 'turnstile'" ref="box" class="cap-ts" />
</template>

<script setup>
import { onBeforeUnmount, ref, watch } from 'vue'
import * as api from '../api/boce.js'
import Icon from './Icon.vue'

const props = defineProps({ mode: { type: String, default: 'off' }, siteKey: { type: String, default: '' }, theme: { type: String, default: 'auto' } })
defineEmits(['enter'])

const code = ref('')
const id = ref('')
const img = ref('')
const token = ref('')
const box = ref(null)
let widgetId = null

async function refresh() {
  code.value = ''
  img.value = ''
  try {
    const d = await api.fetchCaptcha()
    id.value = d.id
    img.value = d.image
  } catch {
    img.value = ''
  }
}

let scriptP = null
function loadTurnstile() {
  if (window.turnstile) return Promise.resolve()
  if (!scriptP) {
    scriptP = new Promise((resolve, reject) => {
      const s = document.createElement('script')
      s.src = 'https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit'
      s.async = true
      s.onload = resolve
      s.onerror = () => reject(new Error('人机验证组件加载失败'))
      document.head.appendChild(s)
    })
  }
  return scriptP
}

async function mountTurnstile() {
  await loadTurnstile()
  if (!box.value || !window.turnstile) return
  if (widgetId !== null) window.turnstile.remove(widgetId)
  token.value = ''
  widgetId = window.turnstile.render(box.value, {
    sitekey: props.siteKey,
    theme: props.theme,
    callback: (t) => (token.value = t),
    'expired-callback': () => (token.value = ''),
    'error-callback': () => (token.value = ''),
  })
}

function setup() {
  if (props.mode === 'image') refresh()
  else if (props.mode === 'turnstile' && props.siteKey) setTimeout(mountTurnstile)
}
watch(() => [props.mode, props.siteKey], setup, { immediate: true })
onBeforeUnmount(() => {
  if (widgetId !== null && window.turnstile) window.turnstile.remove(widgetId)
})

// 校验失败后调用：图形验证码一次性，Turnstile token 也只能用一次
function reset() {
  if (props.mode === 'image') refresh()
  else if (props.mode === 'turnstile' && widgetId !== null && window.turnstile) {
    token.value = ''
    window.turnstile.reset(widgetId)
  }
}

// 返回要合进请求体的字段；未完成时返回错误文案
function payload() {
  if (props.mode === 'image') {
    if (!code.value) return { error: '请输入图形验证码' }
    return { captchaId: id.value, captchaCode: code.value }
  }
  if (props.mode === 'turnstile') {
    if (!token.value) return { error: '请先完成人机验证' }
    return { turnstileToken: token.value }
  }
  return {}
}

defineExpose({ payload, reset })
</script>

<style scoped>
.cap-row { display: flex; gap: 8px; }
.cap-row .inp { flex: 1; min-width: 0; }
.cap-img {
  flex-shrink: 0; width: 120px; height: 46px; padding: 0; border: 1px solid var(--border); border-radius: 12px;
  background: var(--bg-soft); display: flex; align-items: center; justify-content: center; cursor: pointer; overflow: hidden;
  transition: opacity 0.15s;
}
.cap-img:hover { opacity: 0.8; }
.cap-img img { width: 100%; height: 100%; display: block; filter: grayscale(1); }
:global([data-theme='dark']) .cap-img img { filter: grayscale(1) invert(0.9); }
.cap-ts { display: flex; justify-content: center; min-height: 65px; }
</style>
