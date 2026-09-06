<template>
  <div ref="el" class="chart-box" :style="boxStyle"></div>
</template>

<script setup>
// 通用 ECharts 封装：传 option 即渲染，DOM 尺寸变化自动 resize
import { ref, onMounted, onBeforeUnmount, watch, computed, shallowRef } from 'vue'
import * as echarts from 'echarts/core'
import { LineChart, BarChart, PieChart } from 'echarts/charts'
import {
  GridComponent, TooltipComponent, LegendComponent,
  DataZoomComponent, TitleComponent,
  MarkAreaComponent, MarkLineComponent, MarkPointComponent,
} from 'echarts/components'
import { CanvasRenderer } from 'echarts/renderers'

echarts.use([
  LineChart, BarChart, PieChart,
  GridComponent, TooltipComponent, LegendComponent, DataZoomComponent, TitleComponent,
  MarkAreaComponent, MarkLineComponent, MarkPointComponent,
  CanvasRenderer,
])

const props = defineProps({
  option: { type: Object, required: true },
  height: { type: String, default: '' },
})
const boxStyle = computed(() => (props.height ? { height: props.height } : {}))

const el = ref(null)
const chart = shallowRef(null)
let ro = null

function render() {
  if (!chart.value) return
  chart.value.setOption(props.option, true)
}
function ensure() {
  if (!chart.value && el.value) {
    chart.value = echarts.init(el.value)
    render()
  }
}

watch(() => props.option, render, { deep: true })

onMounted(() => {
  ensure()
  if (el.value && typeof ResizeObserver !== 'undefined') {
    ro = new ResizeObserver(() => chart.value?.resize())
    ro.observe(el.value)
  }
  window.addEventListener('resize', onWinResize)
})
onBeforeUnmount(() => {
  ro?.disconnect()
  window.removeEventListener('resize', onWinResize)
  chart.value?.dispose()
  chart.value = null
})
function onWinResize() {
  chart.value?.resize()
}
defineExpose({ getChart: () => chart.value })
</script>
