// 时间范围模型（SLA 监控 / 统计大盘共用）
//
// 后端支持两种窗口（见 timerange.go）：
//   · 相对窗口 ?hours=N      —— “最近 N 小时”，终点恒为“现在”
//   · 绝对区间 ?start=&end=  —— 自然周期（今天 / 本周 / 本月）与自定义区间
// 本模块把两者统一成 { key, start?, end? } 状态，再由 rangeQuery() 生成查询串、
// 由 toQuery()/fromQuery() 与地址栏互转（窗口可分享、可回退、刷新不丢）。
//
// 预设清单按本仓实际能力裁剪，没有照搬通用产品的 12 项：接口上限 90 天
// （maxRangeHours），数据保留期缺省 30 天（data-retention-days），因此
// “近 6 个月 / 近 12 个月 / 今年 / 全部”在当前部署下必然是空图 —— 不放进来。
// 保留期比 90 天更短时，超出那几项由 visiblePresets() 直接隐藏。

// 预设：带 hours 的是相对窗口（可订阅实时推送）；不带的是自然周期（起点按本地时区对齐）。
// sep=true 表示在清单里该项之前画一条分组线：自然周期与它对应的“最近 N”配成一组。
export const RANGE_PRESETS = [
  { key: 'today', label: '今天' },
  { key: '24h', label: '最近 24 小时', hours: 24 },
  { key: 'week', label: '本周', sep: true },
  { key: '7d', label: '最近 7 天', hours: 168 },
  { key: 'month', label: '本月', sep: true },
  { key: '30d', label: '最近 30 天', hours: 720 },
  { key: '90d', label: '最近 90 天', hours: 2160 },
]

export const DEFAULT_RANGE = { key: '24h' }

const HOUR = 3600000

// visiblePresets 按后端给出的可用上限（天）裁剪预设清单；maxDays 为空时原样返回。
// 自然周期（今天/本周/本月）跨度都很小，恒保留。
export function visiblePresets(maxDays) {
  if (!maxDays || maxDays <= 0) return RANGE_PRESETS
  return RANGE_PRESETS.filter((p) => !p.hours || p.hours <= maxDays * 24)
}

// 自然周期的起点（本地时区）：今天 00:00 / 本周一 00:00 / 本月 1 日 00:00。
function periodStart(key, now) {
  const d = new Date(now)
  d.setHours(0, 0, 0, 0)
  if (key === 'week') {
    d.setDate(d.getDate() - ((d.getDay() + 6) % 7)) // 周一 = 一周起点（国内习惯）
  } else if (key === 'month') {
    d.setDate(1)
  }
  return d
}

// resolveRange 把状态解析成具体区间。
// 返回 { from, to, label, kind, hours }：
//   kind='rel'     相对窗口，hours 有效（可订阅实时推送）
//   kind='natural' 自然周期，起点按本地对齐、终点恒为“现在”
//   kind='abs'     自定义 / 平移后的绝对区间（终点固定，可落在过去）
export function resolveRange(state, now = new Date()) {
  const s = state || DEFAULT_RANGE
  if (s.key === 'custom') {
    const from = new Date(s.start || now.getTime() - 24 * HOUR)
    const to = new Date(s.end || now)
    if (!(to > from)) return { ...resolveRange(DEFAULT_RANGE, now) }
    return { from, to, label: '自定义区间', kind: 'abs', hours: (to - from) / HOUR }
  }
  const preset = RANGE_PRESETS.find((p) => p.key === s.key) || RANGE_PRESETS[1]
  if (preset.hours) {
    return { from: new Date(now.getTime() - preset.hours * HOUR), to: now, label: preset.label, kind: 'rel', hours: preset.hours }
  }
  return { from: periodStart(preset.key, now), to: now, label: preset.label, kind: 'natural' }
}

// rangeQuery 生成后端查询串（见 timerange.go parseTimeRange）。
// 相对窗口直接交 hours 给后端按“现在”重算 —— 比前端算好时间戳更稳（不受本地时钟偏差影响）；
// 自然周期只传 start（end 缺省即“现在”，于是窗口天然跟随当前时刻）。
export function rangeQuery(state) {
  const r = resolveRange(state)
  if (r.kind === 'rel') return `hours=${r.hours}`
  const from = r.from.toISOString()
  if (r.kind === 'natural') return `start=${from}`
  return `start=${from}&end=${r.to.toISOString()}`
}

// shiftRange 把窗口整体前移/后移一个等长周期（◀ ▶）→ 结果是绝对区间。
// 前移越过“现在”时按原长度夹回当前时刻，等价于回到“最近一个周期”。
export function shiftRange(state, dir, now = new Date()) {
  const r = resolveRange(state, now)
  const span = Math.max(60000, r.to.getTime() - r.from.getTime())
  let from = r.from.getTime() + dir * span
  let to = r.to.getTime() + dir * span
  if (to > now.getTime()) {
    to = now.getTime()
    from = to - span
  }
  return { key: 'custom', start: new Date(from).toISOString(), end: new Date(to).toISOString() }
}

// canShiftForward 右侧箭头是否可用：窗口终点已经贴着“现在”时没有更近的一期可看。
export function canShiftForward(state, now = new Date()) {
  const r = resolveRange(state, now)
  return now.getTime() - r.to.getTime() > 60000
}

// 当前窗口到“现在”的跨度（小时）：WS 订阅与快照匹配用。
export function spanHours(state, now = new Date()) {
  const r = resolveRange(state, now)
  return (r.to - r.from) / HOUR
}

// ---- 地址栏互转（窗口可分享 / 可回退 / 刷新不丢）----

export function toQuery(state) {
  const s = state || DEFAULT_RANGE
  if (s.key === 'custom' && s.start && s.end) {
    return { range: 'custom', start: s.start, end: s.end }
  }
  return { range: s.key || DEFAULT_RANGE.key }
}

export function fromQuery(q) {
  const range = q?.range
  if (range === 'custom' && q.start && q.end) {
    return { key: 'custom', start: q.start, end: q.end }
  }
  if (RANGE_PRESETS.some((p) => p.key === range)) return { key: range }
  return { ...DEFAULT_RANGE }
}

// ---- 展示辅助 ----

// 自定义区间的短标签（<input type="datetime-local"> 用同款本地格式）
export function shortStamp(d) {
  const p = (n) => String(n).padStart(2, '0')
  return `${d.getMonth() + 1}/${d.getDate()} ${p(d.getHours())}:${p(d.getMinutes())}`
}

export function absLabel(from, to) {
  return `${shortStamp(from)} – ${shortStamp(to)}`
}

// toLocalInput 时间 → datetime-local 的 value（本地时区，不能用 toISOString：那是 UTC）
export function toLocalInput(d) {
  const p = (n) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`
}
