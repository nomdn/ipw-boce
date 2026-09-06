// ==================== 登录态（Pinia） ====================
// 持久化 token + 当前用户元信息（username/role/email/userId），刷新后仍能恢复侧边菜单显隐。
// 说明：角色仅用于前端菜单显隐；真正的权限收紧在后端 adminOnly 中间件。
import { defineStore } from 'pinia'
import { http, getToken, setToken, setOnUnauthorized } from '../api/http.js'

const META_KEY = 'ipw_boce_meta'
const META_FIELDS = ['userId', 'username', 'role', 'email', 'emailVerified']

function readMeta() {
  try {
    return JSON.parse(localStorage.getItem(META_KEY) || '{}')
  } catch {
    return {}
  }
}
function writeMeta(obj) {
  const meta = {}
  for (const k of META_FIELDS) if (obj[k] !== undefined) meta[k] = obj[k]
  localStorage.setItem(META_KEY, JSON.stringify(meta))
}
function clearMeta() {
  localStorage.removeItem(META_KEY)
}

export const useAuthStore = defineStore('auth', {
  state: () => {
    const meta = readMeta()
    return {
      token: getToken(),
      userId: meta.userId || null,
      username: meta.username || '',
      role: meta.role || '',
      email: meta.email || '',
      emailVerified: meta.emailVerified === undefined ? true : !!meta.emailVerified,
      expiresAt: null,
    }
  },
  getters: {
    isAuthed: (s) => !!s.token,
    isAdmin: (s) => (s.role ? s.role === 'admin' : true), // 无 role 的旧会话(升级前仅单一 admin)视为 admin
    // 邮箱待验证：普通账号填了邮箱但未验证（限制 SLA）；admin/无邮箱不视为待验证
    emailPending: (s) => !!s.email && s.emailVerified === false,
  },
  actions: {
    async login(username, password) {
      const data = await http.post('/admin/login', { username, password })
      this.token = data.token
      this.userId = data.userId ?? this.userId
      this.username = data.username || username
      this.role = data.role || 'user'
      this.email = data.email || ''
      this.emailVerified = data.emailVerified === undefined ? true : !!data.emailVerified
      this.expiresAt = data.expiresAt || null
      setToken(data.token)
      writeMeta(this.$state)
    },
    logout() {
      this.token = ''
      this.userId = null
      this.username = ''
      this.role = ''
      this.email = ''
      this.emailVerified = true
      this.expiresAt = null
      setToken('')
      clearMeta()
    },
    // 本人口令/邮箱在"个人资料"页更新后，同步前端登录态与本地元信息
    refreshMeta(meta) {
      if (meta.email !== undefined) this.email = meta.email
      if (meta.emailVerified !== undefined) this.emailVerified = !!meta.emailVerified
      if (meta.username !== undefined) this.username = meta.username
      if (meta.role !== undefined) this.role = meta.role
      writeMeta(this.$state)
    },
  },
})

// 注入 401 处理：任何接口 401 即登出（不清路由，交给路由守卫跳登录）
export function installAuthGuard(router) {
  const auth = useAuthStore()
  setOnUnauthorized(() => {
    auth.logout()
    if (router.currentRoute.value.name !== 'login') {
      router.push({ name: 'login', query: { redirect: router.currentRoute.value.fullPath } })
    }
  })
}
