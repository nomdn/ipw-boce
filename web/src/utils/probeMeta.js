// ==================== 拨测方案元数据 ====================
//
// 前端展示用的"拨测方案"信息，与后端 /v1/{apiType}/* 接口 slug 解耦：
//   - apiOptions：定时拨测(SLA)候选方案。value=apiType slug，label=面向用户的方案名。
//     严格对齐后端 knownProbeTaskTypes（仅 tcping/speed/ssl/detail/dns）；
//     whois/dnssec/location/asn 等诊断类不在 SLA 任务里出现，落在 ProbeView 的 typeMeta。
//   - apiLabel(apiType)：单值查表（表格列、tag 等渲染用）。
//     未知 apiType 走 fallback，不显示空白。
//   - parseProbeRaw(apiType, raw)：把节点侧 raw 拆成"协议/记录类型" + "目标"
//     （speed raw 形如 v4//example.com；dns 形如 a/example.com；其他原样）。
//
// 顺序即前端 select 展示顺序。

export const apiOptions = [
  { value: 'detail', label: '综合详情 · 网站检查' },
  { value: 'ssl', label: 'SSL · 证书检测' },
  { value: 'tcping', label: 'TCPing · 端口连通' },
  { value: 'speed', label: 'Speed · 下载速度' },
  { value: 'dns', label: 'DNS · 域名解析' },
]

const labelMap = Object.fromEntries(apiOptions.map((o) => [o.value, o.label.replace(/\s·.*$/, '').trim()]))
// apiOptions 写出来"中文 · 副标题"，单值取前面中文短名给表格列/tag 用。
// 解析时若匹配不到，返回原 apiType，避免缺失项显示空白。

// 取中文短名（去掉" · 副标题"）给表格 tag / 列表用
export function apiLabel(apiType) {
  if (!apiType) return '—'
  return labelMap[apiType] || apiType
}

// parseProbeRaw 把节点侧 raw 拆成"协议/记录类型" + "目标"。
//   - speed：raw 形如 "v4/<URL>" / "v6/<URL>" → { kind: 'v4 测速' | 'v6 测速', target: '<URL>' }
//   - dns  ：raw 形如 "<type>/<domain>"        → { kind: 'A 解析' | ..., target: '<domain>' }
//   - 其他：原样显示，kind 留空
export function parseProbeRaw(apiType, raw) {
  if (!raw) return { kind: '', target: '' }
  if (apiType === 'speed') {
    const m = /^([vV][46])\/(.+)$/.exec(raw)
    if (m) {
      const proto = m[1].toLowerCase()
      return { kind: proto === 'v4' ? 'IPv4 测速' : 'IPv6 测速', target: m[2] }
    }
    return { kind: '测速', target: raw }
  }
  if (apiType === 'dns') {
    const m = /^([a-zA-Z]+)\/(.+)$/.exec(raw)
    if (m) return { kind: dnsKindLabel(m[1]), target: m[2] }
    return { kind: '解析', target: raw }
  }
  return { kind: '', target: raw }
}

function dnsKindLabel(t) {
  const map = {
    a: 'A 解析', aaaa: 'AAAA 解析', cname: 'CNAME 解析', mx: 'MX 邮件',
    ns: 'NS 服务器', txt: 'TXT 文本', srv: 'SRV 服务', caa: 'CAA 授权',
    ptr: 'PTR 反查',
  }
  return map[t.toLowerCase()] || `${t.toUpperCase()} 解析`
}