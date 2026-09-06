// ==================== ipw-boce /admin 接口封装 ====================
import { http } from './http.js'

// 服务状态
export const fetchStatus = () => http.get('/admin/status')

// ===== 公开注册 / 邮箱验证码 =====
export const sendRegisterCode = (email, username) =>
  http.post('/admin/register/send-code', { email, username })
export const register = ({ username, password, email, code }) =>
  http.post('/admin/register', { username, password, email, code })
// 登录后补验证（本人邮箱）：发码 / 提交
export const sendVerifyCode = () => http.post('/admin/me/verify/send')
export const submitVerifyCode = (code) => http.post('/admin/me/verify', { code })

// 节点快照 / 事件
export const fetchNodes = () => http.get('/admin/nodes')
export const fetchNodeEvents = (nodeId, limit = 100) =>
  http.get(`/admin/nodes/${encodeURIComponent(nodeId)}/events?limit=${limit}`)

// 统计
export const fetchStatsSummary = (hours = 24) => http.get(`/admin/stats/summary?hours=${hours}`)
export const fetchStatsTimeseries = (hours = 24) => http.get(`/admin/stats/timeseries?hours=${hours}`)

// 拨测明细（cat：sched=定时拨测 / biz=业务拨测）
export const fetchProbes = ({ node, type, cat, since, limit = 100 } = {}) => {
  const q = new URLSearchParams()
  if (node) q.set('node', node)
  if (type) q.set('type', type)
  if (cat) q.set('cat', cat)
  if (since) q.set('since', since)
  q.set('limit', limit)
  return http.get(`/admin/probes?${q.toString()}`)
}

// 一键拨测（批量）
export const runBatchProbe = (apiType, raw, nodes, query = {}) => {
  // raw 直接拼进路径（后端 /admin/nodes/probe/:apiType/*raw 按 middlewareHandler slug 语义
  // 处理：detail/ssl 的目标完整 URL 含 '://'，gin 通配原样接收、去前导 / 还原）
  const path = `/admin/nodes/probe/${apiType}/${raw}`
  const q = new URLSearchParams()
  Object.entries(query).forEach(([k, v]) => { if (v !== '' && v != null) q.set(k, v) })
  const qs = q.toString()
  return http.post(path + (qs ? `?${qs}` : ''), nodes && nodes.length ? { nodes } : {})
}

// 配置分发
export const fetchNodeConfigs = () => http.get('/admin/node-configs')
export const fetchNodeConfig = (nodeId) => http.get(`/admin/node-configs/${encodeURIComponent(nodeId)}`)
export const putNodeConfig = (nodeId, obj) =>
  http.put(`/admin/node-configs/${encodeURIComponent(nodeId)}`, obj)
export const deleteNodeConfig = (nodeId) =>
  http.del(`/admin/node-configs/${encodeURIComponent(nodeId)}`)
// 查看某节点合并后的远端配置（global 底 + 节点覆盖）
export const fetchResolvedConfig = (nodeId) => http.get(`/remote-config/${encodeURIComponent(nodeId)}`)

// ===== 定时拨测任务 + SLA（source=sched）=====
export const fetchTaskMeta = () => http.get('/admin/tasks/meta')
export const fetchTasks = () => http.get('/admin/tasks')
export const fetchTask = (id) => http.get(`/admin/tasks/${id}`)
export const createTask = (t) => http.post('/admin/tasks', t)
export const updateTask = (id, t) => http.put(`/admin/tasks/${id}`, t)
export const setTaskEnabled = (id, enabled) => http.patch(`/admin/tasks/${id}/enabled`, { enabled })
export const deleteTask = (id) => http.del(`/admin/tasks/${id}`)
// SLA 聚合：某任务在窗口内的可用率/延迟/错误率 + 最新样本特殊字段
export const fetchTaskSla = (id, hours = 24) => http.get(`/admin/tasks/${id}/sla?hours=${hours}`)
// 某任务的时序曲线（SLA 卡片延迟图）：{taskId,stepMinutes,series:[{time,samples,up,down,availability,avgMs}]}
export const fetchTaskSeries = (id, hours = 24) => http.get(`/admin/tasks/${id}/series?hours=${hours}`)
// 我的定时任务时序（当前用户自己任务 source=sched 分桶）：{stepMinutes, series:[...]}
export const fetchMineSeries = (hours = 24) => http.get(`/admin/tasks/mine/series?hours=${hours}`)

// ===== 用户管理（仅 admin）=====
export const fetchUsers = () => http.get('/admin/users')
export const createUser = (u) => http.post('/admin/users', u) // {username,password,role?,email?}
export const updateUser = (id, patch) => http.patch(`/admin/users/${id}`, patch) // {email?,role?,enabled?}
export const resetUserPassword = (id, password) => http.patch(`/admin/users/${id}/password`, { password })
export const deleteUser = (id) => http.del(`/admin/users/${id}`)

// ===== 个人资料（本人，登录即可）=====
export const fetchMe = () => http.get('/admin/me')
export const updateMeEmail = (email) => http.patch('/admin/me', { email })
export const changeMyPassword = (oldPassword, newPassword) =>
  http.patch('/admin/me/password', { oldPassword, newPassword })

// ===== 站内信（本人，铃铛）=====
// {list:[...], unread}
export const fetchNotices = (limit = 50) => http.get(`/admin/notices?limit=${limit}`)
// 仅取未读数
export const fetchUnreadCount = () => http.get('/admin/notices/unread')
// 标记已读：{ids?:[]}；缺省全已读；返回 {unread}
export const markNoticesRead = (ids) => http.post('/admin/notices/read', ids && ids.length ? { ids } : {})
// 清空本人全部通知
export const clearNotices = () => http.del('/admin/notices')
