<template>
  <div>
    <!-- 类别页签：定时拨测(source=sched) / 业务拨测(节点上报 ws|http + 一键拨测 biz) -->
    <div class="seg">
      <button
        v-for="c in cats" :key="c.value"
        class="seg-item" :class="{ active: cat === c.value }"
        @click="switchCat(c.value)"
      >{{ c.label }}</button>
    </div>

    <div class="toolbar">
      <div class="tb-group">
        <div class="form-field">
          <label>时间范围</label>
          <select class="ak-select" v-model="f.range" @change="load">
            <option v-for="r in rangeOpts" :key="r.value" :value="r.value">{{ r.label }}</option>
          </select>
        </div>
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
        <!-- 目标模糊匹配：匹配节点侧 raw（含 v4/、dns 记录类型前缀），输入即按子串过滤 -->
        <div class="form-field">
          <label>目标</label>
          <input class="ak-input" v-model.trim="f.target" placeholder="域名 / IP / URL 片段"
            style="width:190px" @keyup.enter="load" />
        </div>
        <div class="form-field">
          <label>条数</label>
          <select class="ak-select" v-model="f.limit" @change="load">
            <option :value="50">50</option>
            <option :value="100">100</option>
            <option :value="200">200</option>
          </select>
        </div>
      </div>
      <div class="tb-group tb-group--actions">
        <button class="ak-button ak-button--outline" @click="load">查询</button>
        <button class="ak-button ak-button--outline" :disabled="exporting" @click="doExport">
          {{ exporting ? '导出中…' : '导出 CSV' }}
        </button>
      </div>
    </div>
    <div v-if="loading" class="loading-center"><span class="ak-loading"></span></div>
    <div v-else-if="error" class="dim" style="color:var(--ak-signal-danger);margin:6px 0 10px">{{ error }}</div>

    <div class="panel">
      <div class="ak-table-wrap">
        <table class="ak-table">
          <thead>
            <tr>
              <th>时间</th><th>节点</th><th>拨测方案</th><th>方式</th><th>目标</th><th>状态</th><th>延迟</th><th>来源</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="(p, i) in probes" :key="rowKey(p, i)">
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
import { fetchProbes, fetchNodesBrief, exportProbes } from '../api/boce.js'
import { fmtTime } from '../utils/format.js'
import { apiOptions, probeHistoryTypes, apiLabel, parseProbeRaw } from '../utils/probeMeta.js'

const cats = [
  { value: 'sched', label: '定时拨测' },
  { value: 'biz', label: '业务拨测' },
]
const cat = ref('sched') // 当前页签
// 拨测方案下拉：value 是后端 apiType slug，label 是面向用户的中文方案名（见 utils/probeMeta.js）。
// 定时拨测只可能由 SLA 任务产生（= apiOptions），业务拨测还含节点上报/一键拨测的诊断类（= probeHistoryTypes）——
// 按页签给对应选项，避免在定时拨测里出现永远筛不到东西的"IP 归属地"。
const typeOpts = computed(() => (cat.value === 'sched' ? apiOptions : probeHistoryTypes))
// 时间范围：value 是小时数（'all' = 不限），提交时换算成 since（RFC3339）交给后端
const rangeOpts = [
  { value: 'all', label: '全部时间' },
  { value: '1', label: '近 1 小时' },
  { value: '24', label: '近 24 小时' },
  { value: '168', label: '近 7 天' },
]
const f = reactive({ range: 'all', node: '', type: '', target: '', limit: 100 })
const probes = ref([])
const knownNodes = ref([])
const loading = ref(false)
const error = ref('')
const exporting = ref(false)

const curLabel = computed(() => cats.find((c) => c.value === cat.value)?.label || '')

// 行 key：接口刻意不返回主键 id（ProbeResult.ID 是 json:"-"），
// 直接绑 p.id 会让整表 key 全是 undefined（Vue 报重复 key），故用业务字段拼一个稳定值。
const rowKey = (p, i) => `${p.createdAt}|${p.nodeId}|${p.apiType}|${p.raw}|${i}`

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

// source → 可读来源标签（叫法与页签保持一致：定时拨测 / 一键拨测 / 节点上报）
const srcLabelMap = {
  sched: '定时拨测',
  biz: '一键拨测',
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
  // 节点过滤下拉数据源：节点简表（admin/user 都可访问；完整节点快照是 admin-only）
  try { knownNodes.value = (await fetchNodesBrief()).map((n) => n.nodeId) } catch { /* 忽略 */ }
})

function switchCat(v) {
  if (cat.value === v) return
  cat.value = v
  f.node = ''
  f.type = ''
  f.target = ''
  load()
}

// 时间范围 → since：'all' 不带时间条件，其余按"距现在 N 小时"取 RFC3339（后端按 UTC 比较）
function sinceOf(range) {
  const h = Number(range)
  if (!h) return ''
  return new Date(Date.now() - h * 3600 * 1000).toISOString()
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    probes.value = await fetchProbes({
      node: f.node, type: f.type, cat: cat.value, limit: f.limit,
      since: sinceOf(f.range), target: f.target,
    })
  } catch (e) {
    // 失败时不能只清空列表——否则与"该筛选条件下确实没有样本"无法区分
    probes.value = []
    error.value = e?.message || '加载失败'
  } finally {
    loading.value = false
  }
}

// 导出当前筛选条件下的全部明细（不带 limit，后端默认上限 2 万行），权限范围与列表一致
async function doExport() {
  exporting.value = true
  error.value = ''
  try {
    await exportProbes({
      node: f.node, type: f.type, cat: cat.value,
      since: sinceOf(f.range), target: f.target,
    })
  } catch (e) {
    error.value = e?.message || '导出失败'
  } finally {
    exporting.value = false
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
  background: var(--ui-solid-info);
  color: #fff;
  font-weight: 600;
}
.ak-tag.ch.kind {
  background: var(--ui-tint-hover);
  border-color: var(--ui-line-ctl);
  color: var(--ak-text-secondary);
  font-size: 0.72rem;
}
</style>
