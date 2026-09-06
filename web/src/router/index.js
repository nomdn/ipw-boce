// ==================== 路由与登录守卫 ====================
import { createRouter, createWebHistory } from 'vue-router'
import { useAuthStore } from '../stores/auth.js'

const routes = [
  { path: '/login', name: 'login', component: () => import('../views/LoginView.vue'), meta: { public: true } },
  {
    path: '/',
    component: () => import('../components/ConsoleLayout.vue'),
    redirect: { name: 'dashboard' },
    children: [
      { path: '', name: 'dashboard', component: () => import('../views/DashboardView.vue'), meta: { title: '统计大盘' } },
      { path: 'nodes', name: 'nodes', component: () => import('../views/NodesView.vue'), meta: { title: '节点状态', role: 'admin' } },
      { path: 'probe', name: 'probe', component: () => import('../views/ProbeView.vue'), meta: { title: '一键拨测' } },
      { path: 'records', name: 'records', component: () => import('../views/RecordsView.vue'), meta: { title: '拨测明细' } },
      { path: 'sla', name: 'sla', component: () => import('../views/SlaView.vue'), meta: { title: 'SLA 监控' } },
      { path: 'config', name: 'config', component: () => import('../views/ConfigView.vue'), meta: { title: '配置分发', role: 'admin' } },
      { path: 'users', name: 'users', component: () => import('../views/UsersView.vue'), meta: { title: '用户管理', role: 'admin' } },
      { path: 'profile', name: 'profile', component: () => import('../views/ProfileView.vue'), meta: { title: '个人资料' } },
    ],
  },
  { path: '/:pathMatch(.*)*', redirect: { name: 'dashboard' } },
]

const router = createRouter({ history: createWebHistory(), routes })

// 全局守卫：未登录一律去登录页；已登录访问登录页则回首页
router.beforeEach((to) => {
  const auth = useAuthStore()
  if (!to.meta.public && !auth.isAuthed) {
    return { name: 'login', query: { redirect: to.fullPath } }
  }
  if (to.name === 'login' && auth.isAuthed) {
    return { name: 'dashboard' }
  }
  // 邮箱未验证的普通账号：拦截 SLA 监控，引导去个人资料验证邮箱
  if (to.name === 'sla' && auth.emailPending) {
    return { name: 'profile', query: { verify: '1' } }
  }
  // 受角色保护的页面：普通用户访问 → 回首页（后端另有 adminOnly 兜底）
  if (to.meta.role === 'admin' && !auth.isAdmin) {
    return { name: 'dashboard' }
  }
  return true
})

export default router
