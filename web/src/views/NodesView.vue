<template>
  <div class="page wide">
    <div class="page-head">
      <div>
        <h1>节点管理</h1>
        <div class="sub">节点池保存即生效；WS 节点连 {{ status?.wsEnabled ? '8092/ws' : '（WS 已关闭）' }}，当前在线 {{ status?.wsPeers ?? 0 }}</div>
      </div>
      <div class="row">
        <div class="seg">
          <button v-for="t in TABS" :key="t.v" :class="{ on: tab === t.v }" @click="tab = t.v">{{ t.label }}</button>
        </div>
        <button v-if="tab === 'pool'" class="btn primary" @click="edit()">+ 添加节点</button>
      </div>
    </div>

    <template v-if="tab === 'pool'">
      <div v-if="unconfigured.length" class="card soft">
        <b>发现 {{ unconfigured.length }} 个已接入但未登记的节点：</b>
        <span v-for="u in unconfigured" :key="u.nodeId" class="btn sm" style="margin: 4px" @click="edit(null, u)">{{ u.nodeId }} +</span>
      </div>
      <div class="table-wrap">
        <table class="tbl">
          <thead><tr><th>节点 ID</th><th>名称</th><th>通道</th><th>池</th><th>栈</th><th>凭据</th><th>启用</th><th /></tr></thead>
          <tbody>
            <tr v-for="d in defs" :key="d.id">
              <td class="mono">{{ d.nodeId }}</td>
            <td>{{ d.label }}<span v-if="d.donationId" class="pill ok" style="margin-left: 4px">捐赠·{{ NET_TYPE[d.netType] }}</span></td>
              <td>{{ d.ws ? 'WS' : 'HTTP' }}<div v-if="!d.ws" class="faint">{{ d.url }}</div></td>
              <td>{{ d.pool }}</td><td>{{ d.stack || '—' }}</td>
              <td class="faint">{{ d.hasApiKey ? 'APIKey' : '' }} {{ d.hasWsKey ? 'WSKey' : '' }}</td>
              <td><Switch :model-value="d.enabled" @change="(v) => quick(d, v)" /></td>
              <td><button class="btn sm" @click="edit(d)">编辑</button><button class="btn sm danger" @click="del(d)">删除</button></td>
            </tr>
            <tr v-if="!defs.length"><td colspan="8" class="empty">节点池为空（未登记时回退 setting.json）</td></tr>
          </tbody>
        </table>
      </div>
    </template>

    <NodeOpsPanels v-if="['uptime', 'ota', 'runtime', 'maint'].includes(tab)" :tab="tab" :live="live" />

    <div v-if="tab === 'live'" class="table-wrap">
      <table class="tbl">
        <thead><tr><th>节点</th><th>状态</th><th>来源地址</th><th>版本</th><th>能力</th><th>最后在线</th><th /></tr></thead>
        <tbody>
          <tr v-for="n in live" :key="n.nodeId">
            <td>{{ n.label || n.nodeId }}<div class="faint mono">{{ n.nodeId }}</div></td>
            <td><span class="pill" :class="n.online ? 'ok' : 'bad'">{{ n.online ? '在线' : '离线' }}</span></td>
            <td class="mono">{{ n.remoteAddr || '—' }}</td><td>{{ n.version || '—' }}</td>
            <td class="faint">{{ n.capabilities || '未知' }}</td><td class="faint">{{ fmtTime(n.lastSeenAt) }}</td>
            <td><button class="btn sm" @click="showEvents(n)">事件</button><button class="btn sm danger" @click="delSnap(n)">移除</button></td>
          </tr>
        </tbody>
      </table>
    </div>

    <Modal v-model="formOpen" width="min(560px, 94vw)" :title="form?.id ? '编辑节点' : '添加节点'">
      <div v-if="form">
        <div class="grid c2">
          <div class="field"><label>节点 ID（即转发路径 backendID）</label><input v-model.trim="form.nodeId" class="inp" /></div>
          <div class="field"><label>显示名</label><input v-model="form.label" class="inp" placeholder="中国 江苏 移动" /></div>
          <div class="field"><label>通道</label><select v-model="form.ws" class="inp"><option :value="false">HTTP</option><option :value="true">WS</option></select></div>
          <div class="field" v-if="!form.ws"><label>HTTP 上游地址</label><input v-model.trim="form.url" class="inp" placeholder="https://node.example.com/" /></div>
          <div class="field"><label>栈分组（api 池）</label><select v-model="form.stack" class="inp"><option>DualStack</option><option>IPv4</option><option>IPv6</option></select></div>
          <div class="field"><label>排序</label><input v-model.number="form.sortOrder" type="number" class="inp" /></div>
        </div>
        <div class="field">
          <label>归属池</label>
          <div class="row"><label><input type="checkbox" value="api" v-model="form.pools" /> api（whois/dns/ssl/detail/tcping/speed）</label><label><input type="checkbox" value="location" v-model="form.pools" /> location（location/asn）</label></div>
        </div>
        <div class="grid c2">
          <div class="field"><label>API Key（中心 → 节点，{{ form.hasApiKey ? '已设置，留空不改' : '可选' }}）</label><input v-model="form.apiKey" class="inp" type="password" /></div>
          <div class="field"><label>WS Key（节点注册校验，{{ form.hasWsKey ? '已设置，留空不改' : '可选' }}）</label><input v-model="form.wsKey" class="inp" type="password" /></div>
        </div>
        <div class="row"><Switch v-model="form.enabled" /> 启用</div>
        <div class="row" style="justify-content: flex-end; margin-top: 14px">
          <button class="btn" @click="formOpen = false">取消</button><button class="btn primary" @click="save">保存</button>
        </div>
      </div>
    </Modal>

    <Modal v-model="evOpen" width="min(520px, 92vw)" title="上下线事件">
      <div>
        <div v-for="(e, i) in events" :key="i" class="row between" style="border-bottom: 1px solid var(--border); padding: 6px 0">
          <span :class="e.event === 'online' ? 'ok' : 'bad'">{{ e.event }}</span><span class="faint">{{ e.reason }}</span><span class="faint">{{ fmtTime(e.createdAt) }}</span>
        </div>
        <div v-if="!events.length" class="empty">无</div>
      </div>
    </Modal>
  </div>
</template>

<script setup>
import Modal from '../components/Modal.vue'
import Switch from '../components/Switch.vue'
import { onMounted, ref, watch } from 'vue'
import * as api from '../api/boce.js'
import NodeOpsPanels from '../components/NodeOpsPanels.vue'
import { fmtTime, NET_TYPE } from '../utils/format.js'
import { confirm, run } from '../utils/toast.js'

const TABS = [
  { v: 'pool', label: '节点池' },
  { v: 'live', label: '在线状态' },
  { v: 'uptime', label: '可用率' },
  { v: 'ota', label: 'OTA' },
  { v: 'runtime', label: '运行时配置' },
  { v: 'maint', label: '维护窗口' },
]
const tab = ref('pool')
const defs = ref([])
const live = ref([])
const unconfigured = ref([])
const status = ref(null)
const formOpen = ref(false)
const form = ref(null)
const evOpen = ref(false)
const events = ref([])

async function load() {
  ;[defs.value, unconfigured.value, status.value] = await Promise.all([api.fetchNodeDefs(), api.fetchUnconfigured(), api.fetchStatus()])
}
function edit(d, cand) {
  form.value = d
    ? { ...d, pools: d.pool.split(','), apiKey: '', wsKey: '' }
    : { nodeId: cand?.nodeId || '', label: cand?.label || '', ws: !!cand?.viaWs, url: '', stack: 'DualStack', sortOrder: 0, pools: ['api'], apiKey: '', wsKey: '', enabled: true }
  formOpen.value = true
}
async function save() {
  const f = { ...form.value }
  const body = { nodeId: f.nodeId, label: f.label, ws: f.ws, url: f.url, stack: f.stack, sortOrder: f.sortOrder, pools: f.pools, enabled: f.enabled }
  if (f.apiKey) body.apiKey = f.apiKey
  if (f.wsKey) body.wsKey = f.wsKey
  await run(() => (f.id ? api.updateNodeDef(f.id, body) : api.createNodeDef(body)), '已保存')
  formOpen.value = false
  load()
}
async function quick(d, v) {
  await run(() => api.updateNodeDef(d.id, { enabled: v }))
  d.enabled = v
}
async function del(d) {
  if (!(await confirm(`从节点池删除 ${d.nodeId}？`))) return
  await run(() => api.deleteNodeDef(d.id), '已删除')
  load()
}
async function showEvents(n) {
  events.value = await api.fetchNodeEvents(n.nodeId)
  evOpen.value = true
}
async function delSnap(n) {
  if (!(await confirm(`移除 ${n.nodeId} 的在线快照？（事件历史保留）`))) return
  await run(() => api.deleteNodeSnapshot(n.nodeId), '已移除')
  live.value = await api.fetchNodes()
}
watch(tab, async (t) => t !== 'pool' && (live.value = await api.fetchNodes()))
onMounted(load)
</script>

