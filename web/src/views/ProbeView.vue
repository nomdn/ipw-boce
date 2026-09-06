<template>
  <div>
    <!-- 参数配置 -->
    <section class="panel" style="margin-bottom:18px">
      <h2 class="panel-title">发起一键拨测 <span class="hl">/ POST nodes/probe</span></h2>

      <div class="form-row">
        <div class="form-field">
          <label>接口类型</label>
          <select class="ak-select" v-model="form.apiType" @change="onTypeChange">
            <option v-for="t in typeMeta" :key="t.value" :value="t.value">{{ t.label }}</option>
          </select>
        </div>

        <!-- dns 域名解析：记录类型用下拉选 + 域名输入，前端自动拼 raw（{type}/{domain}），用户不必手拼 -->
        <template v-if="form.apiType === 'dns'">
          <div class="form-field">
            <label>记录类型</label>
            <select class="ak-select" v-model="form.dnsType">
              <option v-for="t in dnsTypes" :key="t.value" :value="t.value">{{ t.value.toUpperCase() }} · {{ t.label }}</option>
            </select>
          </div>
          <div class="form-field" style="flex:1;min-width:260px">
            <label>解析域名</label>
            <input class="ak-input" v-model.trim="form.dnsDomain" placeholder="example.com"
              @keyup.enter="run" />
          </div>
        </template>

        <div class="form-field" v-else style="flex:1;min-width:260px">
          <label>拨测目标 raw</label>
          <input class="ak-input" v-model.trim="form.raw" :placeholder="currentMeta.hint"
            @keyup.enter="run" />
        </div>

        <div class="form-field" v-if="form.apiType === 'tcping'">
          <label>端口</label>
          <input class="ak-input" v-model.trim="form.port" placeholder="80" style="width:90px" />
        </div>
        <div class="form-field" v-if="form.apiType === 'tcping'">
          <label>次数</label>
          <input class="ak-input" v-model.trim="form.count" placeholder="4" style="width:90px" />
        </div>

        <!-- speed 测速：协议 v4/v6 用下拉选，前端自动拼进 raw（v4/ 或 v6/ 前缀），用户不必手拼 -->
        <div class="form-field" v-if="form.apiType === 'speed'">
          <label>测速协议</label>
          <select class="ak-select" v-model="form.proto" style="width:110px">
            <option value="v4">v4 · IPv4</option>
            <option value="v6">v6 · IPv6</option>
          </select>
        </div>

        <div class="form-field">
          <label>目标节点</label>
          <select class="ak-select" v-model="form.mode" @change="form.nodes=''">
            <option value="all">全部节点（HTTP + WS）</option>
            <option value="custom">指定节点 id</option>
          </select>
        </div>
      </div>

      <div v-if="form.mode === 'custom'" class="form-row" style="margin-top:12px">
        <div class="form-field" style="flex:1">
          <label>节点 id（逗号分隔）</label>
          <input class="ak-input" v-model.trim="form.nodes" placeholder="test-node-1, test-http" />
        </div>
      </div>

      <div style="margin-top:16px;display:flex;align-items:center;gap:14px">
        <button class="ak-button ak-button--action" @click="run" :disabled="busy">
          {{ busy ? '拨测中…' : '执行拨测' }}
        </button>
        <span v-if="lastMeta" class="dim" style="font-size:.82rem">
          上次：{{ lastMeta.apiType }} / {{ lastMeta.raw }} → targeted={{ lastMeta.targeted }} ok={{ lastMeta.ok }} failed={{ lastMeta.failed }}
          <span v-if="lastMeta.unknown?.length" class="err"> unknown={{ lastMeta.unknown.join(',') }}</span>
        </span>
      </div>
    </section>

    <!-- 结果 -->
    <div v-if="busy" class="loading-center"><span class="ak-loading"></span></div>
    <div v-if="error" class="ak-notice ak-notice--danger" style="margin-bottom:14px">{{ error }}</div>

    <div v-if="results.length" class="node-wall">
      <div v-for="r in results" :key="r.nodeId" class="probe-card">
        <header class="pc-head">
          <div class="pc-title">
            <span class="ak-tag ch" :class="statusClass(r)">{{ r.status || 'ERR' }}</span>
            <span class="mono pc-nodeid" :title="r.nodeId">{{ r.nodeId }}</span>
            <span class="ak-tag ch" :class="r.channel === 'ws' ? 'ak-tag--advanced' : 'ak-tag--neutral'">{{ r.channel }}</span>
            <span class="dim" style="margin-left:auto;white-space:nowrap">{{ r.latencyMs }} ms</span>
          </div>
          <div class="pc-label mono" :title="r.label">{{ r.label }}</div>
        </header>
        <div class="pc-body">
          <!-- dns：扁平 DNSResult {domain, record[], ttl, duration} → 可读列表展示 -->
          <template v-if="form.apiType === 'dns' && dnsObj(r)">
            <div class="dns-head">
              <span class="ak-tag ch dns-type">{{ form.dnsType.toUpperCase() }}</span>
              <span class="mono" style="font-weight:600">{{ dnsObj(r).domain }}</span>
              <span class="dim" style="margin-left:auto">
                解析 {{ dnsObj(r).duration?.toFixed(2) }} ms
                <template v-if="dnsObj(r).ttl">· TTL {{ dnsObj(r).ttl }}s</template>
                · {{ (dnsObj(r).record || []).length }} 条
              </span>
            </div>
            <ul class="dns-records">
              <li v-for="(rec, i) in (dnsObj(r).record || [])" :key="i" class="dns-record mono">
                <span class="dns-idx">{{ i + 1 }}</span>{{ rec }}
              </li>
              <li v-if="!(dnsObj(r).record || []).length" class="dns-record dim">无解析结果</li>
            </ul>
          </template>
          <!-- 其余类型：通用结构化可视化 -->
          <ResultFieldView v-else-if="r.body && typeof r.body === 'object'" :node="r.body" />
          <pre v-else class="raw-json">{{ pretty(r.error ? { error: r.error } : r.body) }}</pre>
        </div>
      </div>
      <div v-if="!results.length && !busy" class="panel" style="grid-column:1/-1">
        <h2 class="panel-title">尚未执行</h2>
        <p class="dim" style="margin:0">配置上方参数后点击「执行拨测」，将同步聚合各节点结果在此展示。</p>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, reactive, computed } from 'vue'
import { runBatchProbe } from '../api/boce.js'
import ResultFieldView from '../components/ResultFieldView.vue'
import { apiOptions } from '../utils/probeMeta.js'

// typeMeta：UI 元数据 (label/hint/def) 沿用 apiOptions 顺序与命名，
// 仅补前端表单字段（hint/def/defDomain/defType）保持向后兼容。
const typeMeta = [
  { value: 'tcping',   label: 'TCPing · 端口连通',   hint: 'host，如 1.1.1.1', def: '1.1.1.1' },
  { value: 'speed',    label: 'Speed · 下载速度',    hint: '测速文件 URL，如 https://host/file', def: 'https://speed.cloudflare.com/__down?bytes=1000000' },
  { value: 'detail',   label: '综合详情 · 网站检查', hint: '完整 URL，如 https://www.qq.com', def: 'https://www.qq.com' },
  { value: 'ssl',      label: 'SSL · 证书检测',      hint: '完整 URL 或域名', def: 'https://www.qq.com' },
  { value: 'dns',      label: 'DNS · 域名解析',      hint: '', def: '', defDomain: 'example.com', defType: 'a' },
  { value: 'whois',    label: 'Whois · 域名注册',    hint: '域名，如 example.com', def: 'example.com' },
  { value: 'dnssec',   label: 'DNSSEC · 签名校验',   hint: '域名', def: 'cloudflare.com' },
  { value: 'location', label: 'Location · IP 归属地', hint: 'IP', def: '8.8.8.8' },
  { value: 'asn',      label: 'ASN · 自治域',        hint: 'IP', def: '8.8.8.8' },
]

// dns 域名解析可选的记录类型（value 为节点侧 /dns/:type 的 slug）
const dnsTypes = [
  { value: 'a', label: 'IPv4 地址' },
  { value: 'aaaa', label: 'IPv6 地址' },
  { value: 'cname', label: '别名记录' },
  { value: 'mx', label: '邮件交换' },
  { value: 'ns', label: '名称服务器' },
  { value: 'txt', label: '文本记录' },
  { value: 'srv', label: '服务定位' },
  { value: 'caa', label: '证书颁发机构授权' },
  { value: 'ptr', label: '反向解析' },
]

const form = reactive({
  apiType: 'tcping',
  raw: '1.1.1.1',
  port: '80',
  count: '4',
  proto: 'v4', // 仅 speed 测速使用：IPv4/IPv6 协议前缀
  dnsType: 'a',   // 仅 dns 域名解析使用：记录类型（a/aaaa/cname/mx/...）
  dnsDomain: '',  // 仅 dns 域名解析使用：要解析的域名
  mode: 'all',
  nodes: '',
})

const currentMeta = computed(() => typeMeta.find((t) => t.value === form.apiType) || typeMeta[0])

const busy = ref(false)
const error = ref('')
const results = ref([])
const lastMeta = ref(null)

function onTypeChange() {
  const m = currentMeta.value
  if (form.apiType === 'dns') {
    // dns：域名与记录类型分开录入，raw 由两者拼接，这里不回填 raw
    form.dnsDomain = m.defDomain || ''
    form.dnsType = m.defType || 'a'
    form.raw = ''
    return
  }
  form.raw = m.def
}

function statusClass(r) {
  if (r.error || (r.status >= 400)) return 'ak-tag--danger'
  if (r.status >= 200 && r.status < 300) return 'ak-tag--advanced'
  return 'ak-tag--neutral'
}

function pretty(v) {
  if (v == null) return ''
  if (typeof v === 'string') return v
  try { return JSON.stringify(v, null, 2) } catch { return String(v) }
}

// dnsObj 把节点返回的 DNSResult body（可能为字符串）解析成对象；
// 仅在确为 {domain, record[], ...} 结构时返回，否则返回 null（前端回退成裸 JSON 展示）。
function dnsObj(r) {
  if (r.error) return null
  let b = r.body
  if (typeof b === 'string') {
    try { b = JSON.parse(b) } catch { return null }
  }
  if (b && typeof b === 'object' && typeof b.domain === 'string' && Array.isArray(b.record)) {
    return b
  }
  return null
}

async function run() {
  if (busy.value) return
  const t = currentMeta.value
  let raw = form.raw
  if (form.apiType === 'dns') {
    // dns：记录类型 + 域名分开录入，raw 由两者拼接为 {type}/{domain}
    if (!form.dnsDomain) { error.value = '请输入要解析的域名'; return }
    raw = `${form.dnsType}/${form.dnsDomain}`
  } else if (!form.raw) {
    error.value = '请输入拨测目标'
    return
  }
  error.value = ''
  busy.value = true
  results.value = []
  // speed 测速：raw 为纯 URL，需拼上协议前缀（v4/ 或 v6/）；若用户已手动带前缀则不重复拼
  if (form.apiType === 'speed' && !/^(v4|v6)\//.test(raw)) raw = `${form.proto}/${raw}`
  // 指定节点模式 → 转数组；全部模式 → 空（后端默认全池）
  const nodes = form.mode === 'custom'
    ? form.nodes.split(',').map((s) => s.trim()).filter(Boolean)
    : null
  const query = {}
  if (form.apiType === 'tcping') {
    if (form.port) query.port = form.port
    if (form.count) query.count = form.count
  }
  try {
    const data = await runBatchProbe(form.apiType, raw, nodes, query)
    results.value = data.results || []
    lastMeta.value = data
  } catch (e) {
    error.value = e?.message || '拨测失败'
    results.value = []
  } finally {
    busy.value = false
  }
}
</script>
