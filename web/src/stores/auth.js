// 登录态：token + 用户元信息持久化（角色只用于菜单显隐，权限以后端为准）
import { defineStore } from 'pinia'
import { getToken, setToken, setOnUnauthorized } from '../api/http.js'
import * as api from '../api/boce.js'

const META_KEY = 'ipw_boce_meta'
const readMeta = () => {
  try {
    return JSON.parse(localStorage.getItem(META_KEY) || '{}')
  } catch {
    return {}
  }
}

export const useAuthStore = defineStore('auth', {
  state: () => {
    const m = readMeta()
    return {
      token: getToken(),
      userId: m.userId ?? null,
      username: m.username || '',
      role: m.role || '',
      email: m.email || '',
      emailVerified: m.emailVerified !== false,
      points: 0,
      currencyName: '积分',
    }
  },
  getters: {
    isAuthed: (s) => !!s.token,
    isAdmin: (s) => s.role === 'admin',
    emailPending: (s) => !!s.email && s.emailVerified === false && s.role !== 'admin',
  },
  actions: {
    persist() {
      const { userId, username, role, email, emailVerified } = this
      localStorage.setItem(META_KEY, JSON.stringify({ userId, username, role, email, emailVerified }))
    },
    async login(username, password, extra) {
      const d = await api.login(username, password, extra)
      this.token = d.token
      setToken(d.token)
      Object.assign(this, {
        userId: d.userId,
        username: d.username,
        role: d.role || 'user',
        email: d.email || '',
        emailVerified: d.emailVerified !== false,
      })
      this.persist()
      this.refreshPoints()
    },
    logout() {
      this.token = ''
      setToken('')
      localStorage.removeItem(META_KEY)
      Object.assign(this, { userId: null, username: '', role: '', email: '', emailVerified: true, points: 0 })
    },
    refreshMeta(m) {
      for (const k of ['email', 'emailVerified', 'username', 'role']) if (m[k] !== undefined) this[k] = m[k]
      this.persist()
    },
    async refreshPoints() {
      try {
        const p = await api.fetchPointsMe()
        this.points = p.balance
        this.currencyName = p.rules?.currencyName || '积分'
        return p
      } catch {
        return null
      }
    },
  },
})

export function installAuthGuard(router) {
  const auth = useAuthStore()
  setOnUnauthorized(() => {
    auth.logout()
    const cur = router.currentRoute.value
    if (!cur.meta.public) router.push({ name: 'login', query: { redirect: cur.fullPath } })
  })
}
