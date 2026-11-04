<template>
  <div class="page wide">
    <div class="page-head">
      <div>
        <h1>SLA 监控</h1>
        <div class="sub">定时拨测任务，按节点聚合可用率与延迟<span v-if="live" class="ok"> · 实时推送已连接</span></div>
      </div>
      <div class="row">
        <div class="seg">
          <button v-for="h in [1, 6, 24, 72, 168]" :key="h" :class="{ on: hours === h }" @click="hours = h">{{ h < 24 ? `${h}h` : `${h / 24}d` }}</button>
        </div>
        <button class="btn" :disabled="!picked.length" @click="shareOpen = true">分享 {{ picked.length || '' }}</button>
        <button class="btn primary" @click="edit()">+ 新建任务</button>
      </div>
    </div>

    <div v-if="!tasks.length" class="empty">还没有任务，新建一个开始监控</div>

    <div v-for="t in tasks" :key="t.id" class="card task">
      <div class="row between">
        <div class="row" style="min-width: 0">
          <input type="checkbox" v-model="picked" :value="t.id" />
          <b>{{ t.name }}</b>
          <span class="pill mono">{{ t.apiType }}</span>
          <span class="muted mono ellipsis">{{ t.target }}</span>
          <span v-if="t.shareToken" class="pill ok">已分享</span>
          <span v-if="auth.isAdmin && t.ownerUsername" class="faint">@{{ t.ownerUsername }}</span>
        </div>
        <div class="row">
          <Switch :model-value="t.enabled" @change="(v) => toggle(t, v)" />
          <button class="btn sm" @click="api.download(`/admin/tasks/${t.id}/series/export?hours=${hours}`).catch(toastErr)">CSV</button>
          <button class="btn sm" @click="edit(t)">编辑</button>
          <button class="btn sm danger" @click="del(t)">删除</button>
        </div>
      </div>
      <template v-if="sla[t.id]">
        <div class="grid c4" style="margin-top: 12px">
          <div class="stat"><div class="k">可用率</div><div class="v" :class="agg(t.id).avail < 99 ? 'bad' : 'ok'">{{ fmtPct(agg(t.id).avail) }}</div></div>
          <div class="stat"><div class="k">样本</div><div class="v">{{ sla[t.id].samples }}</div></div>
          <div class="stat"><div class="k">平均延迟</div><div class="v">{{ fmtMs(agg(t.id).avg) }}</div></div>
          <div class="stat"><div class="k">间隔</div><div class="v">{{ t.intervalSec }}s</div></div>
        </div>
        <EChart v-if="series[t.id]?.length" :option="chartOf(t.id)" :height="170" style="margin-top: 8px" />
        <div class="table-wrap" style="margin-top: 8px" v-if="sla[t.id].byNode.length">
          <table class="tbl">
            <thead><tr><th>节点</th><th>可用率</th><th>up/down</th><th>平均</th><th>P95</th><th>最新</th><th>特征</th></tr></thead>
            <tbody>
              <tr v-for="n in sla[t.id].byNode" :key="n.nodeId">
                <td><StatusDot :on="n.nodeOnline" /> {{ n.label || n.nodeId }}</td>
                <td>{{ fmtPct(n.availability) }}</td>
                <td>{{ n.up }}/{{ n.down }}<span v-if="n.invalid" class="faint"> ({{ n.invalid }} 无效)</span></td>
                <td>{{ fmtMs(n.avgMs) }}</td><td>{{ fmtMs(n.p95Ms) }}</td>
                <td><span :class="n.latestUp ? 'ok' : 'bad'">{{ n.latestUp ? 'UP' : 'DOWN' }}</span> {{ fmtMs(n.latestMs) }}</td>
                <td class="faint wrap" style="max-width: 260px">{{ special(n.special) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </template>
    </div>

    <!-- 任务表单 -->
    <Modal v-model="formOpen" width="min(640px, 94vw)" :title="form?.id ? '编辑任务' : '新建任务'">
      <div v-if="form">
        <div class="grid c2">
          <div class="field"><label>名称</label><input v-model="form.name" class="inp" /></div>
          <div class="field">
            <label>类型</label>
            <select v-model="form.apiType" class="inp"><option v-for="t in meta.types" :key="t">{{ t }}</option></select>
          </div>
          <div class="field"><label>目标</label><input v-model="form.target" class="inp" :placeholder="targetHint" /></div>
          <div class="field" v-if="form.apiType === 'dns'">
            <label>记录类型</label>
            <select v-model="form.recordType" class="inp"><option v-for="r in meta.recordTypes" :key="r">{{ r }}</option></select>
          </div>
          <div class="field" v-if="form.apiType === 'speed'">
            <label>栈</label>
            <select v-model="form.stack" class="inp"><option value="v4">IPv4</option><option value="v6">IPv6</option></select>
          </div>
          <div class="field"><label>间隔（秒，≥{{ meta.minInterval }}）</label><input v-model.number="form.intervalSec" type="number" class="inp" /></div>
          <div class="field"><label>慢阈值 ms（0 = 不启用）</label><input v-model.number="form.slowMs" type="number" class="inp" /></div>
          <div class="field"><label>期望状态码</label><input v-model="form.expectStatus" class="inp" placeholder="2xx 或 200,301" /></div>
          <div class="field"><label>免打扰时段</label><input v-model="form.quietHours" class="inp" placeholder="23:00-07:00" /></div>
          <div class="field"><label>标签（逗号分隔）</label><input v-model="form.tags" class="inp" /></div>
        </div>
        <div class="field">
          <label>节点范围</label>
          <div class="seg"><button :class="{ on: form.nodeScope === 'all' }" @click="form.nodeScope = 'all'">全部节点</button><button :class="{ on: form.nodeScope === 'custom' }" @click="form.nodeScope = 'custom'">指定节点</button></div>
          <div v-if="form.nodeScope === 'custom'" class="nodes-pick">
            <label v-for="n in nodes" :key="n.nodeId"><input type="checkbox" :value="n.nodeId" v-model="nodePick" /> {{ n.label || n.nodeId }}</label>
          </div>
        </div>
        <div class="checks">
          <label v-if="form.apiType === 'detail'"><input type="checkbox" v-model="form.bothProtocols" /> HTTP 与 HTTPS 都需命中</label>
          <label v-if="['detail', 'ssl'].includes(form.apiType)"><input type="checkbox" v-model="form.requireAllStacks" /> 双栈都可用才算可用</label>
          <label v-if="form.apiType === 'ssl'"><input type="checkbox" v-model="form.certExpiredDown" /> 证书过期视为不可用</label>
          <label><input type="checkbox" v-model="form.notifyRecover" /> 恢复时通知</label>
          <label><input type="checkbox" v-model="form.hideTarget" /> 分享页隐藏目标</label>
        </div>
        <div class="row" style="justify-content: flex-end; margin-top: 16px">
          <button class="btn" @click="formOpen = false">取消</button>
          <button class="btn primary" @click="save">保存</button>
        </div>
      </div>
    </Modal>

    <Modal v-model="shareOpen" width="min(460px, 92vw)" :title="'分享 ' + picked.length + ' 个任务到公开状态页'">
      <div>
        <div class="field"><label>分享码（留空随机；3~32 位小写字母、数字、连字符）</label><input v-model.trim="shareToken" class="inp" /></div>
        <div v-if="shareUrl" class="card soft"><a :href="shareUrl" target="_blank">{{ shareUrl }}</a></div>
        <div class="row" style="justify-content: flex-end">
          <button class="btn danger" @click="unshare">关闭分享</button>
          <button class="btn primary" @click="share">生成链接</button>
        </div>
      </div>
    </Modal>
  </div>
</template>

<script setup>
import Modal from '../components/Modal.vue'
import Switch from '../components/Switch.vue'
import StatusDot from '../components/StatusDot.vue'
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import * as api from '../api/boce.js'
import { getToken } from '../api/http.js'
import { useAuthStore } from '../stores/auth.js'
import EChart from '../components/EChart.vue'
import { fmtMs, fmtPct } from '../utils/format.js'
import { confirm, run, toastErr, toastOk } from '../utils/toast.js'

const auth = useAuthStore()
const hours = ref(24)
const tasks = ref([])
const sla = ref({})
const series = ref({})
const meta = ref({ types: [], recordTypes: [], minInterval: 10 })
const nodes = ref([])
const formOpen = ref(false)
const form = ref(null)
const nodePick = ref([])
const picked = ref([])
const shareOpen = ref(false)
const shareToken = ref('')
const shareUrl = ref('')
const live = ref(false)

const targetHint = computed(() => ({ detail: 'https://example.com', ssl: 'example.com', dns: 'example.com', tcping: 'example.com:443', speed: 'https://example.com/file' }[form.value?.apiType]))

function agg(id) {
  const s = sla.value[id]
  let up = 0, down = 0, w = 0, lat = 0
  for (const n of s?.byNode || []) {
    up += n.up
    down += n.down
    if (n.up) (w += n.up), (lat += n.avgMs * n.up)
  }
  return { avail: up + down ? (up / (up + down)) * 100 : null, avg: w ? lat / w : 0 }
}
function special(s) {
  if (!s) return ''
  return Object.entries(s).slice(0, 4).map(([k, v]) => `${k}=${typeof v === 'object' ? JSON.stringify(v) : v}`).join('  ')
}
function chartOf(id) {
  const pts = series.value[id] || []
  return {
    tooltip: { trigger: 'axis' },
    grid: { left: 44, right: 16, top: 10, bottom: 24 },
    xAxis: { type: 'time' },
    yAxis: { type: 'value', name: 'ms' },
    series: [{
      type: 'line', smooth: true, showSymbol: false, areaStyle: { opacity: 0.1 },
      data: pts.map((p) => [p.time, p.avgMs]),
      markArea: { itemStyle: { color: 'rgba(239,68,68,0.12)' }, data: pts.filter((p) => p.down > 0).map((p) => [{ xAxis: p.time }, { xAxis: p.time }]) },
    }],
  }
}

async function loadTask(t) {
  const [s, se] = await Promise.all([api.fetchTaskSla(t.id, hours.value), api.fetchTaskSeries(t.id, hours.value)])
  sla.value[t.id] = s
  series.value[t.id] = se.series
}
async function load() {
  tasks.value = await api.fetchTasks()
  // shareToken 不在任务 JSON 里，单独查
  await Promise.allSettled(tasks.value.map(async (t) => {
    await loadTask(t)
    const sh = await api.fetchTaskShare(t.id)
    t.shareToken = sh.enabled ? sh.token : ''
  }))
}

function edit(t) {
  form.value = t
    ? { ...t }
    : { name: '', apiType: 'detail', target: '', recordType: 'a', stack: 'v4', intervalSec: 60, slowMs: 0, expectStatus: '2xx', nodeScope: 'all', nodeIds: '', bothProtocols: false, requireAllStacks: false, certExpiredDown: false, notifyRecover: true, quietHours: '', hideTarget: false, tags: '' }
  nodePick.value = (form.value.nodeIds || '').split(',').filter(Boolean)
  formOpen.value = true
}
async function save() {
  const f = { ...form.value, nodeIds: nodePick.value.join(',') }
  if (f.id) await run(() => api.updateTask(f.id, f), '已保存')
  else await run(() => api.createTask({ ...f, enabled: true }), '已创建')
  formOpen.value = false
  load()
}
async function toggle(t, v) {
  await run(() => api.setTaskEnabled(t.id, v))
  t.enabled = v
}
async function del(t) {
  if (!(await confirm(`删除任务「${t.name}」及其全部历史样本？`))) return
  await run(() => api.deleteTask(t.id), '已删除')
  load()
}
async function share() {
  const r = await run(() => api.shareTasks(picked.value, shareToken.value))
  shareUrl.value = location.origin + r.url
  load()
}
async function unshare() {
  await run(() => api.unshareTasks(picked.value), '已关闭分享')
  shareUrl.value = ''
  load()
}

// 实时推送：/console/sla?token=&hours=
let ws
function connect() {
  ws?.close()
  const proto = location.protocol === 'https:' ? 'wss' : 'ws'
  ws = new WebSocket(`${proto}://${location.host}/console/sla?hours=${hours.value}&token=${encodeURIComponent(getToken())}`)
  ws.onopen = () => (live.value = true)
  ws.onclose = () => (live.value = false)
  ws.onmessage = (e) => {
    try {
      const m = JSON.parse(e.data)
      if (m.type === 'sla' && m.data) {
        sla.value[m.data.taskId] = m.data.sla
        const t = tasks.value.find((x) => x.id === m.data.taskId)
        if (t) api.fetchTaskSeries(t.id, hours.value).then((se) => (series.value[t.id] = se.series))
      }
    } catch {}
  }
}
watch(hours, () => {
  load()
  connect()
})
onMounted(async () => {
  meta.value = await api.fetchTaskMeta()
  nodes.value = await api.fetchNodesBrief().catch(() => [])
  load()
  connect()
})
onUnmounted(() => ws?.close())
watch(shareOpen, (v) => v && (shareUrl.value = ''))
</script>

<style scoped>
.task .ellipsis { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; max-width: 320px; }
.nodes-pick { display: flex; flex-wrap: wrap; gap: 6px 14px; margin-top: 8px; font-size: 13px; }
.checks { display: flex; flex-direction: column; gap: 6px; font-size: 14px; }
</style>
