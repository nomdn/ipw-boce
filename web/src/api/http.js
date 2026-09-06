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
    const msg =
      (data && (data.statusMessage || data.error || data.message)) ||
      `HTTP ${resp.status}`
    throw new ApiError(resp.status, msg)
  }
  return data
}

export const http = {
  get: (path) => request('GET', path),
  post: (path, body) => request('POST', path, body === undefined ? {} : body),
  put: (path, body) => request('PUT', path, body),
  patch: (path, body) => request('PATCH', path, body === undefined ? {} : body),
  del: (path) => request('DELETE', path),
}
