// ==================== ipw-boce /admin 接口封装 ====================
import { http, downloadFile } from './http.js'

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
// 节点可用率（由 node_events 事件流还原离线区间算得，见 node_uptime.go）
// 列表：全部节点（不含逐日明细）；detail：单节点（含 daily 供趋势图）
export const fetchNodesUptime = (days = 7) => http.get(`/admin/nodes/uptime?days=${days}`)
export const fetchNodeUptime = (nodeId, days = 7) =>
  http.get(`/admin/nodes/${encodeURIComponent(nodeId)}/uptime?days=${days}`)
// 节点事件历史导出 CSV（浏览器直接下载，带鉴权头）
export const exportNodeEvents = (nodeId, days = 30) =>
  downloadFile(
    `/admin/nodes/${encodeURIComponent(nodeId)}/events/export?days=${days}`,
    `node-events-${nodeId}.csv`,
  )

// ===== 计划维护窗口（节点告警免打扰，见 maintenance.go）=====
// 只屏蔽通知、不屏蔽事实：事件流/状态页/可用率统计不受影响
export const fetchMaintenance = () => http.get('/admin/maintenance')
// { scope?, startAt, endAt, repeatDaily?, reason? }；scope 空 = 全部节点
export const createMaintenance = (w) => http.post('/admin/maintenance', w)
export const deleteMaintenance = (id) => http.del(`/admin/maintenance/${id}`)
// 节点简表（登录即可，脱敏：池内节点 + 在线/版本，无远端地址）——任务表单"指定节点"勾选用
export const fetchNodesBrief = () => http.get('/admin/nodes/brief')

// 我的用量（本人任务的 sched 样本 + 本人发起的 biz 拨测，按 apiType 聚合）
export const fetchMyUsage = (hours = 24) => http.get(`/admin/me/usage?hours=${hours}`)
// 测试本人 webhook（向自配地址推一条测试消息）
export const testMyWebhook = () => http.post('/admin/me/webhook/test')
// 个人 API Token：明文仅在生成响应里返回一次；重复生成使旧 token 立即失效
export const createMyToken = () => http.post('/admin/me/token')
export const revokeMyToken = () => http.del('/admin/me/token')

// 统计（nodes 为节点 ID 数组，空/不传 = 全部节点）
const nodesQS = (nodes) => (nodes && nodes.length ? `&nodes=${encodeURIComponent(nodes.join(','))}` : '')
// 时间窗口参数：数字 = 相对窗口（hours=N，终点恒为“现在”）；
// 字符串 = 原样拼接（start=/end= 绝对区间，见 utils/timeRange.js 与后端 timerange.go）。
const winQ = (w, def = 24) => (typeof w === 'string' ? w : `hours=${w ?? def}`)
export const fetchStatsSummary = (w = 24, nodes = []) =>
  http.get(`/admin/stats/summary?${winQ(w)}${nodesQS(nodes)}`)
export const fetchStatsTimeseries = (w = 24, nodes = []) =>
  http.get(`/admin/stats/timeseries?${winQ(w)}${nodesQS(nodes)}`)
// 数据可用范围：{ now, earliest, maxDays, retention }——时间范围选择器据此裁剪预设清单
export const fetchTimeRange = () => http.get('/admin/stats/range')

// 拨测明细（cat：sched=定时拨测 / biz=业务拨测）
export const fetchProbes = ({ node, type, cat, since, target, limit = 100 } = {}) => {
  const q = new URLSearchParams()
  if (node) q.set('node', node)
  if (type) q.set('type', type)
  if (cat) q.set('cat', cat)
  if (since) q.set('since', since)
  if (target) q.set('target', target)
  q.set('limit', limit)
  return http.get(`/admin/probes?${q.toString()}`)
}

// 拨测明细导出 CSV：参数与列表完全一致（含权限范围），只是改为下载
export const exportProbes = ({ node, type, cat, since, target, limit } = {}) => {
  const q = new URLSearchParams()
  if (node) q.set('node', node)
  if (type) q.set('type', type)
  if (cat) q.set('cat', cat)
  if (since) q.set('since', since)
  if (target) q.set('target', target)
  if (limit) q.set('limit', limit)
  const qs = q.toString()
  return downloadFile(`/admin/probes/export${qs ? `?${qs}` : ''}`, 'probes.csv')
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
// 把运行时配置改动合并进节点托管配置（持久化：重启后远端配置 > ENV > setting.json）
export const mergeNodeHostedConfig = (nodeId, config) =>
  http.post(`/admin/node-configs/${encodeURIComponent(nodeId)}/merge`, { config })

// 节点运行时配置（节点进程当前生效值，WS 优先 / HTTP 回退；区别于上面「托管配置」）
export const fetchNodeRuntimeConfig = (nodeId) =>
  http.get(`/admin/nodes/${encodeURIComponent(nodeId)}/config`)
// payload: { action: 'patch' | 'refresh', config: {...}, persist: bool }
export const applyNodeRuntimeConfig = (nodeId, payload) =>
  http.post(`/admin/nodes/${encodeURIComponent(nodeId)}/config`, payload)

// ===== 节点 OTA 升级（控制台下发，节点执行）=====
// payload: { version?, url?, sha256? }（version 与 url 二选一）
export const dispatchNodeOta = (nodeId, payload) =>
  http.post(`/admin/nodes/${encodeURIComponent(nodeId)}/ota`, payload)
// 最近 OTA 任务列表（节点重连上报新版本号即判定成功，见任务 hint）
export const fetchOtaTasks = (limit = 50) => http.get(`/admin/ota-tasks?limit=${limit}`)

// ===== 定时拨测任务 + SLA（source=sched）=====
export const fetchTaskMeta = () => http.get('/admin/tasks/meta')
export const fetchTasks = (tag) => http.get(`/admin/tasks${tag ? `?tag=${encodeURIComponent(tag)}` : ''}`)
export const fetchTask = (id) => http.get(`/admin/tasks/${id}`)
export const createTask = (t) => http.post('/admin/tasks', t)
export const updateTask = (id, t) => http.put(`/admin/tasks/${id}`, t)
export const setTaskEnabled = (id, enabled) => http.patch(`/admin/tasks/${id}/enabled`, { enabled })
export const deleteTask = (id) => http.del(`/admin/tasks/${id}`)
// SLA 聚合：某任务在窗口内的可用率/延迟/错误率 + 最新样本特殊字段
export const fetchTaskSla = (id, w = 24) => http.get(`/admin/tasks/${id}/sla?${winQ(w)}`)
// 某任务的时序曲线（SLA 卡片延迟图）：{taskId,stepMinutes,series:[{time,samples,up,down,availability,avgMs}]}
// node 传节点 id 时只返回该节点的曲线（多节点对比视图逐节点拉取叠加）
export const fetchTaskSeries = (id, w = 24, node = '') =>
  http.get(`/admin/tasks/${id}/series?${winQ(w)}${node ? `&node=${encodeURIComponent(node)}` : ''}`)
// 曲线导出 CSV（同参数、同归属校验）
export const exportTaskSeries = (id, w = 24, node = '') =>
  downloadFile(
    `/admin/tasks/${id}/series/export?${winQ(w)}${node ? `&node=${encodeURIComponent(node)}` : ''}`,
    `sla-task-${id}.csv`,
  )
// 公开状态页分享（B3）：分享码即分享组——多选任务共用一个分享码；支持自定义分享码
// shareTasks([ids], token?)：token 缺省随机 6 位 hex
export const shareTasks = (ids, token) =>
  http.post('/admin/tasks/share', { ids, token: token || undefined })
export const unshareTasks = (ids) => http.del('/admin/tasks/share', { ids })
// 我的定时任务时序（当前用户自己任务 source=sched 分桶）：{stepMinutes, series:[...]}
export const fetchMineSeries = (w = 24) => http.get(`/admin/tasks/mine/series?${winQ(w)}`)

// ===== 上游节点池（数据库托管，仅 admin）=====
// 节点定义列表（含停用）
export const fetchNodeDefs = () => http.get('/admin/node-defs')
// 新增节点 {nodeId,label,url,ws,pool,stack,enabled,sortOrder}
export const createNodeDef = (n) => http.post('/admin/node-defs', n)
// 更新节点（按主键 id，未传字段保持原值）
export const updateNodeDef = (id, patch) => http.put(`/admin/node-defs/${id}`, patch)
export const deleteNodeDef = (id) => http.del(`/admin/node-defs/${id}`)
// 已接入（WS 在线 / 在线快照）但库里没有节点定义的节点，供控制台补录下拉框
export const fetchUnconfiguredNodes = () => http.get('/admin/node-defs/online')

// ===== 用户管理（仅 admin）=====
export const fetchUsers = () => http.get('/admin/users')
export const createUser = (u) => http.post('/admin/users', u) // {username,password,role?,email?}
export const updateUser = (id, patch) => http.patch(`/admin/users/${id}`, patch) // {email?,role?,enabled?}
export const resetUserPassword = (id, password) => http.patch(`/admin/users/${id}/password`, { password })
export const deleteUser = (id) => http.del(`/admin/users/${id}`)

// ===== 个人资料（本人，登录即可）=====
export const fetchMe = () => http.get('/admin/me')
// PATCH /admin/me：{email?, webhookUrl?, webhookType?}，字段可选只更新传入项
export const updateMeProfile = (patch) => http.patch('/admin/me', patch)
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
