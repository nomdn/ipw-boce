<template>
  <div>
    <!-- ============ admin：全站流量大盘（request_stats） ============ -->
    <template v-if="isAdmin">
      <div class="toolbar">
        <div class="tb-group">
          <div class="form-field">
            <label>统计窗口</label>
            <TimeRangePicker
              :model-value="range"
              :max-days="rangeMaxDays"
              aria-label="统计窗口"
              @update:model-value="onRangeChange"
            />
          </div>
          <div class="form-field">
            <label>节点范围</label>
            <select class="ak-select" v-model="nodeScope" @change="onScopeChange">
              <option value="all">全部节点</option>
              <option value="picked">只看选定节点</option>
            </select>
          </div>
        </div>
        <div class="tb-group tb-group--actions">
          <button class="ak-button ak-button--outline" @click="loadAdmin">刷新</button>
          <button v-if="activeFilter" class="ak-button ak-button--outline" @click="clearFilter">
            清除筛选（{{ pickedNodes.length }}）
          </button>
        </div>
        <span v-if="error" class="err">加载失败：{{ error }}</span>
      </div>

      <!-- 节点筛选：勾选即只看这些节点；一个都不勾 = 全部节点 -->
      <div v-if="nodeScope === 'picked'" class="node-filter">
        <div class="nf-head">
          <span class="nf-title">只看这些节点</span>
          <span class="dim">{{ pickedNodes.length ? `已选 ${pickedNodes.length} / ${allNodes.length}` : '未勾选 = 全部节点' }}</span>
          <span class="nf-spacer"></span>
          <button type="button" class="nf-btn" @click="pickAll">全选</button>
          <button type="button" class="nf-btn" @click="pickNone">清空</button>
        </div>
        <div class="node-picks">
          <label v-for="n in allNodes" :key="n.nodeId" class="node-pick"
            :title="`${n.nodeId} · 窗口内 ${num(n.total)} 次请求`">
            <input type="checkbox" :value="n.nodeId" v-model="pickedNodes" />
            <span class="dot" :class="pickedNodes.includes(n.nodeId) ? 'online' : 'offline'"></span>
            <span :title="n.nodeId">{{ n.label || n.nodeId }}</span>
            <span class="dim">{{ num(n.total) }}</span>
          </label>
          <span v-if="!allNodes.length" class="dim">当前窗口内还没有任何节点上报</span>
        </div>
      </div>
      <div v-if="loading" class="loading-center"><span class="ak-loading"></span></div>

      <div class="kpi-strip" style="margin-bottom: 16px">
        <div class="kpi-card"><div class="kpi-label">请求总数</div><div class="kpi-value">{{ num(total) }}</div><div class="kpi-sub">{{ rangeShort }}</div></div>
        <div class="kpi-card kpi--danger"><div class="kpi-label">错误</div><div class="kpi-value">{{ num(errors) }}</div><div class="kpi-sub">错误率 {{ pct(errors, total) }}</div></div>
        <div class="kpi-card kpi--success"><div class="kpi-label">平均延迟</div><div class="kpi-value" style="font-size:1.5rem">{{ avgMs(latSum, total) }}</div><div class="kpi-sub">端到端</div></div>
        <div class="kpi-card kpi--accent"><div class="kpi-label">接口类型</div><div class="kpi-value">{{ num(apiCount) }}</div><div class="kpi-sub">whois / dns / ssl…</div></div>
        <div class="kpi-card kpi--action">
          <div class="kpi-label">上报节点</div>
          <div class="kpi-value">{{ activeFilter ? `${nodeCount} / ${allNodes.length}` : num(nodeCount) }}</div>
          <div class="kpi-sub">{{ activeFilter ? '已筛选' : '有统计' }}</div>
        </div>
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
              <thead><tr><th>接口类型</th><th>总量</th><th>错误</th><th>错误率</th><th>平均延迟</th></tr></thead>
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
        <div class="tb-group">
          <div class="form-field">
            <label>统计窗口</label>
            <TimeRangePicker
              :model-value="range"
              :max-days="rangeMaxDays"
              aria-label="统计窗口"
              @update:model-value="onRangeChange"
            />
          </div>
        </div>
        <div class="tb-group tb-group--actions">
          <button class="ak-button ak-button--outline" @click="loadMine">刷新</button>
        </div>
        <span v-if="error" class="err">加载失败：{{ error }}</span>
      </div>
      <div v-if="loading" class="loading-center"><span class="ak-loading"></span></div>

      <div class="kpi-strip" style="margin-bottom:16px">
        <div class="kpi-card kpi--accent"><div class="kpi-label">我的任务</div><div class="kpi-value">{{ myTasks.length }}</div><div class="kpi-sub">运行 {{ runningCnt }} / 已停 {{ myTasks.length - runningCnt }}</div></div>
        <div class="kpi-card"><div class="kpi-label">平均可用率</div><div class="kpi-value" :style="availTone">{{ avgAvail }}%</div><div class="kpi-sub">{{ rangeShort }} 加权</div></div>
        <div class="kpi-card kpi--danger"><div class="kpi-label">累计掉线</div><div class="kpi-value">{{ sumDown }}</div><div class="kpi-sub">最近窗口</div></div>
        <div class="kpi-card kpi--success"><div class="kpi-label">样本总数</div><div class="kpi-value">{{ numMy(sumSamples) }}</div><div class="kpi-sub">定时采集</div></div>
        <div class="kpi-card"><div class="kpi-label">平均延迟</div><div class="kpi-value">{{ avgMsAll ? avgMsAll + ' ms' : '—' }}</div><div class="kpi-sub">按样本加权</div></div>
      </div>

      <!-- 我的任务趋势曲线 -->
      <section class="panel" style="margin-bottom:16px">
        <h2 class="panel-title">我的可用率趋势</h2>
        <EChart :option="mineTrendOption" height="300px" />
        <div v-if="!loading && !mySeries.length && !error" class="dim" style="padding:4px 2px">
          暂无定时样本 —— 任务首次拨测完成后，这里会出现可用率曲线。
        </div>
      </section>

      <div class="panel">
        <h2 class="panel-title">我的统计任务
          <router-link class="ak-button ak-button--outline sm" style="float:right" :to="{ name: 'sla' }">进入 SLA 监控</router-link>
        </h2>
        <div v-if="!loading && !myTasks.length && !error" class="dim" style="padding:8px 2px">
          你还没有定时拨测任务 —— 到「SLA 监控」新建一个，系统会按时对节点拨测并累计这份报告。
        </div>
        <div v-else class="ak-table-wrap">
          <table class="ak-table">
            <thead><tr><th>任务</th><th>类型</th><th>目标</th><th>状态</th><th>可用率</th><th>平均延迟</th><th>样本</th><th>掉线</th><th>最近更新</th></tr></thead>
            <tbody>
              <tr v-for="c in myCards" :key="c.task.id" @click="$router.push({ name: 'sla' })" style="cursor:pointer">
                <td class="mono" :title="c.task.name">{{ c.task.name }}</td>
                <td class="mono nowrap"><span class="ak-tag ch">{{ c.task.apiType }}</span></td>
                <td class="mono dim" style="max-width:220px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">{{ c.task.target }}</td>
                <td><span class="dot" :class="c.task.enabled ? 'online' : 'offline'"></span>{{ c.task.enabled ? '运行中' : '已停' }}</td>
                <td class="mono" :style="c.agg ? availStyle(c.agg.availability) : ''">{{ c.agg ? fmtPct(c.agg.availability) : '—' }}</td>
                <td class="mono">{{ c.agg ? c.agg.avgMs + ' ms' : '—' }}</td>
                <td class="mono">{{ c.agg ? c.agg.samples : '—' }}</td>
                <td class="mono" :class="c.agg && c.agg.errCnt ? 'err' : ''">{{ c.agg ? c.agg.errCnt : '—' }}</td>
                <td class="dim" style="white-space:nowrap">{{ timeAgo(c.sla?.window?.to) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </template>
  </div>
</template>

<script setup>
import { ref, reactive, computed, onMounted, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useAuthStore } from '../stores/auth.js'
import EChart from '../components/EChart.vue'
import TimeRangePicker from '../components/TimeRangePicker.vue'
import { useTheme, readChartTheme } from '../composables/useTheme.js'
import { fetchStatsSummary, fetchStatsTimeseries, fetchTasks, fetchTaskSla, fetchMineSeries, fetchTimeRange } from '../api/boce.js'
import { num, avgMs, pct, fmtMinute, timeAgo } from '../utils/format.js'
import { absLabel, fromQuery, rangeQuery, resolveRange, toQuery } from '../utils/timeRange.js'

const auth = useAuthStore()
const route = useRoute()
const router = useRouter()
const isAdmin = computed(() => auth.isAdmin)
// 统计窗口：{ key, start?, end? } —— 相对窗口 / 自然周期 / 自定义区间，见 utils/timeRange.js。
// 初值取自地址栏，于是窗口可分享、可回退、刷新不丢。
const range = ref(fromQuery(route.query))
const rangeMaxDays = ref(0) // 后端可查上限（天），用于隐藏必然查空的预设项
const winQS = computed(() => rangeQuery(range.value))
// KPI 副标题与选择器按钮保持同一口径
const rangeShort = computed(() => {
  const r = resolveRange(range.value)
  return r.kind === 'abs' ? absLabel(r.from, r.to) : r.label
})
const loading = ref(false)
const error = ref('')

// 请求代次：连点窗口 / 反复勾节点时，后发的请求可能先回来，用代次丢弃过期结果
let runSeq = 0

// 窗口切换：写回地址栏后按角色重新加载
function onRangeChange(next) {
  range.value = next
  syncUrl()
  load()
}
function syncUrl() {
  const q = { ...route.query, ...toQuery(range.value) }
  if (range.value.key !== 'custom') {
    delete q.start
    delete q.end
  }
  router.replace({ query: q })
}

onMounted(() => {
  load()
  // 可查范围（保留期 / 接口上限）：选择器据此隐藏必然查空的预设
  fetchTimeRange()
    .then((r) => { rangeMaxDays.value = r?.maxDays || 0 })
    .catch(() => {})
})
function load() {
  isAdmin.value ? loadAdmin() : loadMine()
}

// ================= admin：request_stats 大盘 =================
const summary = ref(null)
const series = ref([])

// ---- 节点筛选：只看部分节点（勾多个）/ 只看一个节点（只勾一个）----
// 统计口径是「分钟 × 节点 × 接口类型」，多节点天然各算各的；这里只在展示侧过滤，不改后端聚合粒度，
// 带上 nodes 参数即可（后端解析见 admin.go statsNodes）。
const nodeScope = ref('all')   // all = 全部节点；picked = 只看勾选的
const pickedNodes = ref([])    // 勾选的节点 ID；空 = 全部（与后端语义一致：空即不过滤）
const allNodes = computed(() =>
  (summary.value?.allNodes || []).slice().sort((a, b) => b.total - a.total),
)
const filterNodes = computed(() =>
  nodeScope.value === 'picked' && pickedNodes.value.length ? pickedNodes.value.slice() : [],
)
const activeFilter = computed(() => filterNodes.value.length > 0)

async function loadAdmin() {
  const my = ++runSeq
  loading.value = true
  error.value = ''
  const nodes = filterNodes.value
  const qs = winQS.value
  try {
    const [s, ts] = await Promise.all([
      fetchStatsSummary(qs, nodes),
      fetchStatsTimeseries(qs, nodes),
    ])
    if (my !== runSeq) return // 已被更新的请求取代
    summary.value = s
    series.value = ts || []
  } catch (e) {
    if (my !== runSeq) return
    error.value = e?.message || '加载失败'
  } finally {
    if (my === runSeq) loading.value = false
  }
}

// 勾选变化（数组被整体替换）即重新拉取；处于「全部节点」时不必请求
watch(pickedNodes, () => {
  if (nodeScope.value === 'picked') loadAdmin()
})
function onScopeChange() { loadAdmin() }
function pickAll() { pickedNodes.value = allNodes.value.map((n) => n.nodeId) }
// 清空勾选 = 回到全部节点（而不是"什么都不看"）
function pickNone() { pickedNodes.value = [] }
function clearFilter() {
  nodeScope.value = 'all'
  pickedNodes.value = []
  loadAdmin()
}

const byApiType = computed(() => (summary.value?.byApiType || []).slice().sort((a, b) => b.total - a.total))
const byNode = computed(() => (summary.value?.byNode || []).slice().sort((a, b) => b.total - a.total))
const total = computed(() => byApiType.value.reduce((s, x) => s + x.total, 0))
const errors = computed(() => byApiType.value.reduce((s, x) => s + x.errors, 0))
const latSum = computed(() => byApiType.value.reduce((s, x) => s + (x.latencySumMs || 0), 0))
const apiCount = computed(() => byApiType.value.length)
const nodeCount = computed(() => byNode.value.length)

// ---- 配色（与 theme.css --chart-* 语义对齐）----
// 信号色与主题无关；网格/轴/label/提示框属中性色，随主题走——
// echarts 画在 canvas 上吃不到 CSS 变量，故读一次存 reactive，切主题时重读。
const { theme } = useTheme()
const C = reactive({ ...readChartTheme() })
const axis = reactive({
  axisLine: { lineStyle: { color: C.axis } },
  axisLabel: { color: C.label },
  splitLine: { lineStyle: { color: C.grid } },
})
const tooltipBase = reactive({
  backgroundColor: C.tipBg, borderColor: C.tipBorder, textStyle: { color: C.tipText },
})
watch(theme, () => {
  Object.assign(C, readChartTheme())
  axis.axisLine.lineStyle.color = C.axis
  axis.axisLabel.color = C.label
  axis.splitLine.lineStyle.color = C.grid
  tooltipBase.backgroundColor = C.tipBg
  tooltipBase.borderColor = C.tipBorder
  tooltipBase.textStyle.color = C.tipText
})
const trendOption = computed(() => ({
  color: [C.cyan, C.red],
  tooltip: { trigger: 'axis', ...tooltipBase },
  legend: { textStyle: { color: C.label }, top: 0, data: ['请求', '错误'] },
  grid: { left: 56, right: 16, top: 32, bottom: 26 },
  xAxis: { type: 'category', boundaryGap: false, data: series.value.map((s) => fmtMinute(s.minute)), ...axis },
  yAxis: { type: 'value', minInterval: 1, ...axis },
  series: [
    { name: '请求', type: 'line', smooth: true, showSymbol: false, areaStyle: { opacity: 0.08 }, data: series.value.map((s) => s.total) },
    { name: '错误', type: 'line', smooth: true, showSymbol: false, data: series.value.map((s) => s.errors) },
  ],
}))
const typeOption = computed(() => ({
  color: [C.cyan], tooltip: { trigger: 'axis', ...tooltipBase },
  grid: { left: 56, right: 16, top: 12, bottom: 34 },
  xAxis: { type: 'category', data: byApiType.value.map((x) => x.apiType), ...axis, axisLabel: { ...axis.axisLabel, rotate: 0 } },
  yAxis: { type: 'value', minInterval: 1, ...axis },
  series: [{ type: 'bar', barMaxWidth: 26, data: byApiType.value.map((x) => x.total), itemStyle: { color: C.cyan, borderRadius: [4, 4, 0, 0] } }],
}))
const nodeOption = computed(() => ({
  color: [C.bar], tooltip: { trigger: 'axis', ...tooltipBase },
  grid: { left: 56, right: 16, top: 12, bottom: 26 },
  xAxis: { type: 'category', data: byNode.value.map((x) => shortNode(x.label || x.nodeId)), ...axis, axisLabel: { ...axis.axisLabel, interval: 0 } },
  yAxis: { type: 'value', minInterval: 1, ...axis },
  series: [{ type: 'bar', barMaxWidth: 28, data: byNode.value.map((x) => x.total), itemStyle: { color: C.bar, borderRadius: [4, 4, 0, 0] } }],
}))
function shortNode(id) { return (id || '').length > 16 ? id.slice(0, 16) + '…' : id }

// ================= user：我的任务报告 =================
const myTasks = ref([])
const mySla = ref({})
const mySeries = ref([])
async function loadMine() {
  const my = ++runSeq
  loading.value = true
  error.value = ''
  const qs = winQS.value
  try {
    const [list, ser] = await Promise.all([
      fetchTasks(),
      fetchMineSeries(qs).catch(() => []),
    ])
    if (my !== runSeq) return
    myTasks.value = list || []
    mySeries.value = Array.isArray(ser) ? ser : (ser?.series || [])
    const res = await Promise.all(
      myTasks.value.map(async (t) => {
        const sla = await fetchTaskSla(t.id, qs).catch(() => null)
        return [t.id, sla]
      }),
    )
    if (my !== runSeq) return
    mySla.value = Object.fromEntries(res)
  } catch (e) {
    if (my !== runSeq) return
    error.value = e?.message || '加载失败'
  } finally {
    if (my === runSeq) loading.value = false
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
      { name: '可用率', type: 'line', smooth: true, showSymbol: false, connectNulls: true, areaStyle: { opacity: 0.08 }, data: avail },
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
</script>

<style scoped>
/* ===== 统计大盘：节点筛选条 ===== */
.node-filter {
  border: var(--ak-line-hairline) solid var(--ui-line);
  border-left: 3px solid var(--ak-signal-info);
  background: var(--ui-tint);
  padding: 9px 12px 10px;
  margin: -6px 0 16px;
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.nf-head { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; font-size: 0.78rem; }
.nf-spacer { flex: 1; }
.nf-btn {
  font-family: inherit;
  font-size: 0.72rem;
  padding: 3px 9px;
  cursor: pointer;
  color: inherit;
  background: transparent;
  border: var(--ak-line-hairline) solid var(--ui-line-ctl);
}
.nf-btn:hover { background: var(--ui-tint-hover); border-color: var(--ui-line-hover); }
.node-picks {
  display: flex;
  flex-wrap: wrap;
  gap: 6px 14px;
  align-items: center;
  max-height: 132px;
  overflow: auto;
}
.node-pick {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  font-size: 0.8rem;
  cursor: pointer;
  white-space: nowrap;
}
.node-pick .dot { width: 7px; height: 7px; border-radius: 50%; display: inline-block; }
.node-pick .dim { font-size: 0.72rem; }
</style>
