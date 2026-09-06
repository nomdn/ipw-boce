<template>
  <div>
    <!-- ============ admin：全站流量大盘（request_stats） ============ -->
    <template v-if="isAdmin">
      <div class="toolbar">
        <div class="form-field">
          <label>统计窗口</label>
          <select class="ak-select" v-model="hours" @change="loadAdmin">
            <option :value="1">近 1 小时</option>
            <option :value="6">近 6 小时</option>
            <option :value="24">近 24 小时</option>
            <option :value="72">近 72 小时</option>
          </select>
        </div>
        <button class="ak-button ak-button--outline" @click="loadAdmin">刷新</button>
        <span v-if="error" class="err">加载失败：{{ error }}</span>
      </div>
      <div v-if="loading" class="loading-center"><span class="ak-loading"></span></div>

      <div class="kpi-strip" style="margin-bottom: 16px">
        <div class="kpi-card"><div class="kpi-label">请求总数</div><div class="kpi-value">{{ num(total) }}</div><div class="kpi-sub">{{ hours }}h 窗口</div></div>
        <div class="kpi-card kpi--danger"><div class="kpi-label">错误</div><div class="kpi-value">{{ num(errors) }}</div><div class="kpi-sub">错误率 {{ pct(errors, total) }}</div></div>
        <div class="kpi-card kpi--success"><div class="kpi-label">平均延迟</div><div class="kpi-value" style="font-size:1.5rem">{{ avgMs(latSum, total) }}</div><div class="kpi-sub">端到端</div></div>
        <div class="kpi-card kpi--accent"><div class="kpi-label">接口类型</div><div class="kpi-value">{{ num(apiCount) }}</div><div class="kpi-sub">whois/dns/ssl…</div></div>
        <div class="kpi-card kpi--action"><div class="kpi-label">上报节点</div><div class="kpi-value">{{ num(nodeCount) }}</div><div class="kpi-sub">有统计</div></div>
      </div>

      <div class="grid-2" style="margin-bottom:16px">
        <section class="panel">
          <h2 class="panel-title">请求趋势 <span class="hl">/ timeseries</span></h2>
          <EChart :option="trendOption" />
        </section>
        <section class="panel">
          <h2 class="panel-title">按接口类型 <span class="hl">/ byApiType</span></h2>
          <EChart :option="typeOption" />
        </section>
      </div>

      <div class="grid-2">
        <section class="panel">
          <h2 class="panel-title">按节点请求量 <span class="hl">/ byNode</span></h2>
          <EChart :option="nodeOption" />
        </section>
        <section class="panel">
          <h2 class="panel-title">接口汇总明细</h2>
          <div class="ak-table-wrap">
            <table class="ak-table">
              <thead><tr><th>apiType</th><th>总量</th><th>错误</th><th>错误率</th><th>平均延迟</th></tr></thead>
              <tbody>
                <tr v-for="r in byApiType" :key="r.apiType">
                  <td class="mono nowrap"><span class="ak-tag ch">{{ r.apiType }}</span></td>
                  <td class="mono">{{ num(r.total) }}</td>
                  <td class="mono" :class="r.errors ? 'err' : 'ok-200'">{{ num(r.errors) }}</td>
                  <td class="mono">{{ pct(r.errors, r.total) }}</td>
                  <td class="mono">{{ avgMs(r.latencySumMs, r.total) }}</td>
                </tr>
                <tr v-if="!byApiType.length"><td colspan="5" class="dim">当前窗口暂无上报数据</td></tr>
              </tbody>
            </table>
          </div>
        </section>
      </div>
    </template>

    <!-- ============ user：我的统计任务报告（owner=自己的 SLA 任务） ============ -->
    <template v-else>
      <div class="toolbar">
        <div class="form-field">
          <label>统计窗口</label>
          <select class="ak-select" v-model="hours" @change="loadMine">
            <option :value="1">近 1 小时</option>
            <option :value="6">近 6 小时</option>
            <option :value="24">近 24 小时</option>
            <option :value="72">近 72 小时</option>
            <option :value="168">近 7 天</option>
          </select>
        </div>
        <button class="ak-button ak-button--outline" @click="loadMine">刷新</button>
        <span v-if="error" class="err">加载失败：{{ error }}</span>
      </div>
      <div v-if="loading" class="loading-center"><span class="ak-loading"></span></div>

      <div class="kpi-strip" style="margin-bottom:16px">
        <div class="kpi-card kpi--accent"><div class="kpi-label">我的任务</div><div class="kpi-value">{{ myTasks.length }}</div><div class="kpi-sub">运行 {{ runningCnt }} / 停 {{ myTasks.length - runningCnt }}</div></div>
        <div class="kpi-card"><div class="kpi-label">平均可用率</div><div class="kpi-value" :style="availTone">{{ avgAvail }}%</div><div class="kpi-sub">{{ hours }}h 加权</div></div>
        <div class="kpi-card kpi--danger"><div class="kpi-label">累计掉线</div><div class="kpi-value">{{ sumDown }}</div><div class="kpi-sub">最近窗口</div></div>
        <div class="kpi-card kpi--success"><div class="kpi-label">样本总数</div><div class="kpi-value">{{ numMy(sumSamples) }}</div><div class="kpi-sub">source=sched</div></div>
        <div class="kpi-card"><div class="kpi-label">平均延迟</div><div class="kpi-value">{{ avgMsAll ? avgMsAll + ' ms' : '—' }}</div><div class="kpi-sub">按样本加权</div></div>
      </div>

      <!-- 我的任务趋势曲线 -->
      <section class="panel" style="margin-bottom:16px">
        <h2 class="panel-title">我的可用率趋势 <span class="hl">/ mine series</span></h2>
        <EChart :option="mineTrendOption" height="300px" />
        <div v-if="!loading && !mySeries.length && !error" class="dim" style="padding:4px 2px">
          暂无定时样本 —— 任务首次拨测落库后这里会出现可用率曲线。
        </div>
      </section>

      <div class="panel">
        <h2 class="panel-title">我的统计任务 <span class="hl">/ my tasks report</span>
          <router-link class="ak-button ak-button--outline sm" style="float:right" :to="{ name: 'sla' }">进入 SLA 监控</router-link>
        </h2>
        <div v-if="!loading && !myTasks.length && !error" class="dim" style="padding:8px 2px">
          你还没有定时拨测任务 —— 到「SLA 监控」新建一个，调度器会按时对节点拨测并累计这份报告。
        </div>
        <div v-else class="ak-table-wrap">
          <table class="ak-table">
            <thead><tr><th>任务</th><th>类型</th><th>目标</th><th>状态</th><th>可用率</th><th>平均延迟</th><th>样本</th><th>掉线</th><th>最近更新</th></tr></thead>
            <tbody>
              <tr v-for="c in myCards" :key="c.task.id" @click="$router.push({ name: 'sla' })" style="cursor:pointer">
                <td class="mono" :title="c.task.name">{{ c.task.name }}</td>
                <td class="mono nowrap"><span class="ak-tag ch">{{ c.task.apiType }}</span></td>
                <td class="mono dim" style="max-width:220px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">{{ c.task.target }}</td>
                <td><span class="dot" :class="c.task.enabled ? 'online' : 'offline'"></span>{{ c.task.enabled ? '运行' : '停' }}</td>
                <td class="mono" :style="c.agg ? availStyle(c.agg.availability) : ''">{{ c.agg ? fmtPct(c.agg.availability) : '—' }}</td>
                <td class="mono">{{ c.agg ? c.agg.avgMs + ' ms' : '—' }}</td>
                <td class="mono">{{ c.agg ? c.agg.samples : '—' }}</td>
                <td class="mono" :class="c.agg && c.agg.errCnt ? 'err' : ''">{{ c.agg ? c.agg.errCnt : '—' }}</td>
                <td class="dim" style="white-space:nowrap">{{ fmtAgo(c.sla?.window?.to) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </template>
  </div>
</template>

<script setup>
import { ref, computed, onMounted } from 'vue'
import { useAuthStore } from '../stores/auth.js'
import EChart from '../components/EChart.vue'
import { fetchStatsSummary, fetchStatsTimeseries, fetchTasks, fetchTaskSla, fetchMineSeries } from '../api/boce.js'
import { num, avgMs, pct, fmtMinute } from '../utils/format.js'

const auth = useAuthStore()
const isAdmin = computed(() => auth.isAdmin)
const hours = ref(24)
const loading = ref(false)
const error = ref('')

// 窗口切换时按角色走对应加载
onMounted(load)
function load() {
  isAdmin.value ? loadAdmin() : loadMine()
}

// ================= admin：request_stats 大盘 =================
const summary = ref(null)
const series = ref([])
async function loadAdmin() {
  loading.value = true
  error.value = ''
  try {
    const [s, ts] = await Promise.all([
      fetchStatsSummary(hours.value),
      fetchStatsTimeseries(hours.value),
    ])
    summary.value = s
    series.value = ts || []
  } catch (e) {
    error.value = e?.message || 'load failed'
  } finally {
    loading.value = false
  }
}

const byApiType = computed(() => (summary.value?.byApiType || []).slice().sort((a, b) => b.total - a.total))
const byNode = computed(() => (summary.value?.byNode || []).slice().sort((a, b) => b.total - a.total))
const total = computed(() => byApiType.value.reduce((s, x) => s + x.total, 0))
const errors = computed(() => byApiType.value.reduce((s, x) => s + x.errors, 0))
const latSum = computed(() => byApiType.value.reduce((s, x) => s + (x.latencySumMs || 0), 0))
const apiCount = computed(() => byApiType.value.length)
const nodeCount = computed(() => byNode.value.length)

// ---- 配色（与 app.css --chart-* 语义对齐）----
const C = {
  cyan: readVar('--chart-cyan') || '#2a9df4',
  yellow: readVar('--chart-yellow') || '#ffd802',
  green: readVar('--chart-green') || '#46c47c',
  red: readVar('--chart-red') || '#e33b3b',
  grid: readVar('--chart-grid') || '#1f2a33',
  axis: readVar('--chart-axis') || 'rgba(240,240,235,.45)',
  label: readVar('--chart-label') || 'rgba(248,248,245,.72)',
}
function readVar(name) {
  try { return getComputedStyle(document.documentElement).getPropertyValue(name).trim() || null } catch { return null }
}
const axis = {
  axisLine: { lineStyle: { color: C.axis } },
  axisLabel: { color: C.label },
  splitLine: { lineStyle: { color: C.grid } },
}
const tooltipBase = {
  backgroundColor: '#101316', borderColor: 'rgba(255,255,255,.2)', textStyle: { color: '#f8f8f5' },
}
const trendOption = computed(() => ({
  color: [C.cyan, C.red],
  tooltip: { trigger: 'axis', ...tooltipBase },
  legend: { textStyle: { color: C.label }, top: 0, data: ['请求', '错误'] },
  grid: { left: 40, right: 16, top: 32, bottom: 26 },
  xAxis: { type: 'category', boundaryGap: false, data: series.value.map((s) => fmtMinute(s.minute)), ...axis },
  yAxis: { type: 'value', minInterval: 1, ...axis },
  series: [
    { name: '请求', type: 'line', smooth: true, showSymbol: false, areaStyle: { opacity: 0.12 }, data: series.value.map((s) => s.total) },
    { name: '错误', type: 'line', smooth: true, showSymbol: false, data: series.value.map((s) => s.errors) },
  ],
}))
const typeOption = computed(() => ({
  color: [C.cyan], tooltip: { trigger: 'axis', ...tooltipBase },
  grid: { left: 40, right: 16, top: 12, bottom: 34 },
  xAxis: { type: 'category', data: byApiType.value.map((x) => x.apiType), ...axis, axisLabel: { ...axis.axisLabel, rotate: 0 } },
  yAxis: { type: 'value', minInterval: 1, ...axis },
  series: [{ type: 'bar', barMaxWidth: 34, data: byApiType.value.map((x) => x.total), itemStyle: { color: C.cyan } }],
}))
const nodeOption = computed(() => ({
  color: [C.yellow], tooltip: { trigger: 'axis', ...tooltipBase },
  grid: { left: 40, right: 16, top: 12, bottom: 26 },
  xAxis: { type: 'category', data: byNode.value.map((x) => shortNode(x.nodeId)), ...axis, axisLabel: { ...axis.axisLabel, interval: 0 } },
  yAxis: { type: 'value', minInterval: 1, ...axis },
  series: [{ type: 'bar', barMaxWidth: 40, data: byNode.value.map((x) => x.total), itemStyle: { color: C.yellow } }],
}))
function shortNode(id) { return (id || '').length > 12 ? id.slice(0, 12) + '…' : id }

// ================= user：我的任务报告 =================
const myTasks = ref([])
const mySla = ref({})
const mySeries = ref([])
async function loadMine() {
  loading.value = true
  error.value = ''
  try {
    const [list, ser] = await Promise.all([
      fetchTasks(),
      fetchMineSeries(hours.value).catch(() => []),
    ])
    myTasks.value = list || []
    mySeries.value = Array.isArray(ser) ? ser : (ser?.series || [])
    const res = await Promise.all(
      myTasks.value.map(async (t) => {
        const sla = await fetchTaskSla(t.id, hours.value).catch(() => null)
        return [t.id, sla]
      }),
    )
    mySla.value = Object.fromEntries(res)
  } catch (e) {
    error.value = e?.message || 'load failed'
  } finally {
    loading.value = false
  }
}

// 汇总一个任务全部节点 → 全局指标（口径同 SlaView.aggregate）
function aggregate(task, sla) {
  if (!sla || !Array.isArray(sla.byNode) || !sla.byNode.length) return null
  const by = sla.byNode
  let samples = 0, up = 0, down = 0, slow = 0, sumMs = 0, nLat = 0
  by.forEach((n) => {
    samples += n.samples; up += n.up; down += n.down; slow += n.slow
    if (n.avgMs && n.samples) { sumMs += n.avgMs * n.samples; nLat += n.samples }
  })
  const denom = up + down || 0
  return {
    samples,
    availability: denom ? Math.round((up / denom) * 10000) / 100 : 0,
    errCnt: down,
    avgMs: nLat ? Math.round(sumMs / nLat) : 0,
  }
}
const myCards = computed(() =>
  myTasks.value.map((task) => ({ task, sla: mySla.value[task.id] || null, agg: aggregate(task, mySla.value[task.id]) })),
)
const runningCnt = computed(() => myTasks.value.filter((t) => t.enabled).length)
const sumSamples = computed(() => myCards.value.reduce((s, c) => s + (c.agg?.samples || 0), 0))
const sumDown = computed(() => myCards.value.reduce((s, c) => s + (c.agg?.errCnt || 0), 0))
const avgAvail = computed(() => {
  const den = myCards.value.reduce((s, c) => s + (c.agg?.samples || 0), 0)
  if (!den) return 0
  const w = myCards.value.reduce((s, c) => s + (c.agg?.availability || 0) * (c.agg?.samples || 0), 0)
  return Math.round((w / den) * 100) / 100
})
const avgMsAll = computed(() => {
  const den = myCards.value.reduce((s, c) => s + (c.agg?.samples || 0), 0)
  if (!den) return 0
  const w = myCards.value.reduce((s, c) => s + (c.agg?.avgMs || 0) * (c.agg?.samples || 0), 0)
  return Math.round(w / den)
})
const numMy = (n) => (n == null ? '—' : Number(n).toLocaleString())
const fmtPct = (n) => (n == null ? '—' : `${n}%`)
const availStyle = (a) => ({ color: a >= 99.9 ? 'var(--ak-signal-success)' : (a >= 95 ? 'var(--ak-signal-action)' : 'var(--ak-signal-danger)') })
const availTone = computed(() => ({ color: avgAvail.value >= 99.9 ? 'var(--ak-signal-success)' : (avgAvail.value >= 95 ? 'var(--ak-signal-action)' : 'var(--ak-signal-danger)') }))

// ---- 我的任务可用率趋势曲线 ----
const mineTrendOption = computed(() => {
  const rows = mySeries.value
  const labels = rows.map((s) => fmtBucket(s.time, s.minute))
  const avail = rows.map((s) => (typeof s.availability === 'number' ? s.availability : null))
  const avg = rows.map((s) => (typeof s.avgMs === 'number' ? s.avgMs : null))
  return {
    color: [C.green, C.cyan],
    tooltip: {
      trigger: 'axis',
      ...tooltipBase,
      valueFormatter: (v, p) => (p?.seriesName === '可用率' ? `${v}%` : `${v} ms`),
    },
    legend: { textStyle: { color: C.label }, top: 0, data: ['可用率', '平均延迟'] },
    grid: { left: 44, right: 44, top: 30, bottom: 26 },
    xAxis: { type: 'category', boundaryGap: false, data: labels, ...axis },
    yAxis: [
      { type: 'value', min: 0, max: 100, axisLabel: { ...axis.axisLabel, formatter: '{value}%' }, ...axis },
      { type: 'value', name: 'ms', ...axis, axisLabel: { ...axis.axisLabel, formatter: '{value}' }, splitLine: { show: false } },
    ],
    series: [
      { name: '可用率', type: 'line', smooth: true, showSymbol: false, connectNulls: true, areaStyle: { opacity: 0.12 }, data: avail },
      { name: '平均延迟', type: 'line', smooth: true, showSymbol: false, connectNulls: true, yAxisIndex: 1, data: avg },
    ],
  }
})
// RFC3339 桶时间 → 短标签（HH:mm 或 MM-DD HH:mm）
function fmtBucket(iso, minute) {
  if (iso) {
    const d = new Date(iso)
    if (!Number.isNaN(d.getTime())) {
      const now = new Date()
      const sameDay = d.toDateString() === now.toDateString()
      const p = (n) => String(n).padStart(2, '0')
      const hm = `${p(d.getHours())}:${p(d.getMinutes())}`
      return sameDay ? hm : `${p(d.getMonth() + 1)}-${p(d.getDate())} ${hm}`
    }
  }
  if (minute != null) return fmtMinute(minute)
  return ''
}
function fmtAgo(iso) {
  if (!iso) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  const s = Math.floor((Date.now() - d.getTime()) / 1000)
  if (s < 60) return '刚刚'
  if (s < 3600) return `${Math.floor(s / 60)} 分钟前`
  if (s < 86400) return `${Math.floor(s / 3600)} 小时前`
  return `${Math.floor(s / 86400)} 天前`
}
</script>
