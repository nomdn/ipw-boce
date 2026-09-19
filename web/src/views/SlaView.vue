<template>
  <div>
    <!-- 顶栏：左边一列下拉 / 输入（参数），右边一排按钮（操作），两类各自成组、不穿插 -->
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
          <label>标签过滤</label>
          <input class="ak-input" v-model.trim="tagFilter" @keyup.enter="load(true)" placeholder="留空 = 全部" style="width:140px" />
        </div>
      </div>
      <div class="tb-group tb-group--actions">
        <button class="ak-button ak-button--outline" @click="load">刷新</button>
        <button class="ak-button ak-button--outline" :disabled="!tasks.length" @click="toggleSelectAll">
          {{ allSelected ? '取消全选' : '全选' }}
        </button>
        <button class="ak-button ak-button--outline" :disabled="!selCount" @click="openBatchShare">
          分享所选{{ selCount ? `（${selCount}）` : '' }}
        </button>
        <button class="ak-button ak-button--action" @click="openCreate">＋ 新建定时拨测</button>
      </div>
      <span v-if="error" class="err">加载失败：{{ error }}</span>
      <span v-if="!loading && !tasks.length && !error" class="dim" style="font-size:.85rem">
        暂无定时拨测任务 —— 点击「新建定时拨测」配置一组，系统会按时对节点拨测并累计 SLA
      </span>
    </div>
    <div v-if="loading && !tasks.length" class="loading-center"><span class="ak-loading"></span></div>

    <!-- 新建任务表单（编辑任务的表单插入到对应任务卡下方，详见 sla-stack 内） -->
    <TaskFormPanel
      v-if="editingId === 0"
      :form="form" :target-hint="targetHint" :nodes="briefNodes"
      :saving="saving" :msg="formMsg" :err="formErr"
      @submit="save" @cancel="cancelEdit"
    />

    <!-- 每任务一张 SLA 卡 -->
    <div v-if="!loading" class="sla-stack">
      <template v-for="tk in taskCards" :key="tk.task.id">
      <section class="panel sla-card">
        <div class="sla-head">
          <div class="sla-title">
            <input type="checkbox" class="sel-box" :checked="!!selMap[tk.task.id]" @change="toggleSel(tk.task.id)"
              :title="selMap[tk.task.id] ? '取消选择' : '选择（用于批量分享）'" />
            <span class="ak-tag ch type-tag">{{ apiLabel(tk.task.apiType) }}</span>
            <span class="mono name">{{ tk.task.name }}</span>
            <span class="dim tgt">{{ tk.task.target }}</span>
            <span class="dim gap">{{ every(tk.task) }} · 慢 &gt; {{ tk.task.slowMs || 0 }} ms</span>
          </div>
          <div class="sla-ops">
            <button class="ak-button ak-button--outline sm" :class="{ off: !tk.task.enabled }"
              :title="tk.task.enabled ? '当前运行中，点击停用' : '当前已停，点击启用'"
              @click="toggle(tk.task)">{{ tk.task.enabled ? '运行中' : '已停' }}</button>
            <button class="ak-button ak-button--outline sm" :class="{ off: trendCollapsed[tk.task.id] }" @click="toggleTrend(tk.task.id)">{{ trendCollapsed[tk.task.id] ? '展开曲线' : '延迟曲线' }}</button>
            <button class="ak-button ak-button--outline sm" @click="toggleCompare(tk.task)">{{ compareMap[tk.task.id] ? '关闭对比' : '节点对比' }}</button>
            <button class="ak-button ak-button--outline sm" :disabled="exportingId === tk.task.id"
              title="导出当前统计窗口的完整曲线（含每轮成功率与平均延迟）"
              @click="exportSeries(tk.task)">{{ exportingId === tk.task.id ? '导出中…' : '导出 CSV' }}</button>
            <button class="ak-button ak-button--outline sm" @click="shareSingle(tk.task)">{{ shareMap[tk.task.id] ? '重生成链接' : '分享' }}</button>
            <button v-if="shareMap[tk.task.id]" class="ak-button ak-button--outline sm danger" @click="closeBatchShare(tk.task)">关闭分享</button>
            <button class="ak-button ak-button--outline sm" @click="openEdit(tk.task)">编辑</button>
            <button class="ak-button ak-button--outline sm danger" @click="remove(tk.task)">✕</button>
          </div>
        </div>

        <!-- 判定摘要 + 全局 -->
        <div class="sla-sub dim">
          <span>判定：{{ judgeText(tk.task) }}</span>
          <span>节点 {{ nodeScopeText(tk.task) }}</span>
          <span v-if="tk.task.notifyRecover">恢复通知 ✓<span v-if="tk.task.quietHours">（免打扰 {{ tk.task.quietHours }}）</span></span>
          <span v-if="tk.task.tags"><span class="ak-tag ch" v-for="tg in tk.task.tags.split(',')" :key="tg">{{ tg }}</span></span>
          <span>最近更新 {{ fmtTime(tk.task.updatedAt) }}</span>
          <span v-if="tk.task.ownerUsername" class="owner" :title="tk.task.ownerId ? '告警将发往该所有者' : ''">创建者 @{{ tk.task.ownerUsername }}</span>
        </div>
        <div v-if="shareMap[tk.task.id]" class="sla-sub share-line">
          <span class="ok-200 mono" style="word-break:break-all">公开链接：{{ shareOrigin }}{{ shareMap[tk.task.id] }} <button class="link-btn" @click="copyShare(tk.task)">复制</button></span>
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
            <span>延迟趋势 <span class="dim">/ 每个点 = 一轮多节点平均延迟 · 标红 = 该轮有节点失败 · 滚轮或底部滑块缩放</span></span>
            <span class="dim" v-if="hasFailure(seriesMap[tk.task.id])">存在失败轮次</span>
          </div>
          <EChart v-if="hasTrend(seriesMap[tk.task.id]) && !trendCollapsed[tk.task.id]" :option="tk.chart" height="150px" />
          <div v-else-if="trendCollapsed[tk.task.id]" class="dim empty">曲线已收起</div>
          <div v-else class="dim empty">该窗口暂无定时样本</div>
        </div>

        <!-- 节点对比（C2）：每条线 = 一个节点的每轮实测延迟，叠加同图 -->
        <div v-if="compareMap[tk.task.id]" class="sla-trend">
          <div class="sla-trend-cap">
            <span>节点对比 <span class="dim">/ 每条线 = 一个节点的每轮延迟</span></span>
            <span class="dim" v-if="compareMap[tk.task.id].loading">加载中…</span>
          </div>
          <EChart v-if="!compareMap[tk.task.id].loading && compareMap[tk.task.id].nodes.length"
            :option="nodeCompareOption(tk.task.id)" height="220px" />
          <div v-else-if="!compareMap[tk.task.id].loading" class="dim empty">无节点曲线数据</div>
        </div>

        <!-- 每节点 -->
        <div v-if="tk.sla && tk.sla.byNode && tk.sla.byNode.length" class="ak-table-wrap">
          <table class="ak-table sla-node">
            <thead><tr>
              <th>节点</th><th>在线</th><th>可用率</th><th>成功/失败</th><th>平均延迟</th><th>最大/P95</th><th>慢请求</th><th>最新延迟</th>
              <th v-if="hasSpecialCol(tk.task.apiType)">{{ specialColName(tk.task.apiType) }}</th>
            </tr></thead>
            <tbody>
              <tr v-for="n in tk.sla.byNode" :key="n.nodeId">
                <td class="mono nowrap">{{ n.nodeId }}</td>
                <td><span class="dot" :class="n.nodeOnline === false ? 'offline' : 'online'"></span></td>
                <td class="mono" :class="upTone(n.availability)">{{ fmtPct(n.availability) }}</td>
                <td class="mono"><span class="ok-200">{{ n.up }}</span>/<span class="err">{{ n.down }}</span><span v-if="n.invalid" class="dim" :title="'无法判定的样本数'"> · {{ n.invalid }} 不明</span></td>
                <td class="mono">{{ n.avgMs }}ms</td>
                <td class="mono dim">{{ n.maxMs }}/{{ n.p95Ms }}</td>
                <td class="mono" :class="n.slow ? 'err' : 'ok-200'">{{ n.slow }}</td>
                <td class="mono">
                  <span class="dot" :class="n.latestUp ? 'online' : 'offline'"></span>{{ n.latestMs }}ms
                </td>
                <!-- 取自最新一条样本（时间见后缀），悬浮看完整快照；与左侧窗口聚合口径不同 -->
                <td v-if="hasSpecialCol(tk.task.apiType)" class="special" :class="specialTone(tk.task.apiType, n.special)"
                  :title="specialFull(tk.task.apiType, n.special)">
                  <template v-if="n.special">
                    {{ specialText(tk.task.apiType, n.special) }}
                    <span class="dim">· {{ timeAgo(n.latestAt) }}</span>
                  </template>
                  <template v-else>—</template>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <div v-else class="dim empty">该窗口暂无定时样本（已采集 {{ tk.sla?.samples || 0 }} 条）。请确认任务已启用、采集间隔合理且节点可达。</div>
      </section>
      <!-- 编辑当前任务时把表单插入到该任务卡下方，而不是跳到列表顶部 -->
      <TaskFormPanel
        v-if="editingId === tk.task.id"
        :form="form" :target-hint="targetHint" :nodes="briefNodes"
        :saving="saving" :msg="formMsg" :err="formErr"
        @submit="save" @cancel="cancelEdit"
      />
    </template>
    </div>

    <!-- 批量分享对话框（多选任务共用一个分享码；支持自定义分享码） -->
    <div v-if="batchShare.show" class="mask" @click.self="batchShare.show = false">
      <div class="dlg">
        <div class="dlg-title">分享所选任务（{{ selCount }} 个，共用一条链接）</div>
        <div class="ak-form-stack">
          <label class="ak-field">
            <span class="ak-label">自定义分享码</span>
            <input class="ak-input" v-model.trim="batchShare.token"
              placeholder="留空随机生成；3~32 位小写字母/数字/连字符" />
          </label>
          <p class="dim" style="margin:4px 0 0;font-size:.74rem">
            链接：&lt;站点地址&gt;/s/&lt;分享码&gt;；同一分享码的任务在公开状态页同页展示；
            重生成会使旧链接立即失效。
          </p>
        </div>
        <div v-if="batchShare.msg" :class="batchShare.err ? 'err' : 'ok-200'" style="margin-top:10px;font-size:.8rem;word-break:break-all">
          {{ batchShare.msg }}
          <template v-if="batchShare.link">
            <br />{{ batchShare.link }}
            <button class="ak-button ak-button--outline" style="padding:2px 8px;font-size:.72rem;margin-left:6px"
              @click="copyText(batchShare.link)">复制</button>
          </template>
        </div>
        <div class="dlg-foot">
          <button class="ak-button ak-button--outline" @click="batchShare.show = false">关闭</button>
          <button v-if="batchShare.link" class="ak-button ak-button--outline danger" @click="closeBatchShare()">关闭分享</button>
          <button class="ak-button ak-button--action" :disabled="batchShare.busy" @click="confirmBatchShare">
            {{ batchShare.busy ? '生成中…' : (batchShare.link ? '重新生成' : '生成分享链接') }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, reactive, computed, onMounted, onBeforeUnmount, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  fetchTasks, fetchTaskSla, createTask, updateTask, setTaskEnabled, deleteTask, fetchTaskMeta, fetchTaskSeries,
  fetchNodesBrief, shareTasks, unshareTasks, exportTaskSeries, fetchTimeRange,
} from '../api/boce.js'
import { getToken } from '../api/http.js'
import { WS_BASE } from '../config.js'
import { fmtTime, timeAgo } from '../utils/format.js'
import { useDialog } from '../composables/useDialog.js'
import { useTheme, readChartTheme } from '../composables/useTheme.js'
import EChart from '../components/EChart.vue'
import TaskFormPanel from '../components/TaskFormPanel.vue'
import TimeRangePicker from '../components/TimeRangePicker.vue'
import { apiOptions, apiLabel, dnsKindLabel } from '../utils/probeMeta.js'
import { fromQuery, rangeQuery, resolveRange, toQuery } from '../utils/timeRange.js'

const dialog = useDialog()
const route = useRoute()
const router = useRouter()
// 统计窗口：{ key, start?, end? } —— 相对窗口 / 自然周期 / 自定义区间，见 utils/timeRange.js。
// 初值取自地址栏，于是窗口可分享、可回退、刷新不丢。
const range = ref(fromQuery(route.query))
const rangeMaxDays = ref(0) // 后端可查上限（天），用于隐藏必然查空的预设项
// 当前窗口的查询串（相对窗口 → hours=N；绝对区间 → start/end），所有取数都带上它
const winQS = computed(() => rangeQuery(range.value))
// 拨测方案候选：fetchTaskMeta 返回的 types 可覆盖（追加业务后端新增的探针类）
const types = ref(apiOptions.map((o) => o.value))
const tasks = ref([])
const slaMap = ref({}) // taskId -> sla resp
const seriesMap = ref({}) // taskId -> 分桶时序（延迟曲线）
const tagFilter = ref('') // 标签过滤（?tag=，服务端子串匹配）
// B3 公开状态页分享：分享码即分享组（多选任务可共用同一分享码）——taskId -> 分享路径（/s/<token>）。
// 页面由前端 Vue 路由渲染（/s/:token），链接拼在访问者所在的**前端** origin 下。
const shareMap = ref({})
const shareOrigin = window.location.origin
// 多选分享：taskId -> bool + 批量分享对话框状态
const selMap = ref({})
const selCount = computed(() => Object.values(selMap.value).filter(Boolean).length)
const batchShare = reactive({ show: false, token: '', busy: false, msg: '', err: false, link: '' })
// 一键全选（针对当前列表的全部任务）
const allSelected = computed(() => tasks.value.length > 0 && tasks.value.every((t) => selMap.value[t.id]))
function toggleSelectAll() {
  const target = !allSelected.value
  const m = { ...selMap.value }
  tasks.value.forEach((t) => { m[t.id] = target })
  selMap.value = m
}
// 曲线收起/展开（按任务）
const trendCollapsed = ref({})
function toggleTrend(id) {
  trendCollapsed.value = { ...trendCollapsed.value, [id]: !trendCollapsed.value[id] }
}
// C2 节点对比：taskId -> { loading, nodes:[{nodeId, series}] }
const compareMap = ref({})
// 正在导出的任务 id（0 = 空闲），仅用于按钮 loading 态
const exportingId = ref(0)
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
    id: null, name: '', apiType: 'detail', target: '', stack: '', recordType: 'a', nodeScope: 'all', nodeIds: '',
    intervalSec: 60, slowMs: 0, expectStatus: '2xx',
    bothProtocols: true, requireAllStacks: true, certExpiredDown: true,
    notifyRecover: false, quietHours: '', tags: '', hideTarget: false,
  }
}

// ---- SLA 实时推送（WS /console/sla）----
// 后端在每轮定时拨测写入样本后，把该任务整窗聚合 SLA 推过来，此处覆盖对应卡片即实时刷新。
// 断线指数退避自动重连；重连成功(非首次)补拉一次全量，补齐断线窗口错过的推送。
let ws = null
let wsTimer = null
let wsEverOpened = false
let wsUserClosed = false
let wsRetry = 0
let lastLoadAt = 0 // 最近一次取数时刻（WS 重连补拉据此去重，见 ws.onopen）

// 切换窗口（预设 / 平移 / 自定义区间都走这里）：写回地址栏 → 重新取数 → 按新窗口重订实时推送。
// 不锁按钮 —— 大窗口慢（7 天是上万点），让人随时能切回去，而不是被禁住十几秒。
let loadSeq = 0
function onRangeChange(next) {
  range.value = next
  syncUrl()
  load(true)
  connectSla()
}

// 窗口写回地址栏（保留页面上的其它参数，如标签过滤）
function syncUrl() {
  const q = { ...route.query, ...toQuery(range.value) }
  if (range.value.key !== 'custom') {
    delete q.start
    delete q.end
  }
  router.replace({ query: q })
}

function connectSla() {
  if (wsUserClosed) return
  if (ws) { try { ws.onclose = null; ws.onmessage = null; ws.close() } catch {} }
  ws = null
  // 实时推送只能按“最近 N 小时”订阅：自然周期（今天/本周/本月）与自定义区间都是绝对区间，
  // 推来的快照对不上，索性不订 —— 这类窗口是回顾性查看，不需要秒级刷新。
  const r = resolveRange(range.value)
  if (r.kind !== 'rel') return
  const token = getToken()
  const url =
    `${WS_BASE}/console/sla?hours=${Math.round(r.hours)}` +
    (token ? `&token=${encodeURIComponent(token)}` : '')
  try {
    ws = new WebSocket(url)
  } catch {
    scheduleReconnect()
    return
  }
  ws.onopen = () => {
    // 重连成功补拉全量，补齐断线期间错过的推送；走 silent —— 卡片已在屏幕上，
    // 不该整页闪成加载态（切窗口会重建 WS，用非 silent 会让卡片整块消失十几秒）。
    // 刚加载过（同一窗口已由 onRangeChange 拉过全量）则跳过，避免重复请求两遍。
    if (wsEverOpened && Date.now() - lastLoadAt > 5000) load(true)
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
// 判断后端推送窗口是否就是当前窗口（后端 window.from/to 为 UTC）。
// 绝对区间一律不接收实时快照 —— 推送的永远是“最近 N 小时”，与历史区间不是一回事。
function matchWindow(win) {
  if (!win?.from || !win?.to) return false
  const r = resolveRange(range.value)
  if (r.kind !== 'rel') return false
  const spanH = (new Date(win.to) - new Date(win.from)) / 3600000
  return Math.abs(spanH - r.hours) < 1
}

onMounted(() => {
  load()
  loadBrief()
  connectSla()
  // 可查范围（保留期 / 接口上限）：选择器据此隐藏必然查空的预设
  fetchTimeRange()
    .then((r) => { rangeMaxDays.value = r?.maxDays || 0 })
    .catch(() => {})
})
onBeforeUnmount(closeSla)

// briefNodes 节点简表（脱敏）：任务表单"指定节点"勾选数据源（admin/user 都可用）
const briefNodes = ref([])
async function loadBrief() {
  try {
    briefNodes.value = await fetchNodesBrief()
  } catch {
    briefNodes.value = []
  }
}

// silent=true：后台刷新（保存/启停/切窗口后），不显示全屏加载态——已有卡片原地更新，
// 避免"每操作一次就整页重载"的体验。
// 代次在函数内自增：每次取数都算一个新代次，任何更早发出的取数（含 WS 重连补拉）回来时
// 都会被丢弃 —— 否则一次慢的补拉会在用户切走窗口之后把旧窗口数据盖回界面。
async function load(silent) {
  const my = ++loadSeq
  lastLoadAt = Date.now()
  if (!silent) loading.value = true
  error.value = ''
  const stale = () => my !== loadSeq
  try {
    const meta = await fetchTaskMeta().catch(() => null)
    if (meta?.types) types.value = meta.types
    const list = await fetchTasks(tagFilter.value || undefined)
    tasks.value = list || []
    if (stale()) return
    // 并发拉取每个任务的 SLA 与时序曲线；两类数据谁先到先上屏 —— SLA 很轻、曲线很重
    // （7 天是上万点），切大窗口时 KPI 与节点表格先刷新，不必陪着曲线一起等。
    const qs = winQS.value
    await Promise.all(
      tasks.value.map((t) => {
        const sla = fetchTaskSla(t.id, qs).catch(() => null)
        const series = fetchTaskSeries(t.id, qs).catch(() => null)
        return Promise.all([
          sla.then((v) => { if (!stale()) slaMap.value = { ...slaMap.value, [t.id]: v } }),
          series.then((v) => { if (!stale()) seriesMap.value = { ...seriesMap.value, [t.id]: v } }),
        ])
      }),
    )
  } catch (e) {
    if (!stale()) error.value = e?.message || '加载失败'
  } finally {
    if (!stale()) loading.value = false
  }
}

// 拉取单个任务时序（WS 推送后刷新该卡片的失败段红标）
async function refreshTaskSeries(taskId) {
  try {
    const series = await fetchTaskSeries(taskId, winQS.value)
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
    // slow 与 up 是独立判定（down 的样本也可能超过慢阈值），故 up-slow 可能为负 → 兜底夹到 0，
    // 否则慢样本多于成功样本时会显示出负的达标率。
    metRate: samples ? Math.max(0, Math.round((met / samples) * 10000) / 100) : 0,
    avgMs: nLat ? Math.round(sumMs / nLat) : 0,
    invalid,
  }
}

const targetHint = computed(() => {
  switch (form.value.apiType) {
    case 'ssl': return '域名，如 www.example.com'
    case 'detail': return '域名，如 www.example.com'
    case 'tcping': return 'host[:port]，如 1.1.1.1:443'
    case 'speed': return '测速文件 URL，如 https://host/file（协议栈选 v4/v6）'
    case 'dns': return '域名，如 example.com（记录类型另选）'
    default: return ''
  }
})

function every(t) {
  return `每 ${t.intervalSec} 秒`
}
function judgeText(t) {
  // dns 的"判定口径"就是记录类型（解析出记录即成功，无状态码/双栈概念）
  if (t.apiType === 'dns') return dnsKindLabel(t.recordType || 'a')
  const p = [t.expectStatus || '2xx']
  if (t.apiType === 'detail') p.push(t.bothProtocols ? 'HTTP 与 HTTPS 均需命中' : '任一协议命中')
  if (t.apiType === 'ssl' || t.apiType === 'detail') p.push(t.requireAllStacks ? 'IPv4 与 IPv6 均可用' : '任一协议可用')
  if (t.apiType === 'ssl') p.push(t.certExpiredDown ? '证书过期判为失败' : '证书过期仅提示')
  return p.join(' · ')
}
function nodeScopeText(t) {
  return t.nodeScope === 'custom' ? `仅 ${t.nodeIds}` : '全部节点'
}
// ===== 特殊字段列（取自最新一条样本，非窗口聚合）=====
// ssl → "证书"（剩余天数着色：>30 正常 / ≤30 临期 / ≤0 过期）；detail → "速度/体积"；其余类型无此列
const hasSpecialCol = (t) => t === 'ssl' || t === 'detail' || t === 'dns'
const specialColName = (t) => (t === 'ssl' ? '证书' : t === 'dns' ? '解析结果' : '速度/体积')

function specialTone(apiType, sp) {
  if (apiType === 'ssl' && sp?.cert_validity_days != null) {
    const d = Number(sp.cert_validity_days)
    if (d <= 0) return 'bad'
    if (d <= 30) return 'warn'
  }
  return ''
}

function specialText(apiType, sp) {
  if (!sp) return '—'
  const parts = []
  if (apiType === 'ssl') {
    if (sp.domain) parts.push(sp.domain)
    if (sp.cert_validity_days != null) {
      const d = Number(sp.cert_validity_days)
      parts.push(d <= 0 ? '证书已过期' : `证书剩 ${d} 天`)
    }
    if (sp.cert_end_time) parts.push(`至 ${String(sp.cert_end_time).slice(0, 10)}`)
  } else if (apiType === 'detail') {
    if (sp.download_speed != null) parts.push(`↓ ${Number(sp.download_speed).toFixed(2)} MB/s`)
    if (sp.page_size != null) parts.push(`${fmtNum(sp.page_size)}B`)
  } else if (apiType === 'dns') {
    if (sp.record_count != null) parts.push(`${sp.record_count} 条记录`)
    if (sp.first_record) parts.push(String(sp.first_record))
    if (sp.duration != null) parts.push(`${sp.duration}ms`)
  }
  return parts.filter(Boolean).join(' · ') || '—'
}

// specialFull 悬浮完整快照（后端 extractSpecial 已回传全部字段，这里仅格式化）
function specialFull(apiType, sp) {
  if (!sp) return ''
  const parts = []
  if (apiType === 'ssl') {
    if (sp.subject_common_name) parts.push(`CN ${sp.subject_common_name}`)
    if (sp.issuer_common_name) parts.push(`颁发者 ${sp.issuer_common_name}`)
    if (sp.issuer_organization) parts.push(`机构 ${sp.issuer_organization}`)
    if (sp.http_version) parts.push(`HTTP ${sp.http_version}`)
    if (sp.cert_start_time) parts.push(`生效 ${String(sp.cert_start_time).slice(0, 10)}`)
    if (sp.cert_end_time) parts.push(`到期 ${String(sp.cert_end_time).slice(0, 10)}`)
    if (sp.cert_validity_days != null) parts.push(`剩余 ${sp.cert_validity_days} 天`)
  } else if (apiType === 'dns') {
    if (sp.domain) parts.push(`域名 ${sp.domain}`)
    if (sp.record_count != null) parts.push(`记录 ${sp.record_count} 条`)
    if (sp.first_record) parts.push(`首条 ${sp.first_record}`)
    if (sp.ttl != null) parts.push(`TTL ${sp.ttl}`)
    if (sp.duration != null) parts.push(`耗时 ${sp.duration}ms`)
  } else {
    if (sp.host_record) parts.push(`解析 ${sp.host_record}`)
    if (sp.dns_lookup_time != null) parts.push(`DNS ${sp.dns_lookup_time}ms`)
    if (sp.tcp_connect_time != null) parts.push(`TCP ${sp.tcp_connect_time}ms`)
    if (sp.http_connect_time != null) parts.push(`HTTP 连接 ${sp.http_connect_time}ms`)
    if (sp.first_byte_time != null) parts.push(`首字节 ${sp.first_byte_time}ms`)
    if (sp.total_time != null) parts.push(`总计 ${sp.total_time}ms`)
    if (sp.page_size != null) parts.push(`大小 ${fmtNum(sp.page_size)}B`)
    if (sp.download_speed != null) parts.push(`速度 ${Number(sp.download_speed).toFixed(2)} MB/s`)
    if (sp.http_status_code != null) parts.push(`HTTP ${sp.http_status_code}`)
    if (sp.https_status_code != null) parts.push(`HTTPS ${sp.https_status_code}`)
  }
  return parts.filter(Boolean).join(' · ')
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

// ===== SLA 延迟曲线（复用 theme.css --chart-* 语义色，与大盘一致）=====
const { theme } = useTheme()
// 图表配色全部来自 readChartTheme()（信号色 + 网格/轴/label/提示框）：
// echarts 画在 canvas 上吃不到 CSS 变量，且亮色卡片是深灰底，
// 信号色在两种主题下取值不同，所以读一次存 reactive、切主题时整体重读。
const CH = reactive({ ...readChartTheme() })
const chartAxis = {
  axisLine: { lineStyle: { color: CH.axis } },
  axisLabel: { color: CH.label },
  splitLine: { lineStyle: { color: CH.grid } },
}
// 提示框底色/边框/文字同为中性色：亮色下白底深字，暗色下深底浅字
const chartTooltip = reactive({
  backgroundColor: CH.tipBg, borderColor: CH.tipBorder, textStyle: { color: CH.tipText },
})
watch(theme, () => {
  Object.assign(CH, readChartTheme())
  chartTooltip.backgroundColor = CH.tipBg
  chartTooltip.borderColor = CH.tipBorder
  chartTooltip.textStyle.color = CH.tipText
})
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

// ---- B3 公开状态页分享（分享码即分享组：多选批量 / 单个） ----
function toggleSel(id) {
  selMap.value = { ...selMap.value, [id]: !selMap.value[id] }
}
// 单卡分享：只选该任务并打开批量分享对话框（一个任务=一个分享码的最小情形）
function shareSingle(t) {
  selMap.value = { [t.id]: true }
  openBatchShare()
}
function openBatchShare() {
  if (!selCount.value) return
  batchShare.show = true
  batchShare.token = ''
  batchShare.msg = ''
  batchShare.err = false
  batchShare.link = ''
}
function selectedIds() {
  return Object.keys(selMap.value).filter((k) => selMap.value[k]).map(Number)
}
async function confirmBatchShare() {
  const ids = selectedIds()
  if (!ids.length) return
  batchShare.busy = true
  batchShare.msg = ''
  batchShare.err = false
  try {
    const r = await shareTasks(ids, batchShare.token)
    for (const id of ids) shareMap.value = { ...shareMap.value, [id]: r.url }
    batchShare.link = shareOrigin + r.url
    batchShare.msg = '已生成分享链接'
  } catch (e) {
    batchShare.err = true
    batchShare.msg = e?.message || '生成失败'
  } finally {
    batchShare.busy = false
  }
}
async function closeBatchShare(t) {
  const ids = t ? [t.id] : selectedIds()
  if (!ids.length) return
  batchShare.busy = true
  try {
    await unshareTasks(ids)
    const m = { ...shareMap.value }
    for (const id of ids) delete m[id]
    shareMap.value = m
    batchShare.show = false
  } catch { /* 后端已给错误提示 */ } finally { batchShare.busy = false }
}
// 复制该任务的公开链接（原模板绑定了 handler 但脚本里未定义，点击会抛错）
function copyShare(t) {
  const path = shareMap.value[t.id]
  if (!path) return
  copyText(shareOrigin + path)
}
async function copyText(txt) {
  try { await navigator.clipboard.writeText(txt) } catch { /* 剪贴板不可用时用户可手动选中 */ }
}

// ---- 曲线导出 CSV（当前统计窗口，服务端按同参数生成）----
async function exportSeries(t) {
  exportingId.value = t.id
  error.value = ''
  try {
    await exportTaskSeries(t.id, winQS.value)
  } catch (e) {
    error.value = e?.message || '导出失败'
  } finally {
    exportingId.value = 0
  }
}

// ---- C2 节点对比 ----
async function toggleCompare(t) {
  if (compareMap.value[t.id]) {
    const m = { ...compareMap.value }
    delete m[t.id]
    compareMap.value = m
    return
  }
  compareMap.value = { ...compareMap.value, [t.id]: { loading: true, nodes: [] } }
  const card = taskCards.value.find((c) => c.task.id === t.id)
  const nodeList = (card?.sla?.byNode || []).slice(0, 8).map((n) => n.nodeId)
  const nodes = (await Promise.all(
    nodeList.map((n) => fetchTaskSeries(t.id, winQS.value, n).catch(() => null)),
  ))
    .map((s, i) => (s ? { nodeId: nodeList[i], series: s.series || [] } : null))
    .filter(Boolean)
  compareMap.value = { ...compareMap.value, [t.id]: { loading: false, nodes } }
}
// 节点对比图 option：每节点一条 avgMs 折线（同窗口同分桶，线上断点=该节点当轮无有效延迟）
function nodeCompareOption(taskId) {
  const nodes = compareMap.value[taskId]?.nodes || []
  // 前三位跟 --chart-cyan/yellow/green（提亮饱和）同值，后五位沿用最初的原版装饰色
  const colors = [CH.cyan, CH.yellow, CH.green, '#c678dd', '#ff9f43', '#4ec9b0', '#e06c75', '#569cd6']
  return {
    color: colors,
    legend: { top: 0, textStyle: { color: CH.label }, itemWidth: 14 },
    tooltip: { trigger: 'axis', axisPointer: { type: 'line' }, ...chartTooltip,
      valueFormatter: (v) => (typeof v === 'number' ? v + ' ms' : '无') },
    grid: { left: 56, right: 16, top: 30, bottom: 40 },
    // x 轴必须用 time（同主趋势图）：value 轴会把毫秒时间戳当普通数字做整数好刻度，
    // 刻度从 0 开始散布在 1970~1973，格式化后就是"01-05/07-10 交错"的怪日期
    xAxis: { type: 'time', ...chartAxis, axisLabel: { ...chartAxis.axisLabel, formatter: (v) => bucketLabel(v), hideOverlap: true } },
    yAxis: { type: 'value', ...chartAxis, axisLabel: { ...chartAxis.axisLabel, formatter: '{value} ms' } },
    series: nodes.map((n) => ({
      name: n.nodeId, type: 'line', showSymbol: false,
      data: (n.series || [])
        .map((s) => {
          const t = bucketMs(s.time)
          const v = typeof s.avgMs === 'number' && s.avgMs > 0 ? s.avgMs : null
          return t == null || v == null ? null : [t, v]
        })
        .filter(Boolean),
    })),
  }
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
  // 失败段：把连续 down>0 的桶聚成区间。孤立的单轮失败在 24h 视图里只有 ~0.1 像素宽
  // （一个采样间隔 / 整个时间轴），因此：① 每段向外扩半个典型采样间隔；② 另加一组
  // 固定顶部条带的红色菱形标记（下方 failDots），任何缩放级别都肉眼可见。
  let step = 0
  const diffs = []
  for (let i = 1; i < xv.length; i++) {
    const d = xv[i] - xv[i - 1]
    if (d > 0) diffs.push(d)
  }
  if (diffs.length) {
    diffs.sort((a, b) => a - b)
    step = diffs[Math.floor(diffs.length / 2)]
  }
  const half = Math.max(step / 2, 500) // 典型采样间隔的一半（至少 0.5s）
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
      areas.push([{ name: '失败', xAxis: segStart - half }, { xAxis: xv[i] + half }])
      segStart = null
      lastFailT = null
    }
  }
  if (segStart !== null) {
    // 段延伸到末尾：右边界取末桶起点（+half 仍有宽度 > 0）
    areas.push([{ name: '失败', xAxis: segStart - half }, { xAxis: lastFailT + half }])
  }
  // 失败轮次标记点：画在隐藏副轴的固定高度（90%），与延迟值无关，任何缩放都可见
  const failDots = rows
    .map((r, i) => (r.down > 0 && xv[i] != null ? [xv[i], 0.9] : null))
    .filter(Boolean)
  return {
    color: [CH.cyan],
    tooltip: {
      trigger: 'axis',
      axisPointer: { type: 'line' },
      ...chartTooltip,
      formatter(params) {
        const arr = Array.isArray(params) ? params : [params]
        const lineP = arr.find((p) => p.seriesName === '平均延迟') || arr[0]
        const failP = arr.find((p) => p.seriesName === '失败轮次')
        const x = lineP?.value
        const ms = Array.isArray(x) ? x[0] : x
        if (!ms) return failP ? `<div style="color:${CH.red}">失败轮次</div>` : ''
        let di = -1
        for (let i = 0; i < xv.length; i++) if (xv[i] === ms) { di = i; break }
        const r = di >= 0 ? rows[di] : null
        const head = `<div style="font-weight:600;margin-bottom:4px">${bucketLabel(ms)}</div>`
        const val = Array.isArray(x) && typeof x[1] === 'number' ? `${x[1]} ms` : '无'
        const line = `<div>${lineP?.marker || ''}平均延迟：${val}</div>`
        let fail = ''
        if (r && r.down > 0) fail += `<div style="color:${CH.red};margin-top:2px">失败 ${r.down}/${r.samples} · 可用率 ${r.availability}%</div>`
        else if (failP) fail += `<div style="color:${CH.red};margin-top:2px">该轮存在失败</div>`
        return head + line + fail
      },
    },
    grid: { left: 46, right: 16, top: 18, bottom: 34 },
    // 缩放看细节：滚轮/双指缩放(inside) + 底部缩放条(slider,可拖可平移)；时间型 x 轴，缩放不改变曲线本身
    dataZoom: [
      { type: 'inside', zoomOnMouseWheel: true, moveOnMouseMove: true, moveOnMouseWheel: false, filterMode: 'none' },
      {
        type: 'slider', height: 14, bottom: 6, filterMode: 'none',
        borderColor: 'transparent', backgroundColor: CH.tint,
        fillerColor: 'rgba(42,157,244,.16)', dataBackgroundColor: CH.tint,
        textStyle: { color: CH.label, fontSize: 10 },
        handleStyle: { color: CH.cyan, borderColor: CH.cyan },
        moveHandleStyle: { color: CH.tintStrong },
      },
    ],
    xAxis: { type: 'time', ...chartAxis, axisLabel: { ...chartAxis.axisLabel, formatter: (ms) => bucketLabel(ms), hideOverlap: true } },
    yAxis: [
      { type: 'value', name: 'ms', ...chartAxis },
      // 隐藏副轴：失败轮次标记固定画在 90% 高度条带上，与延迟值解耦，任何缩放都可见
      { type: 'value', min: 0, max: 1, show: false },
    ],
    series: [
      {
        name: '平均延迟', type: 'line', smooth: true, showSymbol: false, connectNulls: false,
        lineStyle: { width: 2 },
        areaStyle: { opacity: 0.1 },
        data: avg,
        markArea: {
          silent: true,
          itemStyle: { color: 'rgba(227,59,59,0.22)' },
          data: areas,
        },
      },
      {
        // 失败轮次标记（B2/C 修复）：红菱形固定在图表上部条带，缩放/全览均可见
        name: '失败轮次', type: 'scatter', yAxisIndex: 1, symbol: 'diamond', symbolSize: 7,
        itemStyle: { color: CH.red }, z: 5,
        data: failDots,
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
    recordType: t.recordType || 'a', nodeScope: t.nodeScope || 'all', nodeIds: t.nodeIds || '',
    intervalSec: t.intervalSec, slowMs: t.slowMs || 0, expectStatus: t.expectStatus || '2xx',
    bothProtocols: t.bothProtocols, requireAllStacks: t.requireAllStacks, certExpiredDown: t.certExpiredDown,
    notifyRecover: !!t.notifyRecover, quietHours: t.quietHours || '', tags: t.tags || '', hideTarget: !!t.hideTarget,
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
      stack: form.value.stack, recordType: form.value.apiType === 'dns' ? (form.value.recordType || 'a') : '',
      nodeScope: form.value.nodeScope, nodeIds: form.value.nodeIds,
      intervalSec: form.value.intervalSec, slowMs: form.value.slowMs || 0,
      expectStatus: form.value.expectStatus || '2xx',
      bothProtocols: form.value.bothProtocols, requireAllStacks: form.value.requireAllStacks,
      certExpiredDown: form.value.certExpiredDown,
      notifyRecover: form.value.notifyRecover, quietHours: form.value.quietHours || '',
      tags: form.value.tags || '', hideTarget: form.value.hideTarget,
    }
    if (form.value.id) await updateTask(form.value.id, payload)
    else await createTask(payload)
    formMsg.value = '已保存'
    cancelEdit()
    await load(true) // 静默刷新：不闪全屏加载，卡片原地更新
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
    await load(true)
  } catch (e) { error.value = e?.message || '操作失败' }
}
async function remove(t) {
  const ok = await dialog.confirm({
    title: `删除任务「${t.name}」？`,
    message: '任务及其全部历史拨测数据将被删除，操作不可撤销。',
    kind: 'danger',
    confirmText: '删除',
    cancelText: '取消',
  })
  if (!ok) return
  try {
    await deleteTask(t.id)
    await load(true)
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
/* 窄屏：卡片头改上下堆叠、按钮组允许换行 —— 7 个按钮 + 标题一行必然撑破视口（实测 320 下溢出到 371px） */
@media (max-width: 767px) {
  .sla-head { flex-direction: column; align-items: stretch; gap: 8px; }
  .sla-ops { flex-wrap: wrap; }
}
.sla-sub { display: flex; flex-wrap: wrap; gap: 4px 14px; margin-top: 8px; font-size: .72rem; }
.sla-sub .owner { color: var(--ak-signal-accent); font-family: var(--ak-font-mono); }
/* 分享链接行（模板里的 .link-btn 若无样式会渲染成浏览器原生按钮） */
.sla-sub.share-line { margin-top: 6px; }
.link-btn {
  background: none; border: none; color: var(--ak-signal-info);
  cursor: pointer; font-size: 0.78rem; padding: 2px 6px;
}
/* 多选勾选框与批量分享对话框 */
.sel-box { margin-right: 2px; cursor: pointer; }
.mask {
  position: fixed; inset: 0; background: rgba(0, 0, 0, 0.45);
  display: flex; align-items: flex-start; justify-content: center;
  padding-top: 10vh; z-index: 50;
}
.dlg {
  width: min(460px, calc(100vw - 40px)); max-height: 84vh; overflow: auto;
  background: var(--ak-surface-raised);
  border: var(--ak-line-hairline) solid rgba(0, 0, 0, 0.12);
  border-top: 3px solid var(--ak-signal-info);
  padding: 20px 22px; box-shadow: 0 20px 50px rgba(0, 0, 0, 0.35);
}
.dlg-title { font-family: var(--ak-font-command); font-weight: 600; margin-bottom: 14px; }
.dlg-foot { display: flex; justify-content: flex-end; gap: 10px; margin-top: 18px; }
.dlg-foot .danger { color: var(--ak-signal-danger); border-color: var(--ak-signal-danger); }
.sla-kpis { display: grid; grid-template-columns: repeat(auto-fit, minmax(120px, 1fr)); gap: 10px; margin: 14px 0; }
.sla-kpis .kpi { background: var(--ui-tint); border: 1px solid var(--ui-line-soft); padding: 10px 12px; }
.sla-kpis .kpi .l { font-size: .7rem; color: var(--ak-text-secondary); }
.sla-kpis .kpi .v { font-family: var(--ak-font-mono); font-size: 1.25rem; font-weight: 600; margin-top: 2px; }
.sla-kpis .kpi.ok .v { color: var(--ak-signal-success); }
.sla-kpis .kpi.warn .v { color: var(--ak-signal-action); }
.sla-kpis .kpi.bad .v { color: var(--ak-signal-danger); }
.sla-node .special { color: var(--ak-text-secondary); font-size: .76rem; }
/* 证书临期/过期着色（specialTone 输出 warn/bad，复用全局状态色） */
.sla-node .special.warn { color: var(--ak-signal-action); }
.sla-node .special.bad { color: var(--ak-signal-danger); }
.empty { padding: 10px 2px; font-size: .82rem; }
.chk { display: inline-flex; align-items: center; gap: 6px; font-size: .74rem; color: var(--ak-text-secondary); }
.chk input { accent-color: var(--ak-signal-info); }
.ok { color: var(--ak-signal-success); }
.warn { color: var(--ak-signal-action); }
.bad { color: var(--ak-signal-danger); }
.sla-trend { margin: 6px 0 4px; border: 1px solid var(--ui-line-faint); background: var(--ui-tint-ghost); padding: 8px 10px 2px; }
.sla-trend-cap { display: flex; justify-content: space-between; align-items: baseline; font-size: .72rem; color: var(--ak-text-secondary); margin-bottom: 4px; }
.sla-trend-cap .dim { font-size: .7rem; }
.sla-trend-cap .dim:last-child { color: var(--chart-red); }
</style>
