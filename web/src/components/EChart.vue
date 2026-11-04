<template>
  <div ref="el" :style="{ height: height + 'px', width: '100%' }" />
</template>

<script setup>
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import * as echarts from 'echarts/core'
import { LineChart, BarChart } from 'echarts/charts'
import { GridComponent, TooltipComponent, LegendComponent } from 'echarts/components'
import { CanvasRenderer } from 'echarts/renderers'
import { useTheme } from '../composables/useTheme.js'

echarts.use([LineChart, BarChart, GridComponent, TooltipComponent, LegendComponent, CanvasRenderer])

const props = defineProps({ option: { type: Object, required: true }, height: { type: Number, default: 240 } })
const el = ref(null)
const { theme } = useTheme()
let chart
let ro

function render() {
  if (!chart) return
  const dark = theme.value === 'dark'
  const axis = { axisLine: { lineStyle: { color: dark ? '#444' : '#ddd' } }, axisLabel: { color: dark ? '#aaa' : '#777' }, splitLine: { lineStyle: { color: dark ? '#2f2f2f' : '#f0f0f0' } } }
  // 黑白灰阶色板：主线为前景色，其余依次变浅；失败相关系列可在 option 里单独指定红色
  const palette = dark ? ['#ececec', '#8e8e8e', '#5d5d5d', '#b4b4b4', '#3f3f3f'] : ['#0d0d0d', '#8f8f8f', '#c4c4c4', '#5d5d5d', '#e0e0e0']
  const opt = {
    backgroundColor: 'transparent', color: palette, textStyle: { fontFamily: 'inherit' },
    animationDuration: 600, animationEasing: 'cubicOut',
    tooltip: { backgroundColor: dark ? '#2f2f2f' : '#fff', borderColor: dark ? '#383838' : '#e5e5e5', textStyle: { color: dark ? '#ececec' : '#0d0d0d' } },
    ...props.option,
  }
  if (opt.xAxis) opt.xAxis = { ...axis, ...opt.xAxis }
  if (opt.yAxis) opt.yAxis = Array.isArray(opt.yAxis) ? opt.yAxis.map((y) => ({ ...axis, ...y })) : { ...axis, ...opt.yAxis }
  chart.setOption(opt, true)
}

onMounted(() => {
  chart = echarts.init(el.value)
  render()
  ro = new ResizeObserver(() => chart?.resize())
  ro.observe(el.value)
})
onBeforeUnmount(() => {
  ro?.disconnect()
  chart?.dispose()
})
watch(() => props.option, render, { deep: true })
watch(theme, render)
</script>
