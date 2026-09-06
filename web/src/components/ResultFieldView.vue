<template>
  <!--
    通用探测结果可视化：把任意节点返回 body 铺成结构化面板。
    - detail/ssl/tcping 顶级含 ipv4/ipv6 → 双栈分栏
    - 标量/布尔/RFC 时间/单位 → 可读行
    - 对象 → 递归子分组；字符串数组 → pill 列表；对象数组 → 逐条展开
    - 超长文本（raw/headers 等）→ 折叠 <details>
  -->
  <div v-if="isStr(node)" class="fv">
    <span v-if="isErrStr(node)" class="fv-bad mono">{{ node }}</span>
    <span v-else class="fv-val mono">{{ node }}</span>
  </div>

  <div v-else-if="Array.isArray(node)" class="fv">
    <!-- 字符串数组：竖排 pill 列表 -->
    <template v-if="node.every((x) => typeof x === 'string')">
      <ul v-if="node.length" class="fv-list">
        <li v-for="(it, i) in node" :key="i" class="fv-pill mono">{{ it }}</li>
      </ul>
      <span v-else class="fv-empty">空</span>
    </template>
    <!-- 对象数组（如 tcping results[]）：逐条渲染为卡片行 -->
    <template v-else>
      <div v-for="(it, i) in node" :key="i" class="fv-objrow">
        <div v-if="node.length > 1" class="fv-subhead">第 {{ i + 1 }} 项</div>
        <ResultFieldView :node="it" />
      </div>
    </template>
  </div>

  <div v-else-if="isObj(node)" class="fv">
    <!-- 双栈形态：顶级同时含 ipv4 + ipv6(对象或错误串) -->
    <div v-if="isDualStack(node)" class="fv-stack">
      <div class="fv-stack-col">
        <div class="fv-stack-tag fv-v4">IPv4</div>
        <ResultFieldView :node="node.ipv4" />
      </div>
      <div class="fv-stack-col">
        <div class="fv-stack-tag fv-v6">IPv6</div>
        <ResultFieldView :node="node.ipv6" />
      </div>
    </div>

    <table v-else class="fv-tbl">
      <tbody>
        <template v-for="(val, k) in node" :key="k">
          <!-- 子对象（含空对象）→ 分组子表头 + 递归 -->
          <template v-if="isObj(val)">
            <tr class="fv-grouptr"><td colspan="2" class="fv-group">{{ L(k) }}</td></tr>
            <tr v-if="isEmptyObj(val)" class="fv-row"><td></td><td class="fv-val dim">—</td></tr>
            <tr v-else class="fv-nested"><td colspan="2"><ResultFieldView :node="val" /></td></tr>
          </template>

          <!-- 字符串数组 → pill 列表 -->
          <template v-else-if="Array.isArray(val) && val.every((x) => typeof x === 'string')">
            <tr class="fv-row">
              <td class="fv-key">{{ L(k) }}</td>
              <td>
                <span v-if="!val.length" class="dim">—</span>
                <ul v-else class="fv-list fv-inline">
                  <li v-for="(it, i) in val" :key="i" class="fv-pill mono">{{ it }}</li>
                </ul>
              </td>
            </tr>
          </template>

          <!-- 对象数组 → 整段递归（占整行，独立成块） -->
          <template v-else-if="Array.isArray(val)">
            <tr class="fv-grouptr"><td colspan="2" class="fv-group">{{ L(k) }}</td></tr>
            <tr class="fv-nested"><td colspan="2"><ResultFieldView :node="val" /></td></tr>
          </template>

          <!-- 标量（字符串/数字/布尔/null） -->
          <template v-else>
            <tr class="fv-row">
              <td class="fv-key">{{ L(k) }}</td>
              <td>
                <!-- 布尔 → 徽标 -->
                <span v-if="typeof val === 'boolean'" class="fv-bool" :class="val ? 'fv-ok' : 'fv-no'">
                  {{ val ? '✓ 是' : '✕ 否' }}
                </span>
                <!-- 长文本 → 折叠 -->
                <details v-else-if="isLong(val)" class="fv-raw">
                  <summary>查看原文（{{ String(val).length }} 字符）</summary>
                  <pre class="fv-raw-pre mono">{{ val }}</pre>
                </details>
                <!-- 错误提示串 -->
                <span v-else-if="isErrStr(val)" class="fv-bad mono">{{ val }}</span>
                <!-- 常规可读值 -->
                <span v-else class="fv-val mono">{{ F(k, val) }}</span>
              </td>
            </tr>
          </template>
        </template>
      </tbody>
    </table>
  </div>

  <span v-else class="fv-val dim">{{ node == null ? '—' : String(node) }}</span>
</template>

<script setup>
import { computed } from 'vue'

defineOptions({ name: 'ResultFieldView' })
const props = defineProps({ node: { default: null } })
const node = computed(() => props.node)

const isObj = (v) => v && typeof v === 'object' && !Array.isArray(v)
const isEmptyObj = (v) => isObj(v) && Object.keys(v).length === 0
const isStr = (v) => typeof v === 'string'
const isLong = (v) => typeof v === 'string' && v.length > 120 && v.includes('\n')
const isErrStr = (v) => typeof v === 'string' && /^error\s*:/i.test(v.trim())
const isDualStack = (v) =>
  isObj(v) && (isObj(v.ipv4) || isStr(v.ipv4)) && ('ipv4' in v) && ('ipv6' in v)

/* 键名 → 中文标签（精确表 + 兜底 snake→空格标题化） */
const LABELS = {
  // 通用
  ip: 'IP', port: '端口', domain: '域名', status: '状态',
  // detail / ssl / speed 网络指标
  host_record: '解析 IP', http_status_code: 'HTTP 状态', https_status_code: 'HTTPS 状态',
  http_version: 'HTTP 版本', dns_lookup_time: 'DNS 解析耗时', tcp_connect_time: 'TCP 建连耗时',
  http_connect_time: 'HTTP 建连耗时', first_byte_time: '首字节耗时', total_time: '总耗时',
  page_size: '页面大小', download_speed: '下载速度', is_reachable: '可达',
  headers: '响应头', message: '备注', url: '目标',
  // ssl 证书
  cert_validity_days: '剩余有效期', cert_start_time: '证书生效', cert_end_time: '证书到期',
  issuer_organization: '签发机构', issuer_common_name: '签发者 CN', subject_common_name: '主体 CN',
  is_expired: '已过期', is_self_signed: '自签名', ssl_grade: '评级', san: 'SAN 域名',
  // tcping
  sent: '发包', success: '成功', loss_rate: '丢包率', max_rtt: '最大 RTT', min_rtt: '最小 RTT',
  avg_rtt: '平均 RTT', rtt: 'RTT', results: '逐包结果', timestamp: '时间',
  // dns
  record: '解析记录', ttl: 'TTL', duration: '解析耗时', query_time: '查询耗时',
  // dnssec
  enabled: '启用 DNSSEC', valid: '验证通过', has_rrsig: '存在 RRSIG', has_dnskey: '存在 DNSKEY',
  has_ds: '存在 DS', algorithm: '算法', key_tag: 'KeyTag', signer_name: '签名者', validation: '校验结论',
  // whois
  status: '域名状态', registrar: '注册商', registrant: '注册人', technical: '技术联系',
  abuseContact: '滥用举报', dates: '关键时间', nameservers: '域名服务器', whoisServer: 'WHOIS 服务器',
  name: '名称', org: '机构', phone: '电话', email: '邮箱', province: '地区', contactUri: '联系页',
  ianaId: 'IANA ID', registration: '注册时间', expiration: '到期时间', lastChanged: '最近变更',
  // location / asn
  asn: 'ASN', as: 'AS', orgName: '机构名', organization: '机构', country: '国家', country_code: '国家码',
  city: '城市', administrative_area: '省/州', latitude: '纬度', longitude: '经度', isp: '运营商',
  zipcode: '邮编', timezone: '时区', usagetype: '用途', division_code: '行政区划码',
  asNumber: 'AS 号', asName: 'AS 名', orgId: '机构 ID', regDate: '注册日期', updated: '更新日期',
  abuseEmail: '滥用邮箱', abusePhone: '滥用电话', raw: '原始信息',
}

function L(k) {
  if (LABELS[k]) return LABELS[k]
  // snake_case / camelCase 兜底
  return String(k)
    .replace(/_/g, ' ')
    .replace(/([a-z])([A-Z])/g, '$1 $2')
    .replace(/\b\w/g, (c) => c.toUpperCase())
}

/* 值格式化：时间/单位/原始文本 */
function F(k, v) {
  if (typeof v === 'number') return fmtNum(k, v)
  if (isRFC(v)) return fmtTime(v)
  return v
}
const isRFC = (s) => typeof s === 'string' && /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}/.test(s)
function fmtTime(s) {
  const d = new Date(s)
  return Number.isNaN(d.getTime()) ? s : d.toLocaleString('zh-CN', { hour12: false })
}
function fmtNum(k, v) {
  if (!Number.isFinite(v)) return String(v)
  const n = v < 0 && v !== -1 ? v : v // 保留负值(如 tcping rtt=-1 无数据)
  const msKeys = ['dns_lookup_time', 'tcp_connect_time', 'http_connect_time', 'first_byte_time',
    'total_time', 'duration', 'rtt', 'min_rtt', 'max_rtt', 'avg_rtt', 'connect_time', 'ttfb']
  const absKeys = ['min_rtt', 'max_rtt', 'avg_rtt', 'rtt', 'total_time']
  // 毫秒类
  if (msKeys.includes(k)) {
    return absKeys.includes(k) && v < 0 ? '超时/无数据' : `${fmt1(v)} ms`
  }
  if (k === 'download_speed') return fmtSpeed(v)
  if (k === 'page_size') return fmtBytes(v)
  if (k === 'loss_rate') return `${fmt1(v)}%`
  if (k === 'cert_validity_days') return `${v} 天`
  if (k === 'algorithm') return `DNSSEC 算法 ${v}`
  return String(fmt1(v))
}
function fmt1(x) { return (Math.round(x * 100) / 100).toString() }
function fmtBytes(b) {
  if (b < 1024) return `${b} B`
  if (b < 1024 * 1024) return `${(b / 1024).toFixed(1)} KB`
  return `${(b / 1024 / 1024).toFixed(2)} MB`
}
function fmtSpeed(kb) {
  if (kb < 1024) return `${fmt1(kb)} KB/s`
  return `${(kb / 1024).toFixed(2)} MB/s`
}
</script>
