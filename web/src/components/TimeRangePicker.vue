<template>
  <!-- 时间范围选择器：◀ [当前窗口 ▾] ▶
       左箭头 / 右箭头按当前窗口长度整体平移一期（结果变成自定义区间）；
       中间按钮展开预设清单，当前项打勾，末尾是自定义起止时间。
       状态是 { key, start?, end? }，见 utils/timeRange.js。 -->
  <div ref="rootRef" class="trp">
    <button
      class="trp-arrow"
      type="button"
      :disabled="!canPrev"
      title="往前一期"
      aria-label="往前一期"
      @click="shift(-1)"
    >‹</button>

    <div class="trp-main">
      <button
        class="trp-btn"
        type="button"
        :aria-expanded="open"
        aria-haspopup="listbox"
        @click="open = !open"
      >
        <span class="trp-label">{{ label }}</span>
        <span class="trp-caret" aria-hidden="true"></span>
      </button>

      <div v-if="open" class="trp-pop" role="listbox" :aria-label="ariaLabel">
        <button
          v-for="p in presets"
          :key="p.key"
          class="trp-item"
          :class="{ on: isOn(p.key), sep: p.sep }"
          type="button"
          role="option"
          :aria-selected="isOn(p.key)"
          @click="pick(p.key)"
        >
          <span>{{ p.label }}</span>
          <span v-if="isOn(p.key)" class="trp-tick" aria-hidden="true">✓</span>
        </button>

        <button
          class="trp-item sep"
          :class="{ on: isCustom }"
          type="button"
          role="option"
          :aria-selected="isCustom"
          @click="toggleCustom"
        >
          <span>自定义区间</span>
          <span v-if="isCustom" class="trp-tick" aria-hidden="true">✓</span>
        </button>

        <div v-if="customOpen" class="trp-custom">
          <label class="trp-field">
            <span>开始</span>
            <input v-model="cStart" class="ak-input" type="datetime-local" />
          </label>
          <label class="trp-field">
            <span>结束</span>
            <input v-model="cEnd" class="ak-input" type="datetime-local" />
          </label>
          <div class="trp-act">
            <button class="ak-button ak-button--outline sm" type="button" @click="applyCustom">应用</button>
          </div>
          <p v-if="hint" class="trp-hint">{{ hint }}</p>
        </div>
      </div>
    </div>

    <button
      class="trp-arrow"
      type="button"
      :disabled="!canFwd"
      title="往后一期"
      aria-label="往后一期"
      @click="shift(1)"
    >›</button>
  </div>
</template>

<script setup>
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import {
  absLabel,
  canShiftForward,
  resolveRange,
  shiftRange,
  toLocalInput,
  visiblePresets,
} from '../utils/timeRange.js'

const props = defineProps({
  modelValue: { type: Object, default: () => ({ key: '24h' }) },
  // 后端给出的可查上限（天）。用于隐藏查不到数据的预设与禁用越界平移。
  maxDays: { type: Number, default: 0 },
  ariaLabel: { type: String, default: '时间范围' },
})
const emit = defineEmits(['update:modelValue'])

const rootRef = ref(null)
const open = ref(false)
const customOpen = ref(false)
const cStart = ref('')
const cEnd = ref('')

const presets = computed(() => visiblePresets(props.maxDays))
const resolved = computed(() => resolveRange(props.modelValue))
const isCustom = computed(() => props.modelValue?.key === 'custom')

// 相对窗口显示预设名；自定义/平移后的绝对区间直接显示起止时刻
const label = computed(() => {
  const r = resolved.value
  return r.kind === 'abs' ? absLabel(r.from, r.to) : r.label
})

const canFwd = computed(() => canShiftForward(props.modelValue))
// 往前一期会越出可查范围时禁用，避免点进一片空白
const canPrev = computed(() => {
  if (!props.maxDays) return true
  const r = resolved.value
  const span = Math.max(60000, r.to - r.from)
  return Date.now() - (r.from.getTime() - span) <= props.maxDays * 86400000 + 3600000
})

const hint = computed(() =>
  props.maxDays ? `当前部署可查最近 ${props.maxDays} 天，更早的数据已过保留期。` : '',
)

function isOn(key) {
  return props.modelValue?.key === key
}
function pick(key) {
  open.value = false
  customOpen.value = false
  if (!isOn(key)) emit('update:modelValue', { key })
}
function shift(dir) {
  emit('update:modelValue', shiftRange(props.modelValue, dir))
}
function toggleCustom() {
  if (!customOpen.value) {
    // 用当前窗口的起止作为自定义的初始值，改一点点比重选一整段省事
    const r = resolved.value
    cStart.value = toLocalInput(r.from)
    cEnd.value = toLocalInput(r.to)
  }
  customOpen.value = !customOpen.value
}
function applyCustom() {
  const s = new Date(cStart.value)
  const e = new Date(cEnd.value)
  if (!(e > s)) return // 起止不合法就不应用（后端同样会把反向区间夹平）
  emit('update:modelValue', { key: 'custom', start: s.toISOString(), end: e.toISOString() })
  open.value = false
  customOpen.value = false
}

function onDocClick(e) {
  if (rootRef.value && !rootRef.value.contains(e.target)) close()
}
function onKey(e) {
  if (e.key === 'Escape') close()
}
function close() {
  open.value = false
  customOpen.value = false
}

onMounted(() => {
  document.addEventListener('click', onDocClick)
  document.addEventListener('keydown', onKey)
})
onBeforeUnmount(() => {
  document.removeEventListener('click', onDocClick)
  document.removeEventListener('keydown', onKey)
})
</script>

<style scoped>
.trp {
  display: inline-flex;
  align-items: stretch;
  gap: 6px;
  /* 与同排的 .ak-input / .ak-select 同高（ak-ui 控件基准 min-height: 3rem）。
     这两个控件在本仓工具栏里是 48px，选择器矮一截会明显不齐。 */
  height: 48px;
}
.trp-arrow {
  display: inline-flex;
  flex: none;
  align-items: center;
  justify-content: center;
  width: 34px;
  padding: 0 0 2px;
  border: var(--ak-line-hairline) solid var(--ui-line-ctl);
  border-radius: 8px;
  background: transparent;
  color: var(--ak-text-secondary);
  font-family: inherit;
  font-size: 1.05rem;
  line-height: 1;
  cursor: pointer;
}
.trp-arrow:hover:not(:disabled) {
  color: var(--ak-text-primary);
  background: var(--ui-icon-btn-bg);
}
.trp-arrow:disabled {
  opacity: 0.4;
  cursor: default;
}

.trp-main {
  position: relative;
  display: flex; /* 撑满 .trp 的高度，内部按钮才跟着对齐 */
}
.trp-btn {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  min-width: 170px;
  padding: 0 12px;
  border: var(--ak-line-hairline) solid var(--ui-line-ctl);
  border-radius: 8px;
  background: transparent;
  color: var(--ak-text-primary);
  font-family: inherit;
  font-size: 0.8rem;
  cursor: pointer;
}
.trp-btn:hover {
  background: var(--ui-icon-btn-bg);
}
.trp-label {
  flex: 1;
  text-align: left;
  white-space: nowrap;
}
.trp-caret {
  flex: none;
  width: 6px;
  height: 6px;
  margin-top: -3px;
  border-right: 1.5px solid currentColor;
  border-bottom: 1.5px solid currentColor;
  transform: rotate(45deg);
  opacity: 0.7;
}

.trp-pop {
  position: absolute;
  z-index: 60;
  top: calc(100% + 6px);
  left: 0;
  min-width: 272px;
  padding: 5px;
  border: var(--ak-line-hairline) solid var(--ui-line-strong);
  border-radius: 12px;
  /* 浮层底用不透明的 surface-inverse（与站内信面板一致）：--ak-surface-raised 在暗色下
     是 92% 半透明，背后的表单文字会透出来，读起来发花。 */
  background: var(--ak-surface-inverse);
  box-shadow: var(--ui-float-shadow);
}
.trp-item {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  width: 100%;
  padding: 7px 10px;
  border: 0;
  border-radius: 8px;
  background: transparent;
  color: var(--ak-text-primary);
  font-family: inherit;
  font-size: 0.78rem;
  text-align: left;
  white-space: nowrap;
  cursor: pointer;
}
.trp-item:hover {
  background: var(--ui-tint-hover);
}
.trp-item.on {
  font-weight: 600;
}
.trp-item.sep {
  position: relative;
  margin-top: 6px;
}
.trp-item.sep::before {
  content: '';
  position: absolute;
  top: -3px;
  right: 6px;
  left: 6px;
  height: var(--ak-line-hairline);
  background: var(--ui-line);
}
.trp-tick {
  flex: none;
  color: var(--ak-signal-info);
  font-size: 0.8rem;
}

.trp-custom {
  display: grid;
  gap: 8px;
  margin: 4px 4px 3px;
  padding: 10px;
  border-radius: 8px;
  background: var(--ui-tint-ghost);
}
.trp-field {
  display: grid;
  gap: 3px;
  font-size: 0.7rem;
  color: var(--ak-text-secondary);
}
.trp-field .ak-input {
  width: 100%;
}
.trp-act {
  display: flex;
  justify-content: flex-end;
}
.trp-hint {
  margin: 0;
  color: var(--ak-text-secondary);
  font-size: 0.68rem;
  line-height: 1.5;
}
</style>
