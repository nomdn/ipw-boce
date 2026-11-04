<template>
  <!-- 黑白开关（替代 NutUI 绿色 switch） -->
  <button
    type="button"
    class="sw"
    :class="{ on: modelValue }"
    role="switch"
    :aria-checked="modelValue"
    :disabled="disabled"
    @click="toggle"
  >
    <span class="knob" />
  </button>
</template>

<script setup>
const props = defineProps({ modelValue: Boolean, disabled: Boolean })
const emit = defineEmits(['update:modelValue', 'change'])
function toggle() {
  const v = !props.modelValue
  emit('update:modelValue', v)
  emit('change', v)
}
</script>

<style scoped>
.sw {
  position: relative; width: 36px; height: 20px; border-radius: 999px; border: none; padding: 0; cursor: pointer;
  background: var(--border-strong); transition: background-color 0.2s var(--ease); flex-shrink: 0; vertical-align: middle;
}
.sw.on { background: var(--primary); }
.sw:disabled { opacity: 0.4; cursor: not-allowed; }
.knob {
  position: absolute; top: 2px; left: 2px; width: 16px; height: 16px; border-radius: 50%;
  background: var(--bg); box-shadow: 0 1px 2px rgba(0, 0, 0, 0.25); transition: transform 0.22s var(--ease);
}
.sw.on .knob { transform: translateX(16px); background: var(--primary-fg); }
</style>
