<template>
  <span>{{ shown }}</span>
</template>

<script setup>
// 数字滚动：值变化时 600ms ease-out 过渡
import { onMounted, ref, watch } from 'vue'

const props = defineProps({ value: { type: Number, default: 0 } })
const shown = ref(0)
let raf

function animate(from, to) {
  cancelAnimationFrame(raf)
  if (window.matchMedia?.('(prefers-reduced-motion: reduce)').matches) return (shown.value = to)
  const start = performance.now()
  const dur = 600
  const step = (now) => {
    const t = Math.min(1, (now - start) / dur)
    const e = 1 - Math.pow(1 - t, 3)
    shown.value = Math.round(from + (to - from) * e)
    if (t < 1) raf = requestAnimationFrame(step)
  }
  raf = requestAnimationFrame(step)
}

watch(() => props.value, (v, old) => animate(old ?? 0, v ?? 0))
onMounted(() => animate(0, props.value ?? 0))
</script>
