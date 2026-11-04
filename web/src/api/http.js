// ==================== HTTP 封装（Bearer 注入 / 统一错误 / 401 处理） ====================
// 后端两种错误体都要识别：内部 {statusCode,statusMessage}，/api/v1 {error:{code,message}}
export const API_BASE = import.meta.env.VITE_API_BASE || ''
const TOKEN_KEY = 'ipw_boce_token'

export const getToken = () => localStorage.getItem(TOKEN_KEY) || ''
export function setToken(t) {
  if (t) localStorage.setItem(TOKEN_KEY, t)
  else localStorage.removeItem(TOKEN_KEY)
}

let onUnauthorized = null
export function setOnUnauthorized(fn) {
  onUnauthorized = fn
}

export class ApiError extends Error {
  constructor(status, message) {
    super(message)
    this.status = status
  }
}

// 后端部分文案为英文，已知条目做中文化；未命中保留原文
const ERROR_TEXT = {
  'invalid username or password': '用户名或密码错误',
  'account disabled': '账号已停用',
  Unauthorized: '登录状态已失效，请重新登录',
  'admin role required': '需要管理员权限',
  'JWT login is not enabled (set jwt-secret)': '服务端未开启账号登录（jwt-secret 未配置）',
  'invalid body': '请求参数有误',
  'username required': '用户名必填',
  'password must be at least 6 chars': '密码至少 6 位',
  'new password must be at least 6 chars': '新密码至少 6 位',
  'old password incorrect': '原密码不正确',
  'username already exists': '用户名已存在',
  'user not found': '用户不存在',
  'cannot demote your own admin role': '不能降级自己的管理员角色',
  'cannot disable your own account': '不能停用自己的账号',
  'cannot delete your own account': '不能删除自己的账号',
  'must keep at least one enabled admin': '必须保留至少一个启用中的管理员',
  'static token has no account': '静态令牌没有对应账号',
  'All nodes are offline': '全部节点当前均处于离线状态，已跳过拨测',
  'No nodes configured for api type': '该拨测类型没有配置节点',
  'not your task': '无权访问该任务',
  'task not found': '任务不存在',
}

function localizeError(msg) {
  if (!msg) return msg
  if (ERROR_TEXT[msg]) return ERROR_TEXT[msg]
  for (const [en, zh] of Object.entries(ERROR_TEXT)) if (msg.startsWith(en)) return zh
  return msg
}

function errorText(data, status) {
  if (data && typeof data === 'object') {
    if (data.statusMessage) return data.statusMessage
    if (data.error && typeof data.error === 'object') return data.error.message || data.error.code
    if (typeof data.error === 'string') return data.error
    if (data.message) return data.message
  }
  if (typeof data === 'string' && data) return data
  return `HTTP ${status}`
}

async function request(method, path, body) {
  const headers = {}
  const token = getToken()
  if (token) headers.Authorization = `Bearer ${token}`
  if (body !== undefined) headers['Content-Type'] = 'application/json'
  let resp
  try {
    resp = await fetch(API_BASE + path, {
      method,
      headers,
      body: body !== undefined ? JSON.stringify(body) : undefined,
    })
  } catch {
    throw new ApiError(0, '网络错误，无法连接服务器')
  }
  if (resp.status === 401 && !path.startsWith('/admin/login') && onUnauthorized) onUnauthorized()
  if (resp.status === 204) return null
  const text = await resp.text()
  let data = null
  if (text) {
    try {
      data = JSON.parse(text)
    } catch {
      data = text
    }
  }
  if (!resp.ok) throw new ApiError(resp.status, localizeError(errorText(data, resp.status)))
  return data
}

export const http = {
  get: (p) => request('GET', p),
  post: (p, b) => request('POST', p, b === undefined ? {} : b),
  put: (p, b) => request('PUT', p, b),
  patch: (p, b) => request('PATCH', p, b === undefined ? {} : b),
  del: (p, b) => request('DELETE', p, b),
}
