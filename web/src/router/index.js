import { createRouter, createWebHistory } from 'vue-router'
import { useAuthStore } from '../stores/auth.js'

const routes = [
  { path: '/login', name: 'login', component: () => import('../views/LoginView.vue'), meta: { public: true } },
  { path: '/s/:token', name: 'status', component: () => import('../views/StatusView.vue'), meta: { public: true } },
  {
    path: '/',
    component: () => import('../components/AppShell.vue'),
    children: [
      { path: '', name: 'probe', component: () => import('../views/ProbeChatView.vue'), meta: { title: '新拨测' } },
      { path: 'dashboard', name: 'dashboard', component: () => import('../views/DashboardView.vue'), meta: { title: '统计大盘' } },
      { path: 'sla', name: 'sla', component: () => import('../views/SlaView.vue'), meta: { title: 'SLA 监控' } },
      { path: 'records', name: 'records', component: () => import('../views/RecordsView.vue'), meta: { title: '拨测明细' } },
      { path: 'points', name: 'points', component: () => import('../views/PointsView.vue'), meta: { title: '积分中心' } },
      { path: 'shop', name: 'shop', component: () => import('../views/ShopView.vue'), meta: { title: '积分商城' } },
      { path: 'donate', name: 'donate', component: () => import('../views/DonateView.vue'), meta: { title: '捐赠节点' } },
      { path: 'donate-admin', name: 'donate-admin', component: () => import('../views/DonateAdminView.vue'), meta: { title: '捐赠审核', admin: true } },
      { path: 'profile', name: 'profile', component: () => import('../views/ProfileView.vue'), meta: { title: '个人资料' } },
      { path: 'nodes', name: 'nodes', component: () => import('../views/NodesView.vue'), meta: { title: '节点管理', admin: true } },
      { path: 'config', name: 'config', component: () => import('../views/ConfigView.vue'), meta: { title: '配置托管', admin: true } },
      { path: 'users', name: 'users', component: () => import('../views/UsersView.vue'), meta: { title: '用户管理', admin: true } },
      { path: 'points-admin', name: 'points-admin', component: () => import('../views/PointsAdminView.vue'), meta: { title: '积分管理', admin: true } },
      { path: 'shop-admin', name: 'shop-admin', component: () => import('../views/ShopAdminView.vue'), meta: { title: '商品与订单', admin: true } },
      { path: 'settings', name: 'settings', component: () => import('../views/SettingsView.vue'), meta: { title: '系统设置', admin: true } },
    ],
  },
  { path: '/:pathMatch(.*)*', redirect: '/' },
]

const router = createRouter({ history: createWebHistory(), routes })

router.beforeEach((to) => {
  const auth = useAuthStore()
  if (!to.meta.public && !auth.isAuthed) return { name: 'login', query: { redirect: to.fullPath } }
  if (to.name === 'login' && auth.isAuthed) return { name: 'probe' }
  if (to.name === 'sla' && auth.emailPending) return { name: 'profile', query: { verify: '1' } }
  if (to.meta.admin && !auth.isAdmin) return { name: 'probe' }
  return true
})

router.afterEach((to) => {
  document.title = to.meta.title ? `${to.meta.title} · 1IPW-BOCE` : '1IPW-BOCE'
})

export default router
