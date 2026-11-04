// 弹窗公告"是否已读"：localStorage / sessionStorage 记录，rev 变了一定重新弹
const KEY = 'ipw_popup_seen'

const today = () => {
  const d = new Date()
  return `${d.getFullYear()}-${d.getMonth() + 1}-${d.getDate()}`
}

function read(store) {
  try {
    return JSON.parse(store.getItem(KEY) || 'null')
  } catch {
    return null
  }
}

// once：同 rev 只弹一次；daily：同 rev 每天一次；session：同一浏览器会话内不重复
export function shouldShow(p) {
  if (!p) return false
  if (p.frequency === 'session') {
    const s = read(sessionStorage)
    return !(s && s.rev === p.rev)
  }
  const s = read(localStorage)
  if (!s || s.rev !== p.rev) return true
  if (p.frequency === 'daily') return s.day !== today()
  return false
}

export function markSeen(p) {
  if (!p) return
  const v = JSON.stringify({ rev: p.rev, day: today(), at: Date.now() })
  try {
    if (p.frequency === 'session') sessionStorage.setItem(KEY, v)
    else localStorage.setItem(KEY, v)
  } catch {
    /* 隐私模式等：忽略，最多多弹一次 */
  }
}

export function resetSeen() {
  try {
    localStorage.removeItem(KEY)
    sessionStorage.removeItem(KEY)
  } catch {
    /* ignore */
  }
}
