// ==================== HTTP 封装（JWT 注入 / 统一错误 / 401 处理） ====================
// 后端地址统一在 src/config.js 的 API_BASE 指定（支持 VITE_API_BASE 覆盖）
import { API_BASE } from '../config.js'
const TOKEN_KEY = 'ipw_boce_token'

export function getToken() {
  return localStorage.getItem(TOKEN_KEY) || ''
}
export function setToken(t) {
  if (t) localStorage.setItem(TOKEN_KEY, t)
  else localStorage.removeItem(TOKEN_KEY)
}

// 401 回调：登录态失效时由 store 注入跳转（避免 http.js 依赖路由/避免循环引用）
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

// 后端 /admin 接口的部分 message 是英文（login/账号相关），直接展示对用户不友好。
// 这里做已知条目的中文化：精确命中 → 中文；前缀命中 → 中文并丢弃技术性尾部
// （如 "invalid body: json: ..."）；未命中一律保留原文，避免误译。
const ERROR_TEXT = {
  'invalid username or password': '用户名或密码错误',
  'account disabled': '账号已停用',
  Unauthorized: '登录状态已失效，请重新登录',
  'admin role required': '需要管理员权限',
  'JWT login is not enabled (set jwt-secret)': '服务端未开启账号登录（jwt-secret 未配置）',
  'invalid login body': '登录请求格式有误',
  'invalid body': '请求参数有误',
  'username required': '用户名必填',
  'password must be at least 6 chars': '密码至少 6 位',
  'role must be admin|user': '角色只能是 admin 或 user',
  'username already exists': '用户名已存在',
  'user not found': '用户不存在',
  'cannot demote your own admin role': '不能降级自己的管理员角色',
  'cannot disable your own account': '不能停用自己的账号',
  'cannot delete your own account': '不能删除自己的账号',
  'must keep at least one enabled admin': '必须保留至少一个启用中的管理员',
  'issue token': '登录凭证签发失败',
}

function localizeError(msg) {
  if (!msg) return msg
  if (ERROR_TEXT[msg]) return ERROR_TEXT[msg]
  for (const [en, zh] of Object.entries(ERROR_TEXT)) {
    if (msg.startsWith(en)) return zh
  }
  return msg
}

async function request(method, path, body) {
  const headers = {}
  const token = getToken()
  if (token) headers.Authorization = `Bearer ${token}`
  if (body !== undefined) headers['Content-Type'] = 'application/json'

  const resp = await fetch(API_BASE + path, {
    method,
    headers,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  })

  // 401：会话失效，触发登出跳转（登录接口本身的 401 除外）
  if (resp.status === 401 && !path.startsWith('/admin/login')) {
    if (onUnauthorized) onUnauthorized()
  }

  // 无内容成功
  if (resp.status === 204) return null

  let data = null
  const text = await resp.text()
  if (text) {
    try {
      data = JSON.parse(text)
    } catch {
      data = text
    }
  }
  if (!resp.ok) {
    const raw =
      (data && (data.statusMessage || data.error || data.message)) ||
      `HTTP ${resp.status}`
    throw new ApiError(resp.status, localizeError(raw))
  }
  return data
}

export const http = {
  get: (path) => request('GET', path),
  post: (path, body) => request('POST', path, body === undefined ? {} : body),
  put: (path, body) => request('PUT', path, body),
  patch: (path, body) => request('PATCH', path, body === undefined ? {} : body),
  del: (path, body) => request('DELETE', path, body),
}
