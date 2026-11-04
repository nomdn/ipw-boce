export function fmtTime(v) {
  if (!v) return '—'
  const d = new Date(v)
  if (Number.isNaN(d.getTime()) || d.getFullYear() < 2000) return '—'
  const p = (n) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}

export const fmtPct = (v) => (v == null ? '—' : `${Number(v).toFixed(2)}%`)
export const fmtMs = (v) => (v == null || v === 0 ? '—' : `${Math.round(v)} ms`)
export const fmtNum = (v) => (v == null ? '—' : Number(v).toLocaleString())

export function prettyJSON(v) {
  if (v == null) return ''
  if (typeof v === 'string') {
    try {
      return JSON.stringify(JSON.parse(v), null, 2)
    } catch {
      return v
    }
  }
  return JSON.stringify(v, null, 2)
}

export async function copyText(text) {
  try {
    await navigator.clipboard.writeText(text)
    return true
  } catch {
    const ta = document.createElement('textarea')
    ta.value = text
    document.body.appendChild(ta)
    ta.select()
    const ok = document.execCommand('copy')
    ta.remove()
    return ok
  }
}

export const LEDGER_KIND = {
  checkin: '每日签到',
  signup: '注册奖励',
  redeem_code: '兑换码',
  admin: '管理员调整',
  api_grant: '接口发放',
  api_deduct: '接口扣减',
  order: '商城兑换',
  refund: '订单退款',
  probe: '拨测消耗',
  probe_back: '拨测退还',
  donate: '捐赠节点奖励',
  donate_day: '节点在线奖励',
}

export const DONATE_STATUS = {
  pending: ['待审核', 'warn'],
  approved: ['已上线', 'ok'],
  rejected: ['未通过', 'bad'],
  disabled: ['已停用', 'bad'],
  withdrawn: ['已撤回', ''],
}
export const NET_TYPE = { idc: '机房', home: '家庭宽带' }

export function fmtDuration(sec) {
  if (!sec) return '0'
  const d = Math.floor(sec / 86400)
  const h = Math.floor((sec % 86400) / 3600)
  const m = Math.floor((sec % 3600) / 60)
  return [d && `${d}天`, h && `${h}小时`, !d && m && `${m}分`].filter(Boolean).join('') || `${sec}秒`
}

export const ORDER_STATUS = {
  pending: ['待发货', 'warn'],
  processing: ['处理中', 'warn'],
  delivered: ['已发货', 'ok'],
  failed: ['失败已退款', 'bad'],
  canceled: ['已取消退款', 'bad'],
}

export const DELIVERY = { cdk: '卡密自动发', webhook: '接口发放', manual: '人工发货' }
