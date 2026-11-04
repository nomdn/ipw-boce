// ==================== 后端接口封装 ====================
import { http, API_BASE, getToken, ApiError } from './http.js'

const enc = encodeURIComponent
const winQ = (w = 24) => (typeof w === 'string' ? w : `hours=${w}`)

// ---- 账号 ----
export const login = (username, password, extra = {}) => http.post('/admin/login', { username, password, ...extra })
export const sendRegisterCode = (email, username, extra = {}) => http.post('/admin/register/send-code', { email, username, ...extra })
export const register = (p) => http.post('/admin/register', p)
export const fetchStatus = () => http.get('/admin/status')

// ---- 个人资料 ----
export const fetchMe = () => http.get('/admin/me')
export const updateMe = (patch) => http.patch('/admin/me', patch)
export const changePassword = (oldPassword, newPassword) => http.patch('/admin/me/password', { oldPassword, newPassword })
export const sendVerifyCode = () => http.post('/admin/me/verify/send')
export const submitVerifyCode = (code) => http.post('/admin/me/verify', { code })
export const createMyToken = () => http.post('/admin/me/token')
export const revokeMyToken = () => http.del('/admin/me/token')
export const testWebhook = () => http.post('/admin/me/webhook/test')
export const fetchMyUsage = (h = 24) => http.get(`/admin/me/usage?hours=${h}`)

// ---- 站内信 ----
export const fetchNotices = () => http.get('/admin/notices?limit=50')
export const fetchUnread = () => http.get('/admin/notices/unread')
export const markNoticesRead = (ids) => http.post('/admin/notices/read', ids?.length ? { ids } : {})
export const clearNotices = () => http.del('/admin/notices')

// ---- 节点 ----
export const fetchNodes = () => http.get('/admin/nodes')
export const fetchNodesBrief = () => http.get('/admin/nodes/brief')
export const fetchNodeEvents = (id) => http.get(`/admin/nodes/${enc(id)}/events?limit=50`)
export const deleteNodeSnapshot = (id) => http.del(`/admin/nodes/${enc(id)}`)
export const fetchNodeDefs = () => http.get('/admin/node-defs')
export const fetchUnconfigured = () => http.get('/admin/node-defs/online')
export const createNodeDef = (n) => http.post('/admin/node-defs', n)
export const updateNodeDef = (id, n) => http.put(`/admin/node-defs/${id}`, n)
export const deleteNodeDef = (id) => http.del(`/admin/node-defs/${id}`)

// ---- 拨测 ----
export const runBatchProbe = (apiType, raw, nodes, query = {}) => {
  const q = new URLSearchParams()
  Object.entries(query).forEach(([k, v]) => v !== '' && v != null && q.set(k, v))
  const qs = q.toString()
  return http.post(`/admin/nodes/probe/${apiType}/${raw}${qs ? `?${qs}` : ''}`, nodes?.length ? { nodes } : {})
}
export const fetchProbes = (f = {}) => {
  const q = new URLSearchParams()
  Object.entries(f).forEach(([k, v]) => v && q.set(k, v))
  return http.get(`/admin/probes?${q}`)
}

// ---- 统计 ----
export const fetchStatsSummary = (w) => http.get(`/admin/stats/summary?${winQ(w)}`)
export const fetchStatsTimeseries = (w) => http.get(`/admin/stats/timeseries?${winQ(w)}`)

// ---- 托管配置 ----
export const fetchNodeConfigs = () => http.get('/admin/node-configs')
export const fetchNodeConfig = (id) => http.get(`/admin/node-configs/${enc(id)}`)
export const putNodeConfig = (id, obj) => http.put(`/admin/node-configs/${enc(id)}`, obj)
export const deleteNodeConfig = (id) => http.del(`/admin/node-configs/${enc(id)}`)

// ---- 任务 / SLA ----
export const fetchTaskMeta = () => http.get('/admin/tasks/meta')
export const fetchTasks = () => http.get('/admin/tasks')
export const createTask = (t) => http.post('/admin/tasks', t)
export const updateTask = (id, t) => http.put(`/admin/tasks/${id}`, t)
export const setTaskEnabled = (id, enabled) => http.patch(`/admin/tasks/${id}/enabled`, { enabled })
export const deleteTask = (id) => http.del(`/admin/tasks/${id}`)
export const fetchTaskSla = (id, w) => http.get(`/admin/tasks/${id}/sla?${winQ(w)}`)
export const fetchTaskSeries = (id, w) => http.get(`/admin/tasks/${id}/series?${winQ(w)}`)
export const fetchMineSeries = (w) => http.get(`/admin/tasks/mine/series?${winQ(w)}`)
export const shareTasks = (ids, token) => http.post('/admin/tasks/share', { ids, token: token || undefined })
export const unshareTasks = (ids) => http.del('/admin/tasks/share', { ids })
export const fetchTaskShare = (id) => http.get(`/admin/tasks/${id}/share`)
export const fetchPublicStatus = (token, h = 24) => http.get(`/api/public/status/${enc(token)}?hours=${h}`)

// ---- 用户 ----
export const fetchUsers = () => http.get('/admin/users')
export const createUser = (u) => http.post('/admin/users', u)
export const updateUser = (id, p) => http.patch(`/admin/users/${id}`, p)
export const resetUserPassword = (id, password) => http.patch(`/admin/users/${id}/password`, { password })
export const deleteUser = (id) => http.del(`/admin/users/${id}`)

// ---- 积分（用户） ----
export const fetchPointsMe = () => http.get('/admin/points/me')
export const fetchMyLedger = (offset = 0, limit = 30) => http.get(`/admin/points/ledger?offset=${offset}&limit=${limit}`)
export const checkin = () => http.post('/admin/points/checkin')
export const redeemPointsCode = (code) => http.post('/admin/points/redeem', { code })
export const fetchProducts = () => http.get('/admin/shop/products')
export const placeOrder = (productId, quantity, remark) => http.post('/admin/shop/orders', { productId, quantity, remark })
export const fetchMyOrders = () => http.get('/admin/shop/orders')

// ---- 积分（管理） ----
export const fetchPointsSettings = () => http.get('/admin/points/admin/settings')
export const savePointsSettings = (s) => http.put('/admin/points/admin/settings', s)
export const fetchPointsOverview = () => http.get('/admin/points/admin/overview')
export const adjustPoints = (p) => http.post('/admin/points/admin/adjust', p)
export const fetchAllLedger = (f = {}) => {
  const q = new URLSearchParams({ limit: 100, ...f })
  return http.get(`/admin/points/admin/ledger?${q}`)
}
export const fetchAccounts = (q = '') => http.get(`/admin/points/admin/accounts?q=${enc(q)}`)
export const fetchCodes = () => http.get('/admin/points/admin/codes')
export const createCodes = (p) => http.post('/admin/points/admin/codes', p)
export const toggleCode = (id, enabled) => http.patch(`/admin/points/admin/codes/${id}`, { enabled })
export const deleteCode = (id) => http.del(`/admin/points/admin/codes/${id}`)
export const fetchAdminProducts = () => http.get('/admin/shop/admin/products')
export const createProduct = (p) => http.post('/admin/shop/admin/products', p)
export const updateProduct = (id, p) => http.put(`/admin/shop/admin/products/${id}`, p)
export const deleteProduct = (id) => http.del(`/admin/shop/admin/products/${id}`)
export const resetProductSecret = (id) => http.post(`/admin/shop/admin/products/${id}/secret`)
export const testProductWebhook = (id) => http.post(`/admin/shop/admin/products/${id}/test`)
export const fetchCards = (id, state = '') => http.get(`/admin/shop/admin/products/${id}/cards?state=${state}`)
export const importCards = (id, cards) => http.post(`/admin/shop/admin/products/${id}/cards`, { cards })
export const deleteCard = (id) => http.del(`/admin/shop/admin/cards/${id}`)
export const fetchAdminOrders = (f = {}) => {
  const q = new URLSearchParams()
  Object.entries(f).forEach(([k, v]) => v && q.set(k, v))
  return http.get(`/admin/shop/admin/orders?${q}`)
}
export const deliverOrder = (id, content) => http.post(`/admin/shop/admin/orders/${id}/deliver`, { content })
export const cancelOrder = (id, reason) => http.post(`/admin/shop/admin/orders/${id}/cancel`, { reason })
export const retryOrder = (id) => http.post(`/admin/shop/admin/orders/${id}/retry`)

// ---- 捐赠节点 ----
export const fetchDonateMeta = () => http.get('/admin/donations/meta')
export const fetchMyDonations = () => http.get('/admin/donations/mine')
export const createDonation = (d) => http.post('/admin/donations', d)
export const updateDonation = (id, d) => http.patch(`/admin/donations/${id}`, d)
export const rotateDonationKey = (id) => http.post(`/admin/donations/${id}/rotate-key`)
export const withdrawDonation = (id) => http.post(`/admin/donations/${id}/withdraw`)
export const resetAgentToken = () => http.post('/admin/agent/token')

// 系统设置 / 人机验证
export const fetchPublicSite = () => http.get('/admin/public/site')
export const fetchCaptcha = () => http.get('/admin/captcha')
export const fetchSiteSettings = () => http.get('/admin/site-settings')
export const saveSiteSettings = (s) => http.put('/admin/site-settings', s)
export const sendTestMail = (to) => http.post('/admin/site-settings/test-mail', { to })
// 弹窗图片上传：multipart，不能走 JSON 封装
export async function uploadPopupImage(file) {
  const fd = new FormData()
  fd.append('file', file)
  let resp
  try {
    resp = await fetch(API_BASE + '/admin/popup/image', { method: 'POST', headers: { Authorization: `Bearer ${getToken()}` }, body: fd })
  } catch {
    throw new ApiError(0, '网络错误，无法连接服务器')
  }
  const data = await resp.json().catch(() => ({}))
  if (!resp.ok) throw new ApiError(resp.status, data.statusMessage || `上传失败（${resp.status}）`)
  return data
}
export const fetchAdminDonations = (f = {}) => {
  const q = new URLSearchParams()
  Object.entries(f).forEach(([k, v]) => v && q.set(k, v))
  return http.get(`/admin/donations/admin?${q}`)
}
export const reviewDonation = (id, action, note) => http.post(`/admin/donations/admin/${id}/review`, { action, note })
export const settleDonations = (date) => http.post('/admin/donations/admin/settle', date ? { date } : {})

// ---- 节点运维 ----
export const fetchNodesUptime = (days = 7) => http.get(`/admin/nodes/uptime?days=${days}`)
export const fetchNodeUptime = (id, days = 7) => http.get(`/admin/nodes/${enc(id)}/uptime?days=${days}`)
export const fetchNodeRuntimeConfig = (id) => http.get(`/admin/nodes/${enc(id)}/config`)
export const applyNodeRuntimeConfig = (id, p) => http.post(`/admin/nodes/${enc(id)}/config`, p)
export const mergeHostedConfig = (id, config) => http.post(`/admin/node-configs/${enc(id)}/merge`, { config })
export const dispatchOta = (id, p) => http.post(`/admin/nodes/${enc(id)}/ota`, p)
export const fetchOtaTasks = () => http.get('/admin/ota-tasks?limit=100')
export const fetchMaintenance = () => http.get('/admin/maintenance')
export const createMaintenance = (w) => http.post('/admin/maintenance', w)
export const deleteMaintenance = (id) => http.del(`/admin/maintenance/${id}`)

// ---- CSV 导出（带鉴权头，fetch 成 Blob 下载） ----
export async function download(path) {
  const resp = await fetch(API_BASE + path, { headers: { Authorization: `Bearer ${getToken()}` } })
  if (!resp.ok) {
    let msg = `HTTP ${resp.status}`
    try {
      const j = await resp.json()
      msg = j.statusMessage || j.error?.message || msg
    } catch {}
    throw new ApiError(resp.status, msg)
  }
  const cd = resp.headers.get('Content-Disposition') || ''
  const name = /filename="?([^";]+)"?/.exec(cd)?.[1] || 'export.csv'
  const url = URL.createObjectURL(await resp.blob())
  const a = document.createElement('a')
  a.href = url
  a.download = name
  document.body.appendChild(a)
  a.click()
  a.remove()
  setTimeout(() => URL.revokeObjectURL(url), 0)
}
export const qs = (o = {}) => {
  const q = new URLSearchParams()
  Object.entries(o).forEach(([k, v]) => v !== '' && v != null && q.set(k, v))
  const s = q.toString()
  return s ? `?${s}` : ''
}
