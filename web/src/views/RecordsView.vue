<template>
  <div class="page wide">
    <div class="page-head">
      <div>
        <h1>拨测明细</h1>
        <div class="sub">{{ auth.isAdmin ? '全部拨测记录' : '我的定时任务样本与我发起的一键拨测' }}</div>
      </div>
    </div>
    <div class="row" style="margin-bottom: 12px">
      <div class="seg">
        <button v-for="c in CATS" :key="c.v" :class="{ on: f.cat === c.v }" @click="(f.cat = c.v), load()">{{ c.label }}</button>
      </div>
      <select v-model="f.type" class="inp" style="width: 120px" @change="load">
        <option value="">全部类型</option>
        <option v-for="t in ['detail', 'ssl', 'dns', 'tcping', 'speed', 'location']" :key="t">{{ t }}</option>
      </select>
      <input v-model.trim="f.target" class="inp" placeholder="目标包含…" style="width: 200px" @keydown.enter="load" />
      <input v-model.trim="f.node" class="inp" placeholder="节点 ID" style="width: 150px" @keydown.enter="load" />
      <button class="btn" @click="load">查询</button>
      <button class="btn" @click="api.download(`/admin/probes/export${api.qs(f)}`).catch(toastErr)">导出 CSV</button>
    </div>
    <div class="table-wrap">
      <table class="tbl">
        <thead><tr><th>时间</th><th>节点</th><th>类型</th><th>目标</th><th>状态</th><th>延迟</th><th>来源</th><th /></tr></thead>
        <tbody>
          <tr v-for="r in rows" :key="r.createdAt + r.nodeId + r.raw">
            <td class="faint">{{ fmtTime(r.createdAt) }}</td>
            <td>{{ labels[r.nodeId] || r.nodeId }}</td>
            <td>{{ r.apiType }}</td>
            <td class="mono wrap" style="max-width: 280px">{{ r.raw }}</td>
            <td><span class="pill" :class="r.error ? 'bad' : r.status >= 200 && r.status < 300 ? 'ok' : 'warn'">{{ r.error ? 'ERR' : r.status }}</span></td>
            <td>{{ fmtMs(r.latencyMs) }}</td>
            <td>{{ SRC[r.source] || r.source }}</td>
            <td><button class="btn sm" @click="(cur = r), (open = true)">详情</button></td>
          </tr>
          <tr v-if="!rows.length"><td colspan="8" class="empty">暂无记录</td></tr>
        </tbody>
      </table>
    </div>
    <Modal v-model="open" width="min(720px, 94vw)" :title="cur ? cur.apiType + ' · ' + cur.raw : ''">
      <div v-if="cur">
        <p class="faint">{{ labels[cur.nodeId] || cur.nodeId }} · {{ fmtTime(cur.createdAt) }}</p>
        <pre class="json">{{ cur.error || prettyJSON(cur.body) || '（无 body）' }}</pre>
      </div>
    </Modal>
  </div>
</template>

<script setup>
import Modal from '../components/Modal.vue'
import { onMounted, reactive, ref } from 'vue'
import * as api from '../api/boce.js'
import { useAuthStore } from '../stores/auth.js'
import { fmtMs, fmtTime, prettyJSON } from '../utils/format.js'
import { toastErr } from '../utils/toast.js'

const CATS = [
  { v: '', label: '全部' },
  { v: 'sched', label: '定时拨测' },
  { v: 'biz', label: '业务拨测' },
]
const SRC = { sched: '定时', biz: '一键', ws: '节点上报', http: '节点上报' }
const auth = useAuthStore()
const f = reactive({ cat: '', type: '', target: '', node: '' })
const rows = ref([])
const labels = ref({})
const open = ref(false)
const cur = ref(null)

async function load() {
  rows.value = await api.fetchProbes({ ...f, limit: 200 })
}
onMounted(async () => {
  load()
  try {
    for (const n of await api.fetchNodesBrief()) labels.value[n.nodeId] = n.label
  } catch {}
})
</script>
