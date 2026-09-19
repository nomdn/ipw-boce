<template>
  <div class="status-wrap">
    <div v-if="notFound" class="panel nf">链接无效或已关闭分享</div>
    <div v-else-if="loadErr" class="panel nf">
      {{ loadErr }}
      <button class="ak-button ak-button--outline" style="margin-left:10px" @click="load">重试</button>
    </div>
    <template v-else>
      <header class="st-brand">
        <div>
          <div class="st-badge">LEMON / IPW</div>
          <h1 class="st-title">服务可用性状态</h1>
        </div>
        <TimeRangePicker :model-value="range" :max-days="30" :disabled="winBusy"
          aria-label="统计窗口" @update:model-value="onRange" />
      </header>

      <p v-if="!tasks.length" class="dim">该分享链接下暂无任务数据。</p>

      <!-- 每任务一张卡（多选分享时同页展示多个任务） -->
      <section v-for="(t, i) in tasks" :key="i" class="panel stask">
        <h2 class="name">{{ t.name }}</h2>
        <div class="meta">
          {{ apiLabel(t.apiType) || t.apiType }}<span v-if="t.target"> · <span class="mono">{{ t.target }}</span></span>
        </div>

        <div class="kpis">
          <div class="kpi" :class="upTone(t.availability)"><div class="k">可用率</div><div class="v">{{ fmtPct(t.availability) }}</div></div>
          <div class="kpi"><div class="k">样本</div><div class="v">{{ t.samples ?? '—' }}</div></div>
          <div class="kpi"><div class="k">成功 / 失败</div><div class="v"><span class="ok-200">{{ t.up ?? 0 }}</span> / <span class="err">{{ t.down ?? 0 }}</span></div></div>
          <div class="kpi"><div class="k">平均延迟</div><div class="v">{{ t.avgMs != null ? t.avgMs + ' ms' : '—' }}</div></div>
          <div class="kpi"><div class="k">P95 延迟</div><div class="v">{{ t.p95Ms != null ? t.p95Ms + ' ms' : '—' }}</div></div>
        </div>

        <div class="curve-cap">延迟趋势 <span class="dim">/ 每个点 = 一轮多节点平均延迟 · 红菱形 = 有节点失败的轮次</span></div>
        <div class="curve-box">
          <EChart v-if="hasCurve(t)" :option="curveOption(t)" height="180px" />
          <div v-else class="dim empty">该窗口暂无定时样本</div>
        </div>

        <div v-if="t.byNode && t.byNode.length" class="ak-table-wrap">
          <table class="ak-table">
            <thead><tr><th>节点</th><th>可用率</th><th>成功 / 失败</th><th>平均延迟</th><th>P95</th><th>最新延迟</th></tr></thead>
            <tbody>
              <tr v-for="n in t.byNode" :key="n.nodeId">
                <td class="mono" :title="n.nodeId">{{ n.label || n.nodeId }}</td>
                <td class="mono" :class="upTone(n.availability)">{{ fmtPct(n.availability) }}</td>
                <td class="mono"><span class="ok-200">{{ n.up }}</span> / <span class="err">{{ n.down }}</span></td>
                <td class="mono">{{ n.avgMs }}ms</td>
                <td class="mono dim">{{ n.p95Ms }}ms</td>
                <td class="mono">{{ n.latestMs }}ms</td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>

      <div class="foot dim">每 30 秒自动刷新 · 数据截至 {{ fmtTime(lastWindowTo) }}</div>
    </template>
  </div>
</template>

<script setup>
// 公开状态页（B3）：免登录只读。数据来自收集中心公开 JSON
// GET {API_BASE}/api/public/status/:token —— 无 Token、无 Cookie，仅暴露该分享组的窗口聚合。
// 结构（后端 public_status.go）：{ hours, tasks:[{ name/apiType/target?, KPI..., byNode, series }] }
import { ref, reactive, computed, onMounted, onBeforeUnmount, watch } from 'vue'
import { useRoute } from 'vue-router'
import { http } from '../api/http.js'
import { API_BASE } from '../config.js'
import { fmtTime } from '../utils/format.js'
import { apiLabel } from '../utils/probeMeta.js'
import EChart from '../components/EChart.vue'
import TimeRangePicker from '../components/TimeRangePicker.vue'
import { useTheme, readChartTheme } from '../composables/useTheme.js'
import { resolveRange } from '../utils/timeRange.js'

const route = useRoute()
// 必须响应式：vue-router 会复用同一组件实例，从 /s/aaa 切到 /s/bbb 时
// 若把 token 取成普通常量，页面会一直停留在旧分享码的数据上。
const token = computed(() => String(route.params.token || ''))
const notFound = ref(false)
const loadErr = ref('')
const range = ref({ key: '24h' })
// 公开状态页后端只认 ?hours=N（相对窗口，见 public_status.go），任意选择
// （含自然周期/自定义/平移）都折算成"最近 N 小时"交给后端按"现在"重算，规避本地时钟偏差。
const hours = computed(() => {
  const r = resolveRange(range.value)
  return Math.min(24 * 90, Math.max(1, Math.round(r.hours)))
})
const st = ref({})
let timer = null

const tasks = computed(() => st.value.tasks || [])
const lastWindowTo = computed(() => tasks.value.length ? tasks.value[tasks.value.length - 1].window?.to : null)

const hasCurve = (t) => Array.isArray(t.series) && t.series.some((s) => s.avgMs > 0)

function fmtPct(v) {
  return v == null ? '—' : Number(v).toFixed(2) + '%'
}
function upTone(v) {
  if (v == null) return ''
  return v >= 99 ? 'ok' : v >= 95 ? 'warn' : 'bad'
}

// 延迟曲线（按任务）：只画"一轮多节点平均延迟"一条线；失败轮次以红菱形标在同图顶部。
// 多节点各画一条线会让图例溢出分享页窄栏，故分享页刻意不叠加按节点细线（节点明细见下方表格）。
// 网格/轴/label/提示框属中立色，随主题走（echarts 吃不到 CSS 变量，切主题时重读一次）
const { theme } = useTheme()
const CH = reactive({ ...readChartTheme() })
watch(theme, () => Object.assign(CH, readChartTheme()))

const toPts = (series) => (series || [])
  .map((s) => {
    const t = new Date(s.time).getTime()
    const v = typeof s.avgMs === 'number' && s.avgMs > 0 ? s.avgMs : null
    return Number.isNaN(t) || v == null ? null : [t, v]
  })
  .filter(Boolean)
function curveOption(t) {
  // 分享页只画"平均延迟"一条线（避免多节点图例溢出）；失败轮次以红菱形标在同图顶部。
  const series = [{
    name: '平均延迟', type: 'line', showSymbol: false, connectNulls: false,
    color: CH.cyan, lineStyle: { width: 2.5 }, areaStyle: { opacity: 0.08 },
    data: toPts(t.series),
  }]
  const dots = (t.series || [])
    .map((s) => {
      const tm = new Date(s.time).getTime()
      return Number.isNaN(tm) || !(s.down > 0) ? null : [tm, 0.9]
    })
    .filter(Boolean)
  return {
    tooltip: {
      trigger: 'axis', axisPointer: { type: 'line' },
      backgroundColor: CH.tipBg, borderColor: CH.tipBorder, textStyle: { color: CH.tipText },
      valueFormatter: (v) => (typeof v === 'number' ? v + ' ms' : '无'),
    },
    grid: { left: 56, right: 16, top: 18, bottom: 28 },
    xAxis: { type: 'time', axisLine: { lineStyle: { color: CH.axis } }, axisLabel: { color: CH.label, hideOverlap: true }, splitLine: { lineStyle: { color: CH.grid } } },
    yAxis: [
      { type: 'value', name: 'ms', axisLine: { lineStyle: { color: CH.axis } }, axisLabel: { color: CH.label }, splitLine: { lineStyle: { color: CH.grid } } },
      { type: 'value', min: 0, max: 1, show: false },
    ],
    series: [
      ...series,
      { name: '失败轮次', type: 'scatter', yAxisIndex: 1, symbol: 'diamond', symbolSize: 7, itemStyle: { color: CH.red }, data: dots },
    ],
  }
}

async function load() {
  try {
    const r = await http.get(`/api/public/status/${encodeURIComponent(token.value)}?hours=${hours.value}`)
    st.value = r || {}
    notFound.value = false
    loadErr.value = ''
  } catch (e) {
    if (e?.status === 404) {
      notFound.value = true
    } else {
      // 429（防枚举限流）/网络错误等：明确展示，而不是无声卡在"加载中"
      loadErr.value = e?.message || '数据加载失败'
    }
  }
}

onMounted(() => {
  load()
  timer = setInterval(load, 30000)
})
onBeforeUnmount(() => timer && clearInterval(timer))

// 快捷换统计窗口：winBusy 期间禁用选择器，避免连点堆一串请求（30s 自动刷新不受影响）
const winBusy = ref(false)
async function onRange(v) {
  if (winBusy.value || v.key === range.value.key) return
  range.value = v
  winBusy.value = true
  try {
    await load()
  } finally {
    winBusy.value = false
  }
}

// 分享码变化（同一组件实例内切换 /s/:token）→ 清掉旧的错误态并重新拉取
watch(token, () => {
  notFound.value = false
  loadErr.value = ''
  load()
})
</script>

<style scoped>
.status-wrap { max-width: 900px; margin: 0 auto; padding: 40px 20px; }
.st-brand {
  display: flex; align-items: flex-end; justify-content: space-between; gap: 16px;
  flex-wrap: wrap; margin-bottom: 22px;
  padding-bottom: 14px; border-bottom: 1px solid var(--ui-line);
}
.st-badge {
  font-family: var(--ak-font-mono); font-size: .68rem;
  letter-spacing: .3em; color: var(--ak-signal-info); margin-bottom: 6px;
}
.st-title { font-size: 1.35rem; margin: 0; font-family: var(--ak-font-command); }
.stask { margin-bottom: 22px; }
.name { font-size: 1.3rem; margin: 0 0 6px; font-family: var(--ak-font-command); }
.meta { color: var(--ak-text-secondary); font-size: .85rem; margin-bottom: 18px; display: flex; align-items: center; gap: 4px; flex-wrap: wrap; }
.kpis { display: flex; gap: 10px; flex-wrap: wrap; margin-bottom: 20px; }
.kpi { border: var(--ak-line-hairline) solid var(--ui-line); padding: 10px 16px; min-width: 110px; }
.kpi .k { font-size: .68rem; color: var(--ak-text-secondary); margin-bottom: 3px; }
.kpi .v { font-size: 1.15rem; font-weight: 600; }
.kpi.ok .v { color: var(--ak-signal-success); }
.kpi.warn .v { color: var(--ak-signal-action); }
.kpi.bad .v { color: var(--ak-signal-danger); }
.curve-cap { font-size: .75rem; color: var(--ak-text-secondary); margin: 0 0 6px; }
.curve-box { border: var(--ak-line-hairline) solid var(--ui-line); border-radius: 4px; padding: 6px; margin-bottom: 20px; }
.foot { margin-top: 18px; font-size: .72rem; }
.nf { max-width: 480px; margin: 60px auto; text-align: center; padding: 24px; }
</style>
