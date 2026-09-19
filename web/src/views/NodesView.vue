<template>
  <div>
    <div class="toolbar">
      <div class="tb-group">
        <div class="form-field">
          <label>可用率窗口</label>
          <RangeTabs :model-value="uptimeDays" :options="DAYS_WINDOWS" :disabled="uptBusy"
            aria-label="可用率统计窗口" @change="setUptimeDays" />
        </div>
      </div>
      <div class="tb-group tb-group--actions">
        <button class="ak-button ak-button--outline" @click="loadAll">刷新节点</button>
      </div>
      <span v-if="error" class="err">{{ error }}</span>
      <span class="dim" style="font-size:.85rem">在线 {{ onlineCount }} / {{ nodes.length }}</span>
    </div>
    <div v-if="loading" class="loading-center"><span class="ak-loading"></span></div>

    <div class="node-wall">
      <div v-for="n in nodes" :key="n.nodeId" class="node-card" :class="{ online: n.online }">
        <div class="node-head" style="display:flex;justify-content:space-between;align-items:center">
          <span class="node-id" :title="n.nodeId">
            <span class="dot" :class="n.online ? 'online' : 'offline'"></span>{{ n.label || n.nodeId }}
          </span>
          <span class="ak-tag ch" :class="n.online ? 'ak-tag--advanced' : 'ak-tag--neutral'">
            {{ n.online ? '在线' : '离线' }}
          </span>
        </div>
        <div class="node-label">{{ n.label || '（未命名）' }}</div>
        <div class="node-meta">
          <span :title="remoteHint(n.remoteAddr)">远端 {{ n.remoteAddr || '—' }}</span>
          <span>版本 {{ n.version || '—' }}</span>
        </div>
        <div class="node-meta">
          <span>最后活跃 {{ timeAgo(n.lastSeenAt) }}</span>
          <span>首见 {{ timeAgo(n.firstSeenAt) }}</span>
        </div>
        <!-- 可用率（来自上下线事件流还原，见后端 node_uptime.go）：统计窗口见顶部下拉 -->
        <div class="node-meta">
          <span :class="upTone(upt(n).availability)">
            可用率 {{ fmtAvail(upt(n).availability) }}
          </span>
          <span v-if="upt(n).seconds">
            宕机 {{ upt(n).downCount || 0 }} 次<span
              v-if="upt(n).offlineSeconds > 0" class="dim"> / 共 {{ humanDur(upt(n).offlineSeconds) }}</span>
          </span>
          <span v-else class="dim">统计窗口内无数据</span>
        </div>
        <div v-if="capsHint(n).hint" class="node-meta node-note" :class="capsHint(n).tone">
          <span>{{ capsHint(n).hint }}</span>
        </div>
        <div class="node-actions">
          <button class="ak-button ak-button--outline" @click="openEvents(n)">事件历史</button>
          <button class="ak-button ak-button--outline" @click="openUptime(n)">可用率</button>
          <button class="ak-button ak-button--outline" @click="openOta(n)">OTA 升级</button>
        </div>
      </div>
      <div v-if="!nodes.length && !loading" class="panel" style="grid-column:1/-1">
        <h2 class="panel-title">暂无节点</h2>
        <p class="dim" style="margin:0">节点接入后会自动出现在这里。</p>
      </div>
    </div>

    <!-- ===== 计划维护窗口 ===== -->
    <div class="panel" style="margin-top:24px">
      <div class="ota-head">
        <h2 class="panel-title">计划维护窗口 <span class="dim" style="font-size:.78rem;font-weight:400">窗口内的节点掉线 / 恢复不推送告警，事件与统计照常记录</span></h2>
        <div class="tb-group tb-group--actions">
          <button class="ak-button ak-button--outline" @click="openMaint">＋ 新建窗口</button>
          <button class="ak-button ak-button--outline" @click="loadMaint">刷新</button>
        </div>
      </div>

      <!-- 新建表单 -->
      <div v-if="maint.show" class="maint-form">
        <div class="form-field">
          <label>作用范围</label>
          <select class="ak-select" v-model="maint.scope">
            <option value="global">全部节点</option>
            <option v-for="n in nodes" :key="n.nodeId" :value="n.nodeId">{{ n.label || n.nodeId }}</option>
          </select>
        </div>
        <div class="form-field">
          <label>形态</label>
          <select class="ak-select" v-model="maint.repeatDaily" @change="onRepeatChange">
            <option :value="false">一次性时间段</option>
            <option :value="true">每日重复</option>
          </select>
        </div>
        <div class="form-field">
          <label>{{ maint.repeatDaily ? '每日开始' : '开始时间' }}</label>
          <input class="ak-input" :type="maint.repeatDaily ? 'time' : 'datetime-local'" v-model="maint.startAt" />
        </div>
        <div class="form-field">
          <label>{{ maint.repeatDaily ? '每日结束' : '结束时间' }}</label>
          <input class="ak-input" :type="maint.repeatDaily ? 'time' : 'datetime-local'" v-model="maint.endAt" />
        </div>
        <div class="form-field">
          <label>原因</label>
          <input class="ak-input" v-model.trim="maint.reason" placeholder="如：机房割接 / 例行重启" style="width:200px" />
        </div>
        <div class="tb-group tb-group--actions">
          <button class="ak-button ak-button--action" :disabled="maint.busy" @click="submitMaint">
            {{ maint.busy ? '保存中…' : '保存' }}
          </button>
          <button class="ak-button ak-button--outline" @click="maint.show = false">取消</button>
        </div>
        <div v-if="maint.err" class="err" style="flex-basis:100%">{{ maint.err }}</div>
      </div>
      <p v-if="maint.show && maint.repeatDaily" class="dim maint-hint">
        每日重复只取时刻（服务器所在时区），支持跨零点（如 23:30 → 00:30）。
      </p>

      <div class="ak-table-wrap">
        <table class="ak-table">
          <thead>
            <tr><th>范围</th><th>时间</th><th>形态</th><th>原因</th><th>状态</th><th>创建人</th><th></th></tr>
          </thead>
          <tbody>
            <tr v-for="w in maintList" :key="w.id">
              <td class="mono">{{ w.scope === 'global' || !w.scope ? '全部节点' : w.scope }}</td>
              <td class="mono nowrap">{{ maintRange(w) }}</td>
              <td>{{ w.repeatDaily ? '每日重复' : '一次性' }}</td>
              <td class="dim">{{ w.reason || '—' }}</td>
              <td>
                <span v-if="w.active" class="ota-status success">生效中</span>
                <span v-else-if="w.expired" class="dim">已结束</span>
                <span v-else class="dim">未开始</span>
              </td>
              <td class="dim">{{ w.createdBy || '—' }}</td>
              <td><button class="ak-button ak-button--outline sm danger" @click="removeMaint(w)">删除</button></td>
            </tr>
            <tr v-if="!maintList.length"><td colspan="7" class="dim">暂无维护窗口 —— 割接 / 发布前建一个，可避免计划内断连刷告警</td></tr>
          </tbody>
        </table>
      </div>
    </div>

    <!-- ===== OTA 任务列表 ===== -->
    <div class="panel" style="margin-top:24px">
      <div class="ota-head">
        <h2 class="panel-title">OTA 升级任务 <span class="dim" style="font-size:.78rem;font-weight:400">节点重连后版本号变化即判定成功</span></h2>
        <button class="ak-button ak-button--outline" @click="loadOta">刷新</button>
      </div>
      <div class="ak-table-wrap">
        <table class="ak-table">
          <thead>
            <tr><th>#</th><th>节点</th><th>版本变化</th><th>通道</th><th>状态</th><th>阶段 / 原因</th><th>操作人</th><th>下发时间</th></tr>
          </thead>
          <tbody>
            <tr v-for="t in otaTasks" :key="t.id">
              <td class="mono dim">{{ t.id }}</td>
              <td class="mono">{{ t.nodeId }}</td>
              <td class="mono">{{ t.fromVersion || '—' }} → {{ t.targetVersion || (t.url ? '按下载地址' : '—') }}</td>
              <td>{{ t.channel === 'ws' ? 'WS' : 'HTTP' }}</td>
              <td>
                <span class="ota-status" :class="t.status">
                  {{ t.status === 'dispatched' ? '进行中' : t.status === 'success' ? '成功' : '失败' }}
                </span>
              </td>
              <td style="word-break:break-all">
                <span v-if="t.status === 'dispatched'" class="dim">{{ t.stage || '等待节点响应…' }}</span>
                <span v-else-if="t.error" class="err">{{ t.error }}</span>
                <span v-else class="dim">—</span>
              </td>
              <td class="dim">{{ t.dispatchedBy || '—' }}</td>
              <td class="mono nowrap">{{ fmtTime(t.dispatchedAt) }}</td>
            </tr>
            <tr v-if="!otaTasks.length"><td colspan="8" class="dim">暂无 OTA 任务</td></tr>
          </tbody>
        </table>
      </div>
    </div>

    <!-- OTA 下发对话框 -->
    <div v-if="ota.show" class="ev-mask" @click.self="ota.show = false">
      <div class="ev-panel">
        <header class="ev-head">
          <span class="mono">{{ ota.nodeId }}</span>
          <span class="dim">OTA 升级</span>
          <button class="ak-button ak-button--outline" style="margin-left:auto;padding:3px 10px" @click="ota.show = false">关闭</button>
        </header>
        <div class="ak-form-stack" style="margin-top:12px">
          <div class="switch-row">
            <span class="ak-label">方式</span>
            <button class="ak-toggle" :class="{ on: ota.mode === 'version' }" @click.prevent="ota.mode = 'version'">按版本（自动匹配发布资产）</button>
            <button class="ak-toggle" :class="{ on: ota.mode === 'url' }" @click.prevent="ota.mode = 'url'">直发下载地址</button>
          </div>
          <label class="ak-field" v-if="ota.mode === 'version'">
            <span class="ak-label">目标版本</span>
            <input class="ak-input" v-model.trim="ota.version" placeholder="如 v1.2.3（发布标签）" />
          </label>
          <label class="ak-field" v-if="ota.mode === 'url'">
            <span class="ak-label">下载地址</span>
            <input class="ak-input" v-model.trim="ota.url" placeholder="https://…（对应节点平台的二进制）" />
          </label>
          <label class="ak-field">
            <span class="ak-label">SHA256 <span class="dim">（可选校验）</span></span>
            <input class="ak-input" v-model.trim="ota.sha256" placeholder="64 位 hex，兼容 sha256: 前缀" />
          </label>
        </div>
        <p class="dim" style="font-size:.75rem;line-height:1.5;margin:10px 0 0">
          节点将下载并替换自身二进制后重启，期间会短暂断开连接，属正常现象；成败以重连后的版本号为准。
          老版本节点不认识该指令，会按任务超时处理。
        </p>
        <div v-if="ota.msg" class="ok-text" style="font-size:.8rem;margin-top:10px;word-break:break-all">{{ ota.msg }}</div>
        <div v-if="ota.err" class="err" style="font-size:.8rem;margin-top:10px;word-break:break-all">{{ ota.err }}</div>
        <div class="dlg-foot">
          <button class="ak-button ak-button--outline" @click="ota.show = false">取消</button>
          <button class="ak-button ak-button--action" :disabled="ota.busy" @click="submitOta">
            {{ ota.busy ? '下发中…' : '下发 OTA' }}
          </button>
        </div>
      </div>
    </div>

    <!-- 可用率趋势抽屉 -->
    <div v-if="uptPanel" class="ev-mask" @click.self="uptPanel = null">
      <div class="ev-panel">
        <header class="ev-head">
          <span class="mono">{{ uptPanel.nodeId }}</span>
          <span class="dim">可用率趋势</span>
          <button class="ak-button ak-button--outline" style="margin-left:auto;padding:3px 10px" @click="uptPanel = null">关闭</button>
        </header>
        <!-- 图上直接换窗口：与顶部「可用率窗口」同一状态，节点卡片上的可用率一并跟着变 -->
        <div class="upt-bar">
          <span class="dim">统计窗口</span>
          <RangeTabs small :model-value="uptimeDays" :options="DAYS_WINDOWS" :disabled="uptBusy"
            aria-label="可用率统计窗口" @change="setUptimeDays" />
          <span v-if="uptBusy" class="dim">切换中…</span>
        </div>
        <div v-if="uptPanel.loading" class="loading-center"><span class="ak-loading"></span></div>
        <template v-else-if="uptPanel.data">
          <div class="upt-sum">
            <span :class="upTone(uptPanel.data.availability)">
              窗口可用率 {{ fmtAvail(uptPanel.data.availability) }}
            </span>
            <span class="dim">宕机 {{ uptPanel.data.downCount || 0 }} 次</span>
            <span class="dim">累计 {{ humanDur(uptPanel.data.offlineSeconds) }}</span>
            <span class="dim">统计起点 {{ fmtTime(uptPanel.data.from) }}</span>
          </div>
          <EChart v-if="(uptPanel.data.daily || []).length" :option="uptOption" height="240px" />
          <p v-else class="dim" style="margin:14px 0 0">该窗口内没有可用日数据。</p>
          <p class="dim" style="font-size:.72rem;line-height:1.5;margin:12px 0 0">
            口径：按上下线事件还原离线区间，与告警的去抖无关（事件记录的是断连真实时刻）；
            节点首见之前不算宕机，统计区间也不超出数据保留期。
          </p>
        </template>
      </div>
    </div>

    <!-- 事件抽屉 -->
    <div v-if="drawer" class="ev-mask" @click.self="drawer = null">
      <div class="ev-panel">
        <header class="ev-head">
          <span class="mono">{{ drawer.nodeId }}</span>
          <span class="dim">在线/离线事件</span>
          <button class="ak-button ak-button--outline" style="margin-left:auto;padding:3px 10px"
            :disabled="exportingEvents" @click="doExportEvents(drawer)">
            {{ exportingEvents ? '导出中…' : '导出 CSV' }}
          </button>
          <button class="ak-button ak-button--outline" style="padding:3px 10px" @click="drawer=null">关闭</button>
        </header>
        <div class="ak-table-wrap" style="max-height:60vh;overflow:auto">
          <table class="ak-table">
            <thead><tr><th>时间</th><th>事件</th><th>原因</th></tr></thead>
            <tbody>
              <tr v-for="e in events" :key="e.id">
                <td class="mono nowrap">{{ fmtTime(e.createdAt) }}</td>
                <td><span class="dot" :class="e.event === 'online' ? 'online' : 'offline'"></span>
                  <span :class="e.event === 'online' ? 'ok-200' : 'err'">{{ e.event === 'online' ? '上线' : '下线' }}</span></td>
                <td class="dim" style="word-break:break-all">{{ e.reason || '—' }}</td>
              </tr>
              <tr v-if="!events.length"><td colspan="3" class="dim">暂无事件</td></tr>
            </tbody>
          </table>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, reactive, computed, onMounted, onUnmounted, watch } from 'vue'
import {
  fetchNodes, fetchNodeEvents, dispatchNodeOta, fetchOtaTasks,
  fetchNodesUptime, fetchNodeUptime, exportNodeEvents,
  fetchMaintenance, createMaintenance, deleteMaintenance,
} from '../api/boce.js'
import { timeAgo, fmtTime } from '../utils/format.js'
import { useTheme, readChartTheme } from '../composables/useTheme.js'
import EChart from '../components/EChart.vue'
import RangeTabs from '../components/RangeTabs.vue'
import { useDialog } from '../composables/useDialog.js'
import { DAYS_WINDOWS } from '../utils/timeWindow.js'

const dialog = useDialog()

const nodes = ref([])
const events = ref([])
const drawer = ref(null)
const loading = ref(false)
const error = ref('')

// ===== 可用率 =====
const uptimeDays = ref(7)
const uptimeMap = ref({}) // nodeId -> 后端 nodeUptimeStats
const uptPanel = ref(null) // { nodeId, loading, data }
const uptBusy = ref(false) // 切换统计窗口中：禁用按钮，避免连点堆请求
const exportingEvents = ref(false)

// ===== 维护窗口 =====
const maintList = ref([])
const maint = reactive({ show: false, scope: 'global', repeatDaily: false, startAt: '', endAt: '', reason: '', busy: false, err: '' })

// ===== OTA =====
const otaTasks = ref([])
const ota = reactive({
  show: false, nodeId: '', mode: 'version', version: '', url: '', sha256: '',
  busy: false, err: '', msg: '',
})
let otaTimer = null

const onlineCount = computed(() => nodes.value.filter((n) => n.online).length)

// ==== 图表主题（echarts 读不到 CSS 变量，需随主题重算）====
const { theme } = useTheme()
const CH = reactive({ ...readChartTheme() })
watch(theme, () => Object.assign(CH, readChartTheme()))

// 「远端」是节点连上中心时的来源地址（由中心尽力还原）。显示 127.0.0.1 / 内网地址时，说明
// 中心与节点之间还有一层本机或内网代理（反代 / 隧道），此时它只是"最后一跳"，不是节点地址。
function remoteHint(addr) {
  const a = String(addr || '')
  if (!a) return '该节点未记录来源地址（HTTP 接入的节点不记录这一项）'
  if (/^(127\.|10\.|192\.168\.|172\.(1[6-9]|2\d|3[01])\.|::1$)/.test(a)) {
    return '本机或内网地址：中心与节点之间存在代理（反向代理 / 隧道），这里显示的是最后一跳'
  }
  return '节点连接中心时的来源地址'
}

// 节点上报的能力清单（逗号分隔，如 "probe,report,config"；老版本节点不上报，为空串）。
// 节点版本号由 n.version 直接展示——节点不自更新，升级部署后重新注册即为新版本。
// 这里只在"未上报能力清单"时给一句排障提示：这类旧程序可能不认识控制台下发的运行时配置指令。
const capsOf = (n) => String(n?.capabilities || '').split(',').map((s) => s.trim()).filter(Boolean)
function capsHint(n) {
  if (capsOf(n).length) return { tone: '', hint: '' }
  return {
    tone: 'warn',
    hint: '旧版节点：未上报能力清单，运行时配置指令可能不被识别，建议升级节点程序',
  }
}

// ===== 可用率展示辅助 =====
// 后端 availability 是 0~1 的比值（与 SLA 的 0~100 不同），且 seconds=0 表示"窗口内该节点还不存在"
const upt = (n) => uptimeMap.value[n.nodeId] || {}
function fmtAvail(v) {
  if (v == null) return '—'
  return (Number(v) * 100).toFixed(2).replace(/\.?0+$/, '') + '%'
}
function upTone(v) {
  if (v == null) return 'dim'
  const p = Number(v) * 100
  return p >= 99 ? 'up-ok' : p >= 95 ? 'up-warn' : 'up-bad'
}
// 秒 → 「1天3小时」这类人话；不足 1 分钟显示秒
function humanDur(sec) {
  const s = Number(sec) || 0
  if (s <= 0) return '0'
  if (s < 60) return `${s} 秒`
  const d = Math.floor(s / 86400)
  const h = Math.floor((s % 86400) / 3600)
  const m = Math.floor((s % 3600) / 60)
  const parts = []
  if (d) parts.push(`${d} 天`)
  if (h) parts.push(`${h} 小时`)
  if (!d && m) parts.push(`${m} 分`)
  return parts.join(' ') || '不到 1 分'
}

onMounted(() => {
  loadAll()
  loadMaint()
})
onUnmounted(() => { if (otaTimer) clearInterval(otaTimer) })

function loadAll() {
  load()
  loadUptime()
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    nodes.value = await fetchNodes()
  } catch (e) {
    error.value = e?.message || '加载失败'
  } finally {
    loading.value = false
  }
}

async function loadUptime() {
  try {
    const resp = await fetchNodesUptime(uptimeDays.value)
    const m = {}
    for (const s of resp?.nodes || []) m[s.nodeId] = s
    uptimeMap.value = m
  } catch (e) {
    // 可用率失败不该顶掉节点列表的报错位：单独提示，卡片上退化为"—"
    uptimeMap.value = {}
    error.value = e?.message || '可用率加载失败'
  }
}

async function openEvents(n) {
  drawer.value = n
  events.value = []
  try {
    events.value = await fetchNodeEvents(n.nodeId, 100)
  } catch (e) {
    // 拉取失败不能静默成"暂无事件"，否则会被误读为该节点从未有过事件
    error.value = e?.message || '事件历史加载失败'
  }
}

async function doExportEvents(n) {
  exportingEvents.value = true
  try {
    await exportNodeEvents(n.nodeId, 30)
  } catch (e) {
    error.value = e?.message || '事件导出失败'
  } finally {
    exportingEvents.value = false
  }
}

// ===== 可用率趋势 =====
async function openUptime(n) {
  uptPanel.value = { nodeId: n.nodeId, loading: true, data: null }
  await reloadUptimePanel(n.nodeId)
}

// 抽屉可能在加载期间被关掉/切成别的节点：只在仍是同一节点时回填（切换窗口时走这里，
// 不重置 loading，旧图留着直到新数据到达，避免"图表闪一下没了"）
async function reloadUptimePanel(nodeId) {
  try {
    const data = await fetchNodeUptime(nodeId, uptimeDays.value)
    if (uptPanel.value?.nodeId === nodeId) uptPanel.value = { nodeId, loading: false, data }
  } catch (e) {
    uptPanel.value = null
    error.value = e?.message || '可用率加载失败'
  }
}

// 快捷换统计窗口：节点卡片的可用率（loadUptime）与抽屉折线一起换成新窗口
async function setUptimeDays(v) {
  if (uptBusy.value || v === uptimeDays.value) return
  uptimeDays.value = v
  uptBusy.value = true
  try {
    await loadUptime()
    const nid = uptPanel.value?.nodeId
    if (nid) await reloadUptimePanel(nid)
  } finally {
    uptBusy.value = false
  }
}

// 逐日可用率折线 + 当日宕机次数柱（双轴）：折线看稳定性趋势，柱看故障频次
const uptOption = computed(() => {
  const d = uptPanel.value?.data
  const daily = d?.daily || []
  return {
    color: [CH.cyan, CH.yellow],
    tooltip: {
      trigger: 'axis',
      backgroundColor: CH.tipBg, borderColor: CH.tipBorder, textStyle: { color: CH.tipText },
    },
    legend: { top: 0, textStyle: { color: CH.label, fontSize: 11 }, itemWidth: 14 },
    grid: { left: 56, right: 44, top: 30, bottom: 28 },
    xAxis: {
      type: 'category',
      data: daily.map((x) => x.date),
      axisLine: { lineStyle: { color: CH.axis } },
      axisLabel: { color: CH.label, hideOverlap: true },
      splitLine: { show: false },
    },
    yAxis: [
      {
        type: 'value', name: '%', scale: true, max: 100,
        axisLine: { lineStyle: { color: CH.axis } },
        axisLabel: { color: CH.label, formatter: '{value}' },
        splitLine: { lineStyle: { color: CH.grid } },
      },
      {
        type: 'value', name: '次', minInterval: 1,
        axisLine: { lineStyle: { color: CH.axis } },
        axisLabel: { color: CH.label },
        splitLine: { show: false },
      },
    ],
    series: [
      {
        name: '可用率', type: 'line', showSymbol: true, symbolSize: 5,
        lineStyle: { width: 2.5 }, areaStyle: { opacity: 0.08 },
        data: daily.map((x) => (Number(x.availability) * 100).toFixed(3)),
      },
      { name: '宕机次数', type: 'bar', yAxisIndex: 1, barMaxWidth: 18, data: daily.map((x) => x.downCount || 0) },
    ],
  }
})

// ===== 维护窗口 =====
async function loadMaint() {
  try {
    maintList.value = await fetchMaintenance()
  } catch {
    maintList.value = []
  }
}

function openMaint() {
  maint.show = true
  maint.scope = 'global'
  maint.repeatDaily = false
  maint.startAt = ''
  maint.endAt = ''
  maint.reason = ''
  maint.err = ''
}

// 形态切换会同时换掉两个输入的 type（datetime-local ↔ time），两种值格式不通用，
// 必须在切换时清空，否则残留的 "2026-09-19T03:00" 会让 time 控件渲染成空但 v-model 仍有值。
function onRepeatChange() {
  maint.startAt = ''
  maint.endAt = ''
}

function maintRange(w) {
  if (w.repeatDaily) {
    // 只展示"时刻"：后端存的是 UTC，必须经 fmtTime 转成本地时区再截时间部分，
    // 否则会把 03:00（本地）显示成 19:00（UTC）。
    const t = (v) => fmtTime(v).slice(11, 16)
    return `每日 ${t(w.startAt)} ~ ${t(w.endAt)}`
  }
  return `${fmtTime(w.startAt)} ~ ${fmtTime(w.endAt)}`
}

async function submitMaint() {
  if (!maint.startAt || !maint.endAt) { maint.err = '请填写开始与结束时间'; return }
  maint.busy = true
  maint.err = ''
  try {
    await createMaintenance({
      scope: maint.scope, startAt: maint.startAt, endAt: maint.endAt,
      repeatDaily: !!maint.repeatDaily, reason: maint.reason,
    })
    maint.show = false
    await loadMaint()
  } catch (e) {
    maint.err = e?.message || '保存失败'
  } finally {
    maint.busy = false
  }
}

async function removeMaint(w) {
  const ok = await dialog.confirm({
    title: '删除维护窗口',
    message: `确定删除「${maintRange(w)}」这个维护窗口？删除后该时段的节点告警将恢复推送。`,
    kind: 'danger',
    confirmText: '删除',
    cancelText: '取消',
  })
  if (!ok) return
  try {
    await deleteMaintenance(w.id)
    await loadMaint()
  } catch (e) {
    error.value = e?.message || '删除失败'
  }
}

// ===== OTA =====
async function loadOta() {
  try {
    otaTasks.value = await fetchOtaTasks(50)
  } catch {
    otaTasks.value = []
  }
  // 有进行中的任务时 10s 轮询（节点重连/超时收敛后状态会变化），空闲即停
  if (otaTasks.value.some((t) => t.status === 'dispatched')) {
    if (!otaTimer) otaTimer = setInterval(loadOta, 10000)
  } else if (otaTimer) {
    clearInterval(otaTimer)
    otaTimer = null
  }
}

function openOta(n) {
  ota.show = true
  ota.nodeId = n.nodeId
  ota.mode = 'version'
  ota.version = ''
  ota.url = ''
  ota.sha256 = ''
  ota.err = ''
  ota.msg = ''
}

async function submitOta() {
  const payload = { sha256: ota.sha256 }
  if (ota.mode === 'version') {
    if (!ota.version) { ota.err = '请填写目标版本'; return }
    payload.version = ota.version
  } else {
    if (!ota.url) { ota.err = '请填写下载地址'; return }
    payload.url = ota.url
  }
  ota.busy = true; ota.err = ''; ota.msg = ''
  try {
    const d = await dispatchNodeOta(ota.nodeId, payload)
    ota.msg = d.hint || '已下发'
    await loadOta()
  } catch (e) {
    ota.err = e?.message || '下发失败'
  } finally {
    ota.busy = false
  }
}
</script>

<style scoped>
/* 旧版节点提示：贴在卡片上，按严重程度着色 */
.node-card .node-note { font-size: 0.75rem; line-height: 1.4; word-break: break-all; }
.node-card .node-note.ok { color: var(--ak-signal-success); }
.node-card .node-note.warn { color: var(--ak-signal-info); }
.node-card .node-note.err { color: var(--ak-signal-danger); }
/* 可用率的高低三档（卡片与抽屉共用）；类名加 up- 前缀，
   避免与旧版节点提示的 .warn 撞车（两者都可能出现在 .node-meta 上） */
.node-card .node-meta .up-ok, .upt-sum .up-ok { color: var(--ak-signal-success); }
.node-card .node-meta .up-warn, .upt-sum .up-warn { color: var(--ak-signal-warn); }
.node-card .node-meta .up-bad, .upt-sum .up-bad { color: var(--ak-signal-danger); }

/* ===== 维护窗口 ===== */
.maint-form {
  display: flex; flex-wrap: wrap; align-items: flex-end; gap: 12px;
  padding: 12px 0 14px; border-bottom: 1px solid var(--ui-line); margin-bottom: 12px;
}
.maint-hint { font-size: .75rem; line-height: 1.5; margin: -6px 0 12px; }
.upt-sum { display: flex; flex-wrap: wrap; gap: 16px; font-size: .82rem; margin: 12px 0 4px; }
/* 趋势抽屉里的统计窗口切换行（紧跟标题栏，图上方） */
.upt-bar { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; margin-top: 10px; }
.upt-bar .dim { font-size: .72rem; }

/* ===== OTA ===== */
.ota-head {
  display: flex; align-items: center; justify-content: space-between; gap: 12px;
  margin-bottom: 12px;
}
.ota-head .panel-title { margin: 0; }
.ota-status {
  font-size: .75rem; padding: 2px 8px; border-radius: 3px;
  background: var(--ui-tint);
}
.ota-status.dispatched { background: color-mix(in srgb, var(--ak-signal-info) 18%, transparent); color: var(--ak-signal-info); }
.ota-status.success { background: color-mix(in srgb, var(--ak-signal-success) 18%, transparent); color: var(--ak-signal-success); }
.ota-status.failed { background: color-mix(in srgb, var(--ak-signal-danger) 18%, transparent); color: var(--ak-signal-danger); }
.dlg-foot { display: flex; justify-content: flex-end; gap: 10px; margin-top: 18px; }
.switch-row { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
.switch-row .ak-label { min-width: 56px; }
.ak-toggle {
  border: var(--ak-line-hairline) solid var(--ui-line-ctl);
  background: transparent;
  color: var(--ak-text-secondary);
  font-size: 0.72rem;
  padding: 2px 10px;
  border-radius: 10px;
  cursor: pointer;
}
.ak-toggle.on { color: var(--ak-signal-success); border-color: currentColor; }

.ev-mask {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.55);
  z-index: 50;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 20px;
}
.ev-panel {
  width: min(640px, 100%);
  background: var(--ak-surface-inverse);
  border: var(--ak-line-hairline) solid var(--ui-line-strong);
  border-top: 3px solid var(--ak-signal-info);
  padding: 16px 18px;
  max-height: 80vh;
  display: flex;
  flex-direction: column;
}
.ev-head { display: flex; align-items: center; gap: 12px; padding-bottom: 10px; border-bottom: 1px solid var(--ui-line); }
</style>
