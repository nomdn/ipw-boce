<template>
  <!-- 可用率 -->
  <template v-if="tab === 'uptime'">
    <div class="row" style="margin-bottom: 10px">
      <div class="seg"><button v-for="d in [1, 7, 30]" :key="d" :class="{ on: days === d }" @click="(days = d), loadUptime()">{{ d }} 天</button></div>
      <button class="btn" @click="exp(`/admin/nodes/uptime/export${api.qs({ days })}`)">导出 CSV</button>
    </div>
    <div class="table-wrap">
      <table class="tbl">
        <thead><tr><th>节点</th><th>当前</th><th>可用率</th><th>在线</th><th>离线</th><th>掉线次数</th><th /></tr></thead>
        <tbody>
          <tr v-for="n in uptime" :key="n.nodeId">
            <td>{{ n.label }}<div class="faint mono">{{ n.nodeId }}</div></td>
            <td><StatusDot :on="n.currentOnline" /></td>
            <td><b :class="n.availability < 0.99 ? 'bad' : 'ok'">{{ fmtPct(n.availability * 100) }}</b>
              <div class="bar"><span :style="{ width: n.availability * 100 + '%' }" /></div></td>
            <td>{{ fmtDuration(n.onlineSeconds) }}</td><td>{{ fmtDuration(n.offlineSeconds) }}</td><td>{{ n.downCount }}</td>
            <td><button class="btn sm" @click="openDetail(n)">逐日</button>
              <button class="btn sm" @click="exp(`/admin/nodes/${encodeURIComponent(n.nodeId)}/events/export?days=${days}`)">事件 CSV</button></td>
          </tr>
        </tbody>
      </table>
    </div>
    <Modal v-model="detailOpen" width="min(720px, 94vw)" :title="(detail?.label || '') + ' · 逐日可用率'">
      <div v-if="detail">
        <EChart :option="dailyOption" :height="240" />
      </div>
    </Modal>
  </template>

  <!-- OTA -->
  <template v-if="tab === 'ota'">
    <div class="card">
      <h3>下发升级</h3>
      <div class="grid c3">
        <div class="field"><label>节点</label>
          <select v-model="ota.nodeId" class="inp"><option v-for="n in live" :key="n.nodeId" :value="n.nodeId">{{ n.label || n.nodeId }}{{ n.online ? '' : '（离线）' }} {{ n.version ? 'v' + n.version : '' }}</option></select></div>
        <div class="field"><label>目标版本（二选一）</label><input v-model.trim="ota.version" class="inp" placeholder="v1.2.3" /></div>
        <div class="field"><label>或下载地址</label><input v-model.trim="ota.url" class="inp" placeholder="https://.../ipw-cn-linux-amd64" /></div>
      </div>
      <div class="field"><label>SHA256（选填）</label><input v-model.trim="ota.sha256" class="inp mono" /></div>
      <button class="btn primary" :disabled="!ota.nodeId || (!ota.version && !ota.url)" @click="sendOta">下发</button>
      <span class="faint" style="margin-left: 8px">结果以节点重连后上报的版本号为准，最长等待 15 分钟</span>
    </div>
    <div class="table-wrap">
      <table class="tbl">
        <thead><tr><th>#</th><th>节点</th><th>版本</th><th>通道</th><th>状态</th><th>阶段</th><th>操作人</th><th>下发时间</th></tr></thead>
        <tbody>
          <tr v-for="t in otaTasks" :key="t.id">
            <td>{{ t.id }}</td><td>{{ t.label }}</td><td class="mono">{{ t.fromVersion || '?' }} → {{ t.targetVersion || 'url' }}</td>
            <td>{{ t.channel }}</td>
            <td><span class="pill" :class="{ success: 'ok', failed: 'bad', dispatched: 'warn' }[t.status]">{{ { success: '成功', failed: '失败', dispatched: '进行中' }[t.status] }}</span>
              <div v-if="t.error" class="faint bad" style="white-space: normal; max-width: 260px">{{ t.error }}</div></td>
            <td>{{ t.stage }}</td><td class="faint">{{ t.dispatchedBy }}</td><td class="faint">{{ fmtTime(t.dispatchedAt) }}</td>
          </tr>
          <tr v-if="!otaTasks.length"><td colspan="8" class="empty">暂无任务</td></tr>
        </tbody>
      </table>
    </div>
  </template>

  <!-- 运行时配置 -->
  <template v-if="tab === 'runtime'">
    <div class="card">
      <div class="row">
        <select v-model="rc.nodeId" class="inp" style="width: 260px"><option v-for="n in live" :key="n.nodeId" :value="n.nodeId">{{ n.label || n.nodeId }}{{ n.online ? '' : '（离线）' }}</option></select>
        <button class="btn" :disabled="!rc.nodeId" @click="loadRc">读取当前配置</button>
        <button class="btn" :disabled="!rc.nodeId" @click="refreshRc">让节点重新拉取远端配置</button>
      </div>
      <template v-if="rc.res">
        <p class="faint">通道 {{ rc.res.channel }} · 凭据类字段（{{ rc.res.secretKeys.join(', ') }}）不可下发 · 需重启生效：{{ (rc.res.restartKeys || []).join(', ') || '—' }}</p>
        <div class="field"><label>编辑后下发（只会提交改动过的键）</label><textarea v-model="rc.text" class="inp" style="min-height: 280px" /></div>
        <div class="row">
          <label><input type="checkbox" v-model="rc.persist" /> 同时写回节点 setting.json</label>
          <button class="btn primary" @click="patchRc">下发改动</button>
        </div>
        <div v-if="rc.last" class="card soft" style="margin-top: 12px">
          <div>已应用：{{ (rc.last.applied || []).join(', ') || '—' }}；未知键：{{ (rc.last.unknown || []).join(', ') || '—' }}；需重启：{{ (rc.last.restartRequired || []).join(', ') || '—' }}</div>
          <div v-if="rc.last.persistHint" class="bad" style="margin-top: 6px; font-size: 13px">{{ rc.last.persistHint }}（{{ rc.last.unpersistedKeys.join(', ') }}）
            <button class="btn sm" @click="mergeRc">合并进托管配置</button></div>
        </div>
      </template>
    </div>
  </template>

  <!-- 维护窗口 -->
  <template v-if="tab === 'maint'">
    <div class="card">
      <h3>新建维护窗口</h3>
      <p class="faint">窗口内节点上下线不发通知（事件照记）；捐赠节点在窗口内的离线不影响每日在线奖励。</p>
      <div class="grid c3">
        <div class="field"><label>范围</label>
          <select v-model="mw.scope" class="inp"><option value="global">全部节点</option><option v-for="n in live" :key="n.nodeId" :value="n.nodeId">{{ n.label || n.nodeId }}</option></select></div>
        <div class="field"><label>类型</label>
          <select v-model="mw.repeatDaily" class="inp"><option :value="false">一次性</option><option :value="true">每日重复</option></select></div>
        <div class="field"><label>原因</label><input v-model="mw.reason" class="inp" placeholder="机房割接 / 例行重启" /></div>
        <div class="field"><label>开始</label><input v-model="mw.startAt" :type="mw.repeatDaily ? 'time' : 'datetime-local'" class="inp" /></div>
        <div class="field"><label>结束</label><input v-model="mw.endAt" :type="mw.repeatDaily ? 'time' : 'datetime-local'" class="inp" /></div>
      </div>
      <button class="btn primary" :disabled="!mw.startAt || !mw.endAt" @click="addMw">添加</button>
    </div>
    <div class="table-wrap">
      <table class="tbl">
        <thead><tr><th>范围</th><th>时间</th><th>原因</th><th>状态</th><th>创建人</th><th /></tr></thead>
        <tbody>
          <tr v-for="w in windows" :key="w.id">
            <td>{{ w.scope === 'global' ? '全部节点' : w.scope }}</td>
            <td>{{ w.repeatDaily ? `每日 ${hm(w.startAt)}-${hm(w.endAt)}` : `${fmtTime(w.startAt)} ~ ${fmtTime(w.endAt)}` }}</td>
            <td>{{ w.reason || '—' }}</td>
            <td><span class="pill" :class="w.active ? 'warn' : w.expired ? '' : 'ok'">{{ w.active ? '生效中' : w.expired ? '已结束' : '待生效' }}</span></td>
            <td class="faint">{{ w.createdBy }}</td>
            <td><button class="btn sm danger" @click="delMw(w)">删除</button></td>
          </tr>
          <tr v-if="!windows.length"><td colspan="6" class="empty">没有维护窗口</td></tr>
        </tbody>
      </table>
    </div>
  </template>
</template>

<script setup>
import Modal from './Modal.vue'
import StatusDot from './StatusDot.vue'
import { computed, reactive, ref, watch } from 'vue'
import * as api from '../api/boce.js'
import EChart from './EChart.vue'
import { fmtDuration, fmtPct, fmtTime } from '../utils/format.js'
import { confirm, run, toastErr, toastOk } from '../utils/toast.js'

const props = defineProps({ tab: String, live: Array })

const days = ref(7)
const uptime = ref([])
const detailOpen = ref(false)
const detail = ref(null)
const otaTasks = ref([])
const ota = reactive({ nodeId: '', version: '', url: '', sha256: '' })
const rc = reactive({ nodeId: '', res: null, text: '', orig: {}, persist: false, last: null })
const windows = ref([])
const mw = reactive({ scope: 'global', repeatDaily: false, startAt: '', endAt: '', reason: '' })

const hm = (t) => new Date(t).toTimeString().slice(0, 5)
const exp = (p) => api.download(p).catch(toastErr)

async function loadUptime() {
  uptime.value = (await api.fetchNodesUptime(days.value)).nodes
}
async function openDetail(n) {
  detail.value = await api.fetchNodeUptime(n.nodeId, Math.max(days.value, 7))
  detailOpen.value = true
}
const dailyOption = computed(() => ({
  tooltip: { trigger: 'axis' },
  grid: { left: 44, right: 16, top: 16, bottom: 28 },
  xAxis: { type: 'category', data: (detail.value?.daily || []).map((d) => d.date.slice(5)) },
  yAxis: { type: 'value', min: 0, max: 100 },
  series: [{ type: 'bar', data: (detail.value?.daily || []).map((d) => +(d.availability * 100).toFixed(2)) }],
}))

async function loadOta() {
  otaTasks.value = await api.fetchOtaTasks()
}
async function sendOta() {
  const r = await run(() => api.dispatchOta(ota.nodeId, { version: ota.version, url: ota.url, sha256: ota.sha256 }))
  toastOk(r.hint)
  loadOta()
}

async function loadRc() {
  rc.res = await run(() => api.fetchNodeRuntimeConfig(rc.nodeId))
  rc.orig = rc.res.config || {}
  rc.text = JSON.stringify(rc.orig, null, 2)
  rc.last = null
}
async function refreshRc() {
  await run(() => api.applyNodeRuntimeConfig(rc.nodeId, { action: 'refresh' }), '节点已重新拉取远端配置')
  loadRc()
}
function changed() {
  const now = JSON.parse(rc.text)
  const diff = {}
  for (const [k, v] of Object.entries(now)) if (JSON.stringify(v) !== JSON.stringify(rc.orig[k])) diff[k] = v
  return diff
}
async function patchRc() {
  let diff
  try {
    diff = changed()
  } catch (e) {
    return toastErr(new Error('JSON 格式错误：' + e.message))
  }
  if (!Object.keys(diff).length) return toastErr(new Error('没有改动'))
  rc.last = await run(() => api.applyNodeRuntimeConfig(rc.nodeId, { action: 'patch', config: diff, persist: rc.persist }), '已下发')
  rc.last.diff = diff
}
async function mergeRc() {
  const d = {}
  for (const k of rc.last.unpersistedKeys) d[k] = rc.last.diff[k]
  await run(() => api.mergeHostedConfig(rc.nodeId, d), '已合并进托管配置')
  rc.last.persistHint = ''
}

async function loadMw() {
  windows.value = await api.fetchMaintenance()
}
async function addMw() {
  const w = { ...mw }
  if (!w.repeatDaily) {
    w.startAt = new Date(w.startAt).toISOString()
    w.endAt = new Date(w.endAt).toISOString()
  }
  await run(() => api.createMaintenance(w), '已添加')
  loadMw()
}
async function delMw(w) {
  if (!(await confirm('删除该维护窗口？'))) return
  await run(() => api.deleteMaintenance(w.id), '已删除')
  loadMw()
}

watch(
  () => props.tab,
  (t) => {
    if (t === 'uptime') loadUptime()
    if (t === 'ota') loadOta()
    if (t === 'maint') loadMw()
  },
  { immediate: true },
)
</script>

<style scoped>
.bar { height: 4px; background: var(--bg-hover); border-radius: 2px; margin-top: 4px; width: 120px; }
.bar span { display: block; height: 100%; background: var(--text); border-radius: 2px; transition: width 0.6s var(--ease); }
</style>
