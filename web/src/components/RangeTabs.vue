<template>
  <!-- 时间窗口快捷切换：一排分段按钮，当前项高亮。样式 .timerange 在 app.css（双主题共享）。
       value 用 v-model（同值不重复触发，避免误发请求）；change 便于调用方做"切换即重载"。 -->
  <div class="timerange" :class="{ sm: small }" role="group" :aria-label="ariaLabel">
    <button
      v-for="o in options"
      :key="o.value"
      type="button"
      class="timerange-btn"
      :class="{ on: o.value === modelValue }"
      :aria-pressed="o.value === modelValue"
      :disabled="disabled"
      :title="o.title || ''"
      @click="pick(o.value)"
    >{{ o.label }}</button>
  </div>
</template>

<script setup>
const props = defineProps({
  // 当前选中的窗口值（数字或字符串，由调用方决定语义）
  modelValue: { type: [Number, String], default: null },
  // [{ value, label, title? }]
  options: { type: Array, default: () => [] },
  small: Boolean, // 紧凑形态（卡片内 / 抽屉内）
  disabled: Boolean, // 数据加载中禁用，避免连点堆请求
  ariaLabel: { type: String, default: '时间窗口' },
})
const emit = defineEmits(['update:modelValue', 'change'])

function pick(v) {
  if (props.disabled || v === props.modelValue) return
  emit('update:modelValue', v)
  emit('change', v)
}
</script>
