<template>
  <div>
    <!-- 类别页签：定时拨测(source=sched) / 业务拨测(节点上报 ws|http + 手动一键 biz) -->
    <div class="seg">
      <button
        v-for="c in cats" :key="c.value"
        class="seg-item" :class="{ active: cat === c.value }"
        @click="switchCat(c.value)"
      >{{ c.label }}</button>
    </div>

    <div class="toolbar">
      <div class="form-field">
        <label>节点</label>
        <select class="ak-select" v-model="f.node">
          <option value="">全部</option>
          <option v-for="n in knownNodes" :key="n" :value="n">{{ n }}</option>
        </select>
      </div>
      <div class="form-field">
        <label>拨测方案</label>
        <select class="ak-select" v-model="f.type">
          <option value="">全部</option>
          <option v-for="t in typeOpts" :key="t.value" :value="t.value">{{ t.label }}</option>
        </select>
      </div>
      <div class="form-field">
        <label>条数</label>
        <select class="ak-select" v-model="f.limit" @change="load">
          <option :value="50">50</option>
          <option :value="100">100</option>
          <option :value="200">200</option>
        </select>
      </div>
      <button class="ak-button ak-button--outline" @click="load">查询</button>
    </div>
    <div v-if="loading" class="loading-center"><span class="ak-loading"></span></div>

    <div class="panel">
      <div class="ak-table-wrap">
        <table class="ak-table">
          <thead>
            <tr>
              <th>时间</th><th>节点</th><th>拨测方案</th><th>方式</th><th>目标</th><th>状态</th><th>延迟</th><th>来源</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="p in probes" :key="p.id">
              <td class="mono nowrap">{{ fmtTime(p.createdAt) }}</td>
              <td class="mono nowrap">{{ p.nodeId }}</td>
              <td><span class="ak-tag ch">{{ apiLabel(p.apiType) }}</span></td>
              <td>
                <span v-if="parseRow(p).kind" class="ak-tag ch kind">{{ parseRow(p).kind }}</span>
              </td>
              <td class="mono" style="word-break:break-all;max-width:360px">{{ parseRow(p).target }}</td>
              <td><span class="mono" :class="p.status >= 200 && p.status < 300 ? 'ok-200' : (p.error ? 'err' : 'dim')">{{ p.status || '—' }}</span></td>
              <td class="mono nowrap">{{ p.latencyMs }}ms</td>
              <td><span class="ak-tag ch" :class="srcClass(p.source)">{{ srcLabel(p.source) }}</span></td>
            </tr>
            <tr v-if="!probes.length && !loading"><td colspan="8" class="dim">暂无{{ curLabel }}明细</td></tr>
          </tbody>
        </table>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, reactive, computed, onMounted } from 'vue'
import { fetchProbes, fetchNodes } from '../api/boce.js'
import { fmtTime } from '../utils/format.js'
import { apiOptions, apiLabel, parseProbeRaw } from '../utils/probeMeta.js'

const cats = [
  { value: 'sched', label: '定时拨测' },
  { value: 'biz', label: '业务拨测' },
]
// 拨测方案下拉：value 是后端 apiType slug，label 是面向用户的中文方案名（见 utils/probeMeta.js）
const typeOpts = apiOptions
const cat = ref('sched') // 当前页签
const f = reactive({ node: '', type: '', limit: 100 })
const probes = ref([])
const knownNodes = ref([])
const loading = ref(false)

const curLabel = computed(() => cats.find((c) => c.value === cat.value)?.label || '')

// 解析每条样本的 raw → { kind, target }（speed 拆 v4/v6，dns 拆记录类型）
// 缓存解析结果（同一 raw 重复出现时省一次正则）
const parsedCache = new Map()
function parseRow(p) {
  const key = `${p.apiType}::${p.raw}`
  let r = parsedCache.get(key)
  if (!r) {
    r = parseProbeRaw(p.apiType, p.raw)
    parsedCache.set(key, r)
  }
  return r
}

// source → 可读来源标签
const srcLabelMap = {
  sched: '定时拨测',
  biz: '手动一键',
  ws: '节点上报',
  http: '节点上报',
}
const srcLabel = (s) => srcLabelMap[s] || s || '—'
const srcClass = (s) => {
  if (s === 'sched') return 'ak-tag--advanced'
  if (s === 'ws' || s === 'http') return 'ak-tag--advanced'
  return 'ak-tag--neutral'
}

onMounted(async () => {
  load()
  try { knownNodes.value = (await fetchNodes()).map((n) => n.nodeId) } catch { /* 忽略 */ }
})

function switchCat(v) {
  if (cat.value === v) return
  cat.value = v
  f.node = ''
  f.type = ''
  load()
}

async function load() {
  loading.value = true
  try {
    probes.value = await fetchProbes({ node: f.node, type: f.type, cat: cat.value, limit: f.limit })
  } catch (e) {
    probes.value = []
  } finally {
    loading.value = false
  }
}
</script>

<style scoped>
/* 类别页签（定时拨测 / 业务拨测） */
.seg {
  display: inline-flex;
  background: var(--ak-surface-raised);
  border: var(--ak-line-hairline) solid rgba(0, 0, 0, 0.1);
  padding: 3px;
  gap: 3px;
  margin-bottom: 16px;
  border-radius: 6px;
}
.seg-item {
  border: none;
  background: transparent;
  color: var(--ak-text-secondary);
  font-size: 0.82rem;
  letter-spacing: var(--ak-type-wide);
  padding: 6px 16px;
  border-radius: 4px;
  cursor: pointer;
  transition: background 0.15s ease, color 0.15s ease;
}
.seg-item:hover { color: var(--ak-text-primary); }
.seg-item.active {
  background: var(--ak-signal-info);
  color: #fff;
  font-weight: 600;
}
.ak-tag.ch.kind {
  background: rgba(255, 255, 255, 0.06);
  border-color: rgba(255, 255, 255, 0.18);
  color: var(--ak-text-secondary);
  font-size: 0.72rem;
}
</style>
