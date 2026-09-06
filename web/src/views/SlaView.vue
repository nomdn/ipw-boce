<template>
  <div>
    <!-- 顶栏：窗口 + 刷新 + 新建任务 -->
    <div class="toolbar">
      <div class="form-field">
        <label>统计窗口</label>
        <select class="ak-select" v-model="hours" @change="onWindowChange">
          <option :value="1">近 1 小时</option>
          <option :value="6">近 6 小时</option>
          <option :value="24">近 24 小时</option>
          <option :value="72">近 72 小时</option>
          <option :value="168">近 7 天</option>
        </select>
      </div>
      <button class="ak-button ak-button--outline" @click="load">刷新</button>
      <button class="ak-button ak-button--action" @click="openCreate">＋ 新建定时拨测</button>
      <span v-if="error" class="err">加载失败：{{ error }}</span>
      <span v-if="!loading && !tasks.length && !error" class="dim" style="font-size:.85rem">
        暂无定时拨测任务 —— 点击「新建定时拨测」配置一组，调度器会按时对节点拨测并累计 SLA
      </span>
    </div>
    <div v-if="loading" class="loading-center"><span class="ak-loading"></span></div>

    <!-- 新建任务表单（编辑任务的表单插入到对应任务卡下方，详见 sla-stack 内） -->
    <TaskFormPanel
      v-if="editingId === 0"
      :form="form" :target-hint="targetHint"
      :saving="saving" :msg="formMsg" :err="formErr"
      @submit="save" @cancel="cancelEdit"
    />

    <!-- 每任务一张 SLA 卡 -->
    <div v-if="!loading" class="sla-stack">
      <template v-for="tk in taskCards" :key="tk.task.id">
      <section class="panel sla-card">
        <div class="sla-head">
          <div class="sla-title">
            <span class="ak-tag ch type-tag">{{ apiLabel(tk.task.apiType) }}</span>
            <span class="mono name">{{ tk.task.name }}</span>
            <span class="dim tgt">{{ tk.task.target }}</span>
            <span class="dim gap">{{ every(tk.task) }} · 慢&gt;{{ tk.task.slowMs || 0 }}ms</span>
          </div>
          <div class="sla-ops">
            <button class="ak-button ak-button--outline sm" :class="{ off: !tk.task.enabled }" @click="toggle(tk.task)">{{ tk.task.enabled ? '运行中' : '已停' }}</button>
            <button class="ak-button ak-button--outline sm" @click="openEdit(tk.task)">编辑</button>
            <button class="ak-button ak-button--outline sm danger" @click="remove(tk.task)">✕</button>
          </div>
        </div>

        <!-- 判定摘要 + 全局 -->
        <div class="sla-sub dim">
          <span>判定: {{ judgeText(tk.task) }}</span>
          <span>节点 {{ nodeScopeText(tk.task) }}</span>
          <span>最近更新 {{ fmtTime(tk.task.updatedAt) }}</span>
          <span v-if="tk.task.ownerUsername" class="owner" :title="tk.task.ownerId ? '告警将发往该所有者' : ''">创建者 @{{ tk.task.ownerUsername }}</span>
        </div>

        <div class="sla-kpis">
          <div class="kpi" :class="upTone(tk.agg?.availability)"><div class="l">可用率</div><div class="v">{{ fmtPct(tk.agg?.availability) }}</div></div>
          <div class="kpi"><div class="l">平均延迟</div><div class="v">{{ tk.agg ? tk.agg.avgMs + ' ms' : '—' }}</div></div>
          <div class="kpi"><div class="l">错误率</div><div class="v" :class="errTone(tk.agg?.errRate)">{{ fmtPct(tk.agg?.errRate) }}</div></div>
          <div class="kpi"><div class="l">达标率</div><div class="v" :class="upTone(tk.agg?.metRate)">{{ fmtPct(tk.agg?.metRate) }}</div></div>
          <div class="kpi"><div class="l">样本</div><div class="v">{{ tk.agg?.samples ?? '—' }}</div></div>
        </div>

        <!-- 延迟曲线：一条折线，每样本点=单轮多节点平均延迟；有 down 的轮次标红带 -->
        <div class="sla-trend">
          <div class="sla-trend-cap">
            <span>延迟趋势 <span class="dim">/ 每个点 = 一轮多节点平均 · 红带 = 有节点失败的轮次 · 滚轮/底部条缩放</span></span>
            <span class="dim" v-if="hasFailure(seriesMap[tk.task.id])">存在失败轮次</span>
          </div>
          <EChart v-if="hasTrend(seriesMap[tk.task.id])" :option="tk.chart" height="150px" />
          <div v-else class="dim empty">该窗口暂无定时样本</div>
        </div>

        <!-- 每节点 -->
        <div v-if="tk.sla && tk.sla.byNode && tk.sla.byNode.length" class="ak-table-wrap">
          <table class="ak-table sla-node">
            <thead><tr>
              <th>节点</th><th>在线</th><th>可用</th><th>up/down</th><th>avg</th><th>max/p95</th><th>慢</th><th>最新</th><th>特殊字段</th>
            </tr></thead>
            <tbody>
              <tr v-for="n in tk.sla.byNode" :key="n.nodeId">
                <td class="mono nowrap">{{ n.nodeId }}</td>
                <td><span class="dot" :class="n.nodeOnline === false ? 'offline' : 'online'"></span></td>
                <td class="mono" :class="upTone(n.availability)">{{ fmtPct(n.availability) }}</td>
                <td class="mono"><span class="ok-200">{{ n.up }}</span>/<span class="err">{{ n.down }}</span><span v-if="n.invalid" class="dim">·{{ n.invalid }}?</span></td>
                <td class="mono">{{ n.avgMs }}ms</td>
                <td class="mono dim">{{ n.maxMs }}/{{ n.p95Ms }}</td>
                <td class="mono" :class="n.slow ? 'err' : 'ok-200'">{{ n.slow }}</td>
                <td class="mono">
                  <span class="dot" :class="n.latestUp ? 'online' : 'offline'"></span>{{ n.latestMs }}ms
                </td>
                <td class="special">{{ specialText(tk.task.apiType, n.special) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
        <div v-else class="dim empty">该窗口暂无定时样本：{{ tk.sla?.samples || 0 }} 条（确认任务已启用、间隔合理且节点可达）</div>
      </section>
      <!-- 编辑当前任务时把表单插入到该任务卡下方，而不是跳到列表顶部 -->
      <TaskFormPanel
        v-if="editingId === tk.task.id"
        :form="form" :target-hint="targetHint"
        :saving="saving" :msg="formMsg" :err="formErr"
        @submit="save" @cancel="cancelEdit"
      />
    </template>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, onBeforeUnmount } from 'vue'
import {
  fetchTasks, fetchTaskSla, createTask, updateTask, setTaskEnabled, deleteTask, fetchTaskMeta, fetchTaskSeries,
} from '../api/boce.js'
import { getToken } from '../api/http.js'
import { WS_BASE } from '../config.js'
import { fmtTime } from '../utils/format.js'
import { useDialog } from '../composables/useDialog.js'
import EChart from '../components/EChart.vue'
import TaskFormPanel from '../components/TaskFormPanel.vue'
import { apiOptions, apiLabel } from '../utils/probeMeta.js'

const dialog = useDialog()
const hours = ref(24)
// 拨测方案候选：fetchTaskMeta 返回的 types 可覆盖（追加业务后端新增的探针类）
const types = ref(apiOptions.map((o) => o.value))
const tasks = ref([])
const slaMap = ref({}) // taskId -> sla resp
const seriesMap = ref({}) // taskId -> 分桶时序（延迟曲线）
const loading = ref(false)
const error = ref('')

const editing = ref(false)
const saving = ref(false)
const formMsg = ref('')
const formErr = ref(false)
const form = ref(blankForm())
// 当前编辑的是哪个任务：0 = 新建任务（顶部表单）；其他 = 编辑该任务（表单插到对应卡片下方）
const editingId = ref(0)

function blankForm() {
  return {
    id: null, name: '', apiType: 'detail', target: '', stack: '', nodeScope: 'all', nodeIds: '',
    intervalSec: 60, slowMs: 0, expectStatus: '2xx',
    bothProtocols: true, requireAllStacks: true, certExpiredDown: true,
  }
}

// ---- SLA 实时推送（WS /console/sla）----
// 后端在每轮定时拨测落库后，把该任务整窗聚合 SLA 推过来，此处覆盖对应卡片即实时刷新。
// 断线指数退避自动重连；重连成功(非首次)补拉一次全量，补齐断线窗口错过的推送。
let ws = null
let wsTimer = null
let wsEverOpened = false
let wsUserClosed = false
let wsRetry = 0

function onWindowChange() {
  load()
  connectSla() // 窗口变了，重建 WS 用新 hours 订阅，避免收到的快照窗口错位
}

function connectSla() {
  if (wsUserClosed) return
  if (ws) { try { ws.onclose = null; ws.onmessage = null; ws.close() } catch {} }
  const token = getToken()
  const url =
    `${WS_BASE}/console/sla?hours=${hours.value}` +
    (token ? `&token=${encodeURIComponent(token)}` : '')
  try {
    ws = new WebSocket(url)
  } catch {
    scheduleReconnect()
    return
  }
  ws.onopen = () => {
    if (wsEverOpened) load() // 重连成功补拉全量，补齐错过的推送
    else wsEverOpened = true
    wsRetry = 0
  }
  ws.onmessage = (e) => {
    let m
    try { m = JSON.parse(e.data) } catch { return }
    // 后端推送帧：{ type:'sla', data:{ taskId, sla } }（taskId/sla 在 data 载荷内）
    const d = m?.data || {}
    if (m?.type === 'sla' && d.taskId && d.sla) applySlaSnapshot(d.taskId, d.sla)
  }
  ws.onclose = () => scheduleReconnect()
  ws.onerror = () => { try { ws.close() } catch {} }
}
function scheduleReconnect() {
  if (wsUserClosed) return
  if (wsTimer) clearTimeout(wsTimer)
  const delay = Math.min(15000, 1000 * 2 ** wsRetry++) // 1s→2s→…→15s 封顶
  wsTimer = setTimeout(connectSla, delay)
}
function closeSla() {
  wsUserClosed = true
  if (wsTimer) clearTimeout(wsTimer)
  if (ws) { try { ws.onclose = null; ws.close() } catch {} }
  ws = null
}
// 用推送快照覆盖对应任务卡片（仅当推送窗口与当前所选窗口一致才应用，避免错位）
function applySlaSnapshot(taskId, sla) {
  if (!matchWindow(sla.window)) return
  slaMap.value = { ...slaMap.value, [taskId]: sla }
  // 顺带刷新该任务时序（失败段红标需最新 down 桶）
  refreshTaskSeries(taskId)
}
// 判断后端推送窗口是否等于当前 hours（后端 window.from/to 为 UTC）
function matchWindow(win) {
  if (!win?.from || !win?.to) return false
  const spanH = (new Date(win.to) - new Date(win.from)) / 3600000
  return Math.abs(spanH - Number(hours.value)) < 1
}

onMounted(() => {
  load()
  connectSla()
})
onBeforeUnmount(closeSla)

async function load() {
  loading.value = true
  error.value = ''
  try {
    const meta = await fetchTaskMeta().catch(() => null)
    if (meta?.types) types.value = meta.types
    const list = await fetchTasks()
    tasks.value = list || []
    // 并发拉取每个任务的 SLA 与时序曲线
    const res = await Promise.all(
      tasks.value.map(async (t) => {
        const [sla, series] = await Promise.all([
          fetchTaskSla(t.id, hours.value).catch(() => null),
          fetchTaskSeries(t.id, hours.value).catch(() => null),
        ])
        return [t.id, { sla, series }]
      }),
    )
    const sm = {}
    const sem = {}
    res.forEach(([id, v]) => {
      sm[id] = v.sla
      sem[id] = v.series
    })
    slaMap.value = sm
    seriesMap.value = sem
  } catch (e) {
    error.value = e?.message || 'load failed'
  } finally {
    loading.value = false
  }
}

// 拉取单个任务时序（WS 推送后刷新该卡片的失败段红标）
async function refreshTaskSeries(taskId) {
  try {
    const series = await fetchTaskSeries(taskId, hours.value)
    seriesMap.value = { ...seriesMap.value, [taskId]: series }
  } catch {
    /* 静默 */
  }
}

const taskCards = computed(() =>
  tasks.value.map((task) => ({
    task,
    sla: slaMap.value[task.id] || null,
    agg: aggregate(task, slaMap.value[task.id]),
    chart: slaTrendOption(seriesMap.value[task.id] || null),
  })),
)

// 汇总一个任务全部节点 → 全局指标（可选率等取后端 byNode 的可用率均值加权不算精，直接用 up/样本）
function aggregate(task, sla) {
  if (!sla || !Array.isArray(sla.byNode) || !sla.byNode.length) return null
  const by = sla.byNode
  let samples = 0, up = 0, down = 0, invalid = 0, slow = 0, sumMs = 0, nLat = 0, met = 0
  by.forEach((n) => {
    samples += n.samples; up += n.up; down += n.down; invalid += n.invalid
    slow += n.slow; met += (n.up - n.slow)
    if (n.avgMs && n.samples) { sumMs += n.avgMs * n.samples; nLat += n.samples }
  })
  const denom = up + down || 0
  return {
    samples,
    availability: denom ? Math.round((up / denom) * 10000) / 100 : 0,
    errRate: denom ? Math.round((down / denom) * 10000) / 100 : 0,
    metRate: samples ? Math.round((met / samples) * 10000) / 100 : 0,
    avgMs: nLat ? Math.round(sumMs / nLat) : 0,
    invalid,
  }
}

const targetHint = computed(() => {
  switch (form.value.apiType) {
    case 'ssl': return '域名，如 www.example.com'
    case 'detail': return '域名，如 www.example.com'
    case 'tcping': return 'host[:port]，如 1.1.1.1:443'
    case 'speed': return '测速文件 URL，如 https://host/file（栈选 v4/v6）'
    default: return ''
  }
})

function every(t) {
  return `每 ${t.intervalSec}s`
}
function judgeText(t) {
  const p = [t.expectStatus || '2xx']
  if (t.apiType === 'detail') p.push(t.bothProtocols ? 'http+https都中' : '任一协议中')
  if (t.apiType === 'ssl' || t.apiType === 'detail') p.push(t.requireAllStacks ? '双栈全通' : '任一栈通')
  if (t.apiType === 'ssl') p.push(t.certExpiredDown ? '过期即fail' : '过期仅提示')
  return p.join(' · ')
}
function nodeScopeText(t) {
  return t.nodeScope === 'custom' ? `仅 ${t.nodeIds}` : '全池'
}
function specialText(apiType, sp) {
  if (!sp) return '—'
  if (apiType === 'ssl') {
    const parts = []
    if (sp.domain) parts.push(sp.domain)
    if (sp.cert_validity_days != null) parts.push(`证书 ${sp.cert_validity_days} 天`)
    if (sp.cert_end_time) parts.push(`至 ${fmtTime(sp.cert_end_time).slice(0, 10)}`)
    return parts.join(' · ') || '—'
  }
  if (apiType === 'detail') {
    const parts = []
    if (sp.download_speed != null) parts.push(`↓ ${Number(sp.download_speed).toFixed(2)} MB/s`)
    if (sp.page_size != null) parts.push(`${fmtNum(sp.page_size)}B`)
    return parts.join(' · ') || '—'
  }
  return '—'
}

function fmtNum(n) {
  if (n == null) return '—'
  if (n >= 1e6) return (n / 1e6).toFixed(1) + 'M'
  if (n >= 1e3) return (n / 1e3).toFixed(1) + 'K'
  return String(Math.round(n))
}
function fmtPct(v) {
  if (v == null) return '—'
  return Number(v).toFixed(1) + '%'
}
function upTone(v) {
  if (v == null) return ''
  return v >= 95 ? 'ok' : v >= 80 ? 'warn' : 'bad'
}
function errTone(v) {
  if (v == null) return ''
  return v <= 1 ? 'ok' : v <= 10 ? 'warn' : 'bad'
}

// ===== SLA 延迟曲线（复用 app.css --chart-* 语义色，与大盘一致）=====
function readVar(name) {
  try { return getComputedStyle(document.documentElement).getPropertyValue(name).trim() || null } catch { return null }
}
const CH = {
  cyan: readVar('--chart-cyan') || '#2a9df4',
  yellow: readVar('--chart-yellow') || '#ffd802',
  green: readVar('--chart-green') || '#46c47c',
  red: readVar('--chart-red') || '#e33b3b',
  grid: readVar('--chart-grid') || '#1f2a33',
  axis: readVar('--chart-axis') || 'rgba(240,240,235,.45)',
  label: readVar('--chart-label') || 'rgba(248,248,245,.72)',
}
const chartAxis = {
  axisLine: { lineStyle: { color: CH.axis } },
  axisLabel: { color: CH.label },
  splitLine: { lineStyle: { color: CH.grid } },
}
const chartTooltip = {
  backgroundColor: '#101316', borderColor: 'rgba(255,255,255,.2)', textStyle: { color: '#f8f8f5' },
}
// RFC3339 桶时间 → 毫秒时间戳（value x 轴用）
function bucketMs(iso) {
  const d = iso ? new Date(iso) : null
  return d && !Number.isNaN(d.getTime()) ? d.getTime() : null
}
// RFC3339 桶时间 → 短标签（HH:mm；跨日含 MM-DD）
function bucketLabel(iso) {
  if (!iso) return ''
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ''
  const p = (n) => String(n).padStart(2, '0')
  const hm = `${p(d.getHours())}:${p(d.getMinutes())}`
  return d.toDateString() === new Date().toDateString() ? hm : `${p(d.getMonth() + 1)}-${p(d.getDate())} ${hm}`
}

// 是否有分桶样本可画
function hasTrend(series) {
  return Array.isArray(series?.series) && series.series.length > 0
}
// 窗口内是否存在失败样本
function hasFailure(series) {
  return Array.isArray(series?.series) && series.series.some((s) => s.down > 0)
}

// 延迟曲线 option：青色 avgMs 折线 + 失败(down>0)时间段红色 markArea 带。
// x 用 value/time（毫秒），避免 category 跨日标签重复导致 markArea 锚定歧义。
function slaTrendOption(series) {
  const rows = Array.isArray(series?.series) ? series.series : []
  const xv = rows.map((s) => bucketMs(s.time))
  // 有有效延迟样本才画点，否则断线（无数据留白）
  const avg = rows.map((s, i) => {
    const t = xv[i]
    const v = typeof s.avgMs === 'number' && s.avgMs > 0 ? s.avgMs : null
    return t == null || v == null ? null : [t, v]
  })
  // 失败段：把连续 down>0 的桶聚成区间。区间左边界 = 段内首桶起点；
  // 右边界 = 段内末桶的下一个桶起点（若无下一个桶则用末桶起点），确保整段连续失败被完整覆盖。
  // （旧实现只在段首设 endI 不随段内更新，连续失败段被压成单桶宽，红线几乎不可见。）
  const areas = []
  let segStart = null // 当前失败段首桶起点（ms）
  let lastFailT = null // 当前失败段末桶起点（ms）
  for (let i = 0; i < rows.length; i++) {
    const fail = rows[i].down > 0
    if (fail) {
      if (segStart === null) segStart = xv[i]
      lastFailT = xv[i]
    } else if (segStart !== null) {
      // 段结束：右边界取到失败段紧邻的下一个桶起点，覆盖完整
      areas.push([{ name: '失败', xAxis: segStart }, { xAxis: xv[i] }])
      segStart = null
      lastFailT = null
    }
  }
  if (segStart !== null) {
    // 段延伸到末尾：无下一桶，右边界取末桶起点（仍有宽度 > 0）
    areas.push([{ name: '失败', xAxis: segStart }, { xAxis: lastFailT }])
  }
  return {
    color: [CH.cyan],
    tooltip: {
      trigger: 'axis',
      axisPointer: { type: 'line' },
      ...chartTooltip,
      formatter(params) {
        const arr = Array.isArray(params) ? params : [params]
        const x = arr[0]?.value
        const ms = Array.isArray(x) ? x[0] : x
        if (!ms) return ''
        let di = -1
        for (let i = 0; i < xv.length; i++) if (xv[i] === ms) { di = i; break }
        const r = di >= 0 ? rows[di] : null
        const head = `<div style="font-weight:600;margin-bottom:4px">${bucketLabel(ms)}</div>`
        const val = Array.isArray(x) && typeof x[1] === 'number' ? `${x[1]} ms` : '无'
        const line = `<div>${arr[0]?.marker || ''}平均延迟：${val}</div>`
        const fail = r && r.down > 0 ? `<div style="color:${CH.red};margin-top:2px">失败 ${r.down}/${r.samples} · 可用率 ${r.availability}%</div>` : ''
        return head + line + fail
      },
    },
    grid: { left: 46, right: 16, top: 18, bottom: 34 },
    // 缩放看细节：滚轮/双指缩放(inside) + 底部缩放条(slider,可拖可平移)；时间型 x 轴，缩放不改变曲线本身
    dataZoom: [
      { type: 'inside', zoomOnMouseWheel: true, moveOnMouseMove: true, moveOnMouseWheel: false, filterMode: 'none' },
      {
        type: 'slider', height: 14, bottom: 6, filterMode: 'none',
        borderColor: 'transparent', backgroundColor: 'rgba(255,255,255,.04)',
        fillerColor: 'rgba(42,157,244,.16)', dataBackgroundColor: 'rgba(255,255,255,.05)',
        textStyle: { color: CH.label, fontSize: 10 },
        handleStyle: { color: CH.cyan, borderColor: CH.cyan },
        moveHandleStyle: { color: 'rgba(255,255,255,.18)' },
      },
    ],
    xAxis: { type: 'time', ...chartAxis, axisLabel: { ...chartAxis.axisLabel, formatter: (ms) => bucketLabel(ms), hideOverlap: true } },
    yAxis: { type: 'value', name: 'ms', ...chartAxis },
    series: [
      {
        name: '平均延迟', type: 'line', smooth: true, showSymbol: false, connectNulls: false,
        lineStyle: { width: 2 },
        areaStyle: { opacity: 0.1 },
        data: avg,
        markArea: {
          silent: true,
          itemStyle: { color: 'rgba(227,59,59,0.15)' },
          data: areas,
        },
      },
    ],
  }
}

// ---- 任务 CRUD ----
function openCreate() {
  form.value = blankForm()
  editing.value = true
  editingId.value = 0 // 新建：顶部表单
  formMsg.value = ''
}
function openEdit(t) {
  form.value = {
    id: t.id, name: t.name, apiType: t.apiType, target: t.target, stack: t.stack || '',
    nodeScope: t.nodeScope || 'all', nodeIds: t.nodeIds || '', intervalSec: t.intervalSec,
    slowMs: t.slowMs || 0, expectStatus: t.expectStatus || '2xx',
    bothProtocols: t.bothProtocols, requireAllStacks: t.requireAllStacks, certExpiredDown: t.certExpiredDown,
  }
  editing.value = true
  editingId.value = t.id // 编辑：该任务卡下方插入表单
  formMsg.value = ''
}
function cancelEdit() { editing.value = false; editingId.value = 0; formMsg.value = '' }

async function save() {
  saving.value = true
  formErr.value = false
  formMsg.value = ''
  try {
    const payload = {
      name: form.value.name, apiType: form.value.apiType, target: form.value.target,
      stack: form.value.stack, nodeScope: form.value.nodeScope, nodeIds: form.value.nodeIds,
      intervalSec: form.value.intervalSec, slowMs: form.value.slowMs || 0,
      expectStatus: form.value.expectStatus || '2xx',
      bothProtocols: form.value.bothProtocols, requireAllStacks: form.value.requireAllStacks,
      certExpiredDown: form.value.certExpiredDown,
    }
    if (form.value.id) await updateTask(form.value.id, payload)
    else await createTask(payload)
    formMsg.value = '已保存'
    cancelEdit()
    await load()
  } catch (e) {
    formErr.value = true
    formMsg.value = e?.message || '保存失败'
  } finally {
    saving.value = false
  }
}

async function toggle(t) {
  try {
    await setTaskEnabled(t.id, !t.enabled)
    await load()
  } catch (e) { error.value = e?.message || '操作失败' }
}
async function remove(t) {
  const ok = await dialog.confirm({
    title: `删除任务「${t.name}」？`,
    message: '任务定义及其全部历史拨测数据将被清退，操作不可撤销。',
    kind: 'danger',
    confirmText: '删除',
    cancelText: '取消',
  })
  if (!ok) return
  try {
    await deleteTask(t.id)
    await load()
  } catch (e) { error.value = e?.message || '删除失败' }
}
</script>

<style scoped>
/* 本页所有 .ak-button 都按内容收缩（ak-ui 默认固定 150×50 只适合主行动按钮） */
.toolbar :deep(.ak-button),
.form-row :deep(.ak-button) {
  width: auto;
  height: auto;
  padding: 6px 12px;
}
.sla-stack { display: flex; flex-direction: column; gap: 16px; }
.sla-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; }
.sla-title { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; min-width: 0; }
.sla-title .name { font-weight: 600; font-size: .95rem; }
.sla-title .tgt { font-size: .82rem; word-break: break-all; }
.sla-title .gap { font-size: .78rem; }
.type-tag { text-transform: none; }
.sla-ops { display: flex; gap: 6px; flex: none; }
.sla-ops .ak-button.sm { width: auto; height: auto; padding: 3px 9px; font-size: .72rem; font-weight: 400; }
.sla-ops .ak-button.off { opacity: .5; }
.sla-ops .ak-button.danger { color: var(--ak-signal-danger); border-color: var(--ak-signal-danger); }
.sla-sub { display: flex; flex-wrap: wrap; gap: 4px 14px; margin-top: 8px; font-size: .72rem; }
.sla-sub .owner { color: var(--ak-signal-accent); font-family: var(--ak-font-mono); }
.sla-kpis { display: grid; grid-template-columns: repeat(auto-fit, minmax(120px, 1fr)); gap: 10px; margin: 14px 0; }
.sla-kpis .kpi { background: rgba(255,255,255,.03); border: 1px solid rgba(255,255,255,.06); padding: 10px 12px; }
.sla-kpis .kpi .l { font-size: .7rem; color: var(--ak-text-secondary); }
.sla-kpis .kpi .v { font-family: var(--ak-font-mono); font-size: 1.25rem; font-weight: 600; margin-top: 2px; }
.sla-kpis .kpi.ok .v { color: var(--ak-signal-success); }
.sla-kpis .kpi.warn .v { color: var(--ak-signal-action); }
.sla-kpis .kpi.bad .v { color: var(--ak-signal-danger); }
.sla-node .special { color: var(--ak-text-secondary); font-size: .76rem; }
.empty { padding: 10px 2px; font-size: .82rem; }
.chk { display: inline-flex; align-items: center; gap: 6px; font-size: .74rem; color: var(--ak-text-secondary); }
.chk input { accent-color: var(--ak-signal-info); }
.ok { color: var(--ak-signal-success); }
.warn { color: var(--ak-signal-action); }
.bad { color: var(--ak-signal-danger); }
.sla-trend { margin: 6px 0 4px; border: 1px solid rgba(255,255,255,.05); background: rgba(255,255,255,.015); padding: 8px 10px 2px; }
.sla-trend-cap { display: flex; justify-content: space-between; align-items: baseline; font-size: .72rem; color: var(--ak-text-secondary); margin-bottom: 4px; }
.sla-trend-cap .dim { font-size: .7rem; }
.sla-trend-cap .dim:last-child { color: var(--chart-red); }
</style>
