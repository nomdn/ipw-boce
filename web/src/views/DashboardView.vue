<template>
  <div class="page wide">
    <div class="page-head">
      <div>
        <h1>{{ auth.isAdmin ? '统计大盘' : '我的大盘' }}</h1>
        <div class="sub">{{ auth.isAdmin ? '节点上报的请求统计（全站）' : '我的定时任务与一键拨测' }}</div>
      </div>
      <div class="seg">
        <button v-for="h in [1, 6, 24, 72, 168]" :key="h" :class="{ on: hours === h }" @click="hours = h">{{ h < 24 ? `${h}h` : `${h / 24}d` }}</button>
      </div>
    </div>

    <template v-if="auth.isAdmin && summary">
      <div class="grid c4">
        <div class="stat"><div class="k">请求总数</div><div class="v">{{ fmtNum(total.total) }}</div></div>
        <div class="stat"><div class="k">错误数</div><div class="v bad">{{ fmtNum(total.errors) }}</div></div>
        <div class="stat"><div class="k">成功率</div><div class="v">{{ total.total ? fmtPct((1 - total.errors / total.total) * 100) : '—' }}</div></div>
        <div class="stat"><div class="k">平均延迟</div><div class="v">{{ total.total ? fmtMs(total.lat / total.total) : '—' }}</div></div>
      </div>
      <div class="card" style="margin-top: 14px">
        <h3>请求趋势</h3>
        <EChart :option="tsOption" :height="260" />
      </div>
      <div class="grid c2">
        <div class="card">
          <h3>按类型</h3>
          <table class="tbl">
            <thead><tr><th>类型</th><th>请求</th><th>错误</th><th>均延迟</th></tr></thead>
            <tbody><tr v-for="r in summary.byApiType" :key="r.apiType"><td>{{ r.apiType }}</td><td>{{ r.total }}</td><td>{{ r.errors }}</td><td>{{ fmtMs(r.total ? r.latencySumMs / r.total : 0) }}</td></tr></tbody>
          </table>
        </div>
        <div class="card">
          <h3>按节点</h3>
          <table class="tbl">
            <thead><tr><th>节点</th><th>请求</th><th>错误</th></tr></thead>
            <tbody><tr v-for="r in summary.byNode" :key="r.nodeId"><td>{{ r.label || r.nodeId }}</td><td>{{ r.total }}</td><td>{{ r.errors }}</td></tr></tbody>
          </table>
        </div>
      </div>
    </template>

    <div class="card" v-if="usage">
      <h3>{{ auth.isAdmin ? '定时 + 一键拨测样本' : '我的拨测用量' }}</h3>
      <div class="grid c4">
        <div class="stat"><div class="k">样本</div><div class="v">{{ usage.total }}</div></div>
        <div class="stat"><div class="k">可用</div><div class="v ok">{{ usage.up }}</div></div>
        <div class="stat"><div class="k">不可用</div><div class="v bad">{{ usage.down }}</div></div>
        <div class="stat"><div class="k">可用率</div><div class="v">{{ usage.up + usage.down ? fmtPct((usage.up / (usage.up + usage.down)) * 100) : '—' }}</div></div>
      </div>
      <EChart v-if="mine.length" :option="mineOption" :height="220" style="margin-top: 12px" />
      <div v-else class="empty">窗口内没有定时任务样本，可以去「SLA 监控」创建任务</div>
    </div>
  </div>
</template>

<script setup>
import { computed, onMounted, ref, watch } from 'vue'
import * as api from '../api/boce.js'
import { useAuthStore } from '../stores/auth.js'
import EChart from '../components/EChart.vue'
import { fmtMs, fmtNum, fmtPct } from '../utils/format.js'

const auth = useAuthStore()
const hours = ref(24)
const summary = ref(null)
const ts = ref([])
const usage = ref(null)
const mine = ref([])

const total = computed(() => {
  const t = { total: 0, errors: 0, lat: 0 }
  for (const r of summary.value?.byApiType || []) {
    t.total += r.total
    t.errors += r.errors
    t.lat += r.latencySumMs
  }
  return t
})
const tsOption = computed(() => ({
  tooltip: { trigger: 'axis' },
  legend: { data: ['请求', '错误'], textStyle: { color: '#888' } },
  grid: { left: 40, right: 16, top: 36, bottom: 28 },
  xAxis: { type: 'time' },
  yAxis: { type: 'value' },
  series: [
    { name: '请求', type: 'line', smooth: true, showSymbol: false, areaStyle: { opacity: 0.12 }, data: ts.value.map((r) => [r.minute * 60000, r.total]) },
    { name: '错误', type: 'line', smooth: true, showSymbol: false, data: ts.value.map((r) => [r.minute * 60000, r.errors]) },
  ],
}))
const mineOption = computed(() => ({
  tooltip: { trigger: 'axis' },
  grid: { left: 44, right: 44, top: 20, bottom: 28 },
  xAxis: { type: 'time' },
  yAxis: [{ type: 'value', min: 0, max: 100, name: '%' }, { type: 'value', name: 'ms' }],
  series: [
    { name: '可用率', type: 'line', smooth: true, showSymbol: false, data: mine.value.map((p) => [p.time, p.availability]) },
    { name: '平均延迟', type: 'line', yAxisIndex: 1, smooth: true, showSymbol: false, data: mine.value.map((p) => [p.time, p.avgMs]) },
  ],
}))

async function load() {
  const h = hours.value
  const jobs = [api.fetchMyUsage(h).then((d) => (usage.value = d)), api.fetchMineSeries(h).then((d) => (mine.value = d.series))]
  if (auth.isAdmin) {
    jobs.push(api.fetchStatsSummary(h).then((d) => (summary.value = d)))
    jobs.push(api.fetchStatsTimeseries(h).then((d) => (ts.value = d)))
  }
  await Promise.allSettled(jobs)
}
watch(hours, load)
onMounted(load)
</script>
