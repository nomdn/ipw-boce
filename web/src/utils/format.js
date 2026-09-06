// ==================== 展示格式化 ====================
export function fmtNum(n) {
  if (n == null || isNaN(n)) return '0'
  n = Number(n)
  if (Math.abs(n) >= 1e9) return (n / 1e9).toFixed(1) + 'B'
  if (Math.abs(n) >= 1e6) return (n / 1e6).toFixed(1) + 'M'
  if (Math.abs(n) >= 1e4) return (n / 1e3).toFixed(1) + 'K'
  return String(Math.round(n))
}

// 平均延迟（ms），由 延迟和 / 请求数 推得
export function avgMs(sumMs, total) {
  if (!total) return '—'
  return (sumMs / total).toFixed(0) + ' ms'
}

// 错误率百分比
export function pct(errors, total) {
  if (!total) return '0.00%'
  return ((errors / total) * 100).toFixed(2) + '%'
}

// 后端 minute 为 unix 分钟桶 → 可读时间
export function fmtMinute(m) {
  if (m == null) return '—'
  const d = new Date(m * 60000)
  return `${pad(d.getHours())}:${pad(d.getMinutes())}`
}
function pad(n) { return String(n).padStart(2, '0') }

// 距现在多久
export function timeAgo(iso) {
  if (!iso) return '—'
  const t = new Date(iso).getTime()
  if (isNaN(t)) return iso
  const s = Math.floor((Date.now() - t) / 1000)
  if (s < 5) return '刚刚'
  if (s < 60) return `${s}s 前`
  if (s < 3600) return `${Math.floor(s / 60)}m 前`
  if (s < 86400) return `${Math.floor(s / 3600)}h 前`
  return `${Math.floor(s / 86400)}d 前`
}

// 展示时间戳（ISO 或秒）
export function fmtTime(v) {
  if (!v) return '—'
  const d = typeof v === 'number' ? new Date(v * 1000) : new Date(v)
  if (isNaN(d)) return String(v)
  const p = (n) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}

export { fmtNum as num }
