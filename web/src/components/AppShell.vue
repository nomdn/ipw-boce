<template>
  <div class="shell" :class="{ collapsed }">
    <!-- 侧边栏：桌面端常驻，可收起；移动端抽屉 -->
    <aside class="sidebar" :class="{ open: drawer }">
      <div class="sb-top">
        <button class="btn icon" title="收起侧边栏" aria-label="收起侧边栏" @click="toggleSidebar"><Icon name="PanelLeft" :size="20" /></button>
        <router-link to="/" class="btn icon" title="新拨测" aria-label="新拨测" @click="newChat"><Icon name="SquarePen" :size="20" /></router-link>
      </div>

      <nav class="sb-nav">
        <router-link to="/" class="sb-item" :class="{ 'is-active': route.name === 'probe' && !route.query.h }" @click="newChat">
          <Icon name="SquarePen" :size="17" /><span>新拨测</span>
        </router-link>
        <router-link v-for="it in mainNav" :key="it.to" :to="it.to" class="sb-item" @click="drawer = false">
          <Icon :name="it.icon" :size="17" /><span>{{ it.label }}</span>
        </router-link>

        <template v-if="auth.isAdmin">
          <div class="sb-group">管理</div>
          <router-link v-for="it in adminNav" :key="it.to" :to="it.to" class="sb-item" @click="drawer = false">
            <Icon :name="it.icon" :size="17" /><span>{{ it.label }}</span>
          </router-link>
        </template>

        <template v-if="history.length">
          <div class="sb-group">最近拨测</div>
          <TransitionGroup name="list" tag="div">
            <router-link
              v-for="h in history"
              :key="h.id"
              :to="{ name: 'probe', query: { h: h.id } }"
              class="sb-item hist"
              :class="{ 'is-active': route.query.h === h.id }"
              @click="drawer = false"
            >
              <span class="ellipsis">{{ h.raw }}</span>
              <span class="hist-type">{{ h.apiType }}</span>
            </router-link>
          </TransitionGroup>
        </template>
      </nav>

      <div class="sb-bottom" ref="menuRoot">
        <Transition name="pop">
          <div v-if="menu" class="user-menu">
            <div class="um-head">
              <div class="faint">{{ auth.email || auth.username }}</div>
            </div>
            <router-link to="/points" class="um-item" @click="menu = false">
              <Icon name="Coins" :size="16" /> {{ auth.currencyName }}<span class="um-badge">{{ auth.points }}</span>
            </router-link>
            <button class="um-item" @click="openNotices">
              <Icon name="Bell" :size="16" /> 通知<span v-if="unread" class="um-badge dark">{{ unread }}</span>
            </button>
            <router-link to="/profile" class="um-item" @click="menu = false"><Icon name="User" :size="16" /> 个人资料</router-link>
            <div class="um-sep" />
            <div class="um-theme">
              <span class="faint">外观</span>
              <div class="seg mini">
                <button v-for="t in THEMES" :key="t.v" :class="{ on: themeMode === t.v }" :title="t.label" @click="setMode(t.v)">
                  <Icon :name="t.icon" :size="14" />
                </button>
              </div>
            </div>
            <div class="um-sep" />
            <button class="um-item" @click="logout"><Icon name="LogOut" :size="16" /> 退出登录</button>
          </div>
        </Transition>
        <button class="user-row" :class="{ on: menu }" @click="menu = !menu">
          <div class="avatar">{{ (auth.username || '?').slice(0, 1).toUpperCase() }}</div>
          <div class="user-meta">
            <div class="ellipsis">{{ auth.username }}</div>
            <div class="faint">{{ auth.isAdmin ? '管理员' : `${auth.points} ${auth.currencyName}` }}</div>
          </div>
          <span v-if="unread" class="badge-dot" />
        </button>
      </div>
    </aside>
    <Transition name="fade"><div v-if="drawer" class="scrim" @click="drawer = false" /></Transition>

    <main class="main">
      <header class="topbar">
        <button class="btn icon mobile-only" aria-label="打开菜单" @click="drawer = true"><Icon name="PanelLeft" :size="20" /></button>
        <template v-if="collapsed">
          <button class="btn icon desktop-only" aria-label="展开侧边栏" @click="toggleSidebar"><Icon name="PanelLeft" :size="20" /></button>
          <router-link to="/" class="btn icon desktop-only" aria-label="新拨测" @click="newChat"><Icon name="SquarePen" :size="20" /></router-link>
        </template>
        <div class="brand">1IPW-BOCE<span v-if="route.meta.title && route.name !== 'probe'" class="crumb">{{ route.meta.title }}</span></div>
        <div class="spacer" />
        <button class="btn icon" aria-label="通知" @click="openNotices">
          <Icon name="Bell" :size="19" /><span v-if="unread" class="badge-dot top" />
        </button>
      </header>
      <div class="main-scroll">
        <router-view v-slot="{ Component, route: r }">
          <Transition name="route" mode="out-in">
            <component :is="Component" :key="r.name === 'probe' ? 'probe' : r.fullPath" />
          </Transition>
        </router-view>
      </div>
    </main>

    <Drawer v-model="noticesOpen" title="通知">
      <template #actions>
        <button class="btn sm ghost" :disabled="!unread" @click="readAll">全部已读</button>
        <button class="btn sm ghost danger" :disabled="!notices.length" @click="clearAll">清空</button>
      </template>
      <div v-if="!notices.length" class="empty"><Icon name="Bell" :size="28" />暂无通知</div>
      <TransitionGroup name="list" tag="div" class="notices">
        <div v-for="n in notices" :key="n.id" class="notice" :class="{ unread: !n.read }">
          <div class="row between" style="flex-wrap: nowrap">
            <b class="ellipsis">{{ n.title }}</b><span class="faint" style="flex-shrink: 0">{{ fmtTime(n.createdAt) }}</span>
          </div>
          <div class="muted" style="white-space: pre-wrap; font-size: 13px; margin-top: 4px">{{ n.body }}</div>
        </div>
      </TransitionGroup>
    </Drawer>
  </div>
</template>

<script setup>
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useAuthStore } from '../stores/auth.js'
import { useTheme } from '../composables/useTheme.js'
import { useProbeHistory } from '../composables/useProbeHistory.js'
import * as api from '../api/boce.js'
import { fmtTime } from '../utils/format.js'
import Icon from './Icon.vue'
import Drawer from './Drawer.vue'

const auth = useAuthStore()
const route = useRoute()
const router = useRouter()
const { mode: themeMode, setMode } = useTheme()
const { history } = useProbeHistory()

const THEMES = [
  { v: 'light', label: '浅色', icon: 'Sun' },
  { v: 'dark', label: '深色', icon: 'Moon' },
  { v: 'system', label: '跟随系统', icon: 'Monitor' },
]

const collapsed = ref(localStorage.getItem('ipw_sb_collapsed') === '1')
const drawer = ref(false)
const menu = ref(false)
const menuRoot = ref(null)
const noticesOpen = ref(false)
const notices = ref([])
const unread = ref(0)

const mainNav = computed(() => [
  { to: '/dashboard', label: auth.isAdmin ? '统计大盘' : '我的大盘', icon: 'LayoutDashboard' },
  { to: '/sla', label: 'SLA 监控', icon: 'Activity' },
  { to: '/records', label: '拨测明细', icon: 'ScrollText' },
  { to: '/points', label: '积分中心', icon: 'Coins' },
  { to: '/shop', label: '积分商城', icon: 'ShoppingBag' },
  { to: '/donate', label: '捐赠节点', icon: 'HandHeart' },
])
const adminNav = [
  { to: '/nodes', label: '节点管理', icon: 'Server' },
  { to: '/donate-admin', label: '捐赠审核', icon: 'ShieldCheck' },
  { to: '/config', label: '配置托管', icon: 'Puzzle' },
  { to: '/users', label: '用户管理', icon: 'Users' },
  { to: '/points-admin', label: '积分管理', icon: 'Wallet' },
  { to: '/shop-admin', label: '商品与订单', icon: 'Package' },
  { to: '/settings', label: '系统设置', icon: 'Settings' },
]

function toggleSidebar() {
  collapsed.value = !collapsed.value
  localStorage.setItem('ipw_sb_collapsed', collapsed.value ? '1' : '0')
}
function newChat() {
  drawer.value = false
  if (route.name === 'probe') router.replace({ name: 'probe', query: { new: Date.now() } })
}
function logout() {
  menu.value = false
  auth.logout()
  router.push({ name: 'login' })
}
function openNotices() {
  menu.value = false
  noticesOpen.value = true
}
async function loadUnread() {
  try {
    unread.value = (await api.fetchUnread()).unread
  } catch {}
}
async function loadNotices() {
  const d = await api.fetchNotices()
  notices.value = d.list
  unread.value = d.unread
}
async function readAll() {
  unread.value = (await api.markNoticesRead()).unread
  notices.value.forEach((n) => (n.read = true))
}
async function clearAll() {
  await api.clearNotices()
  notices.value = []
  unread.value = 0
}
watch(noticesOpen, (v) => v && loadNotices())

// 点击菜单外部关闭
function onDocClick(e) {
  if (menu.value && menuRoot.value && !menuRoot.value.contains(e.target)) menu.value = false
}

let timer
onMounted(async () => {
  document.addEventListener('mousedown', onDocClick)
  auth.refreshPoints()
  loadUnread()
  timer = setInterval(loadUnread, 60000)
  try {
    const me = await api.fetchMe()
    if (!me.staticToken) auth.refreshMeta(me)
  } catch {}
})
onUnmounted(() => {
  clearInterval(timer)
  document.removeEventListener('mousedown', onDocClick)
})
</script>

<style scoped>
.shell { display: flex; height: 100vh; overflow: hidden; }
.sidebar {
  width: var(--sidebar-w); flex-shrink: 0; background: var(--sidebar);
  display: flex; flex-direction: column; padding: 8px;
  transition: margin-left 0.3s var(--ease), background-color 0.25s var(--ease);
}
.collapsed .sidebar { margin-left: calc(-1 * var(--sidebar-w)); }
.sb-top { display: flex; justify-content: space-between; padding: 2px 2px 10px; }
.sb-nav { flex: 1; overflow-y: auto; display: flex; flex-direction: column; gap: 1px; margin: 0 -4px; padding: 0 4px; }
.sb-item {
  display: flex; align-items: center; gap: 10px; padding: 7px 10px; border-radius: 10px;
  color: var(--text); font-size: 14px; min-width: 0; text-decoration: none;
  transition: background-color 0.15s var(--ease);
}
.sb-item svg { color: var(--text-2); }
.sb-item:hover { background: var(--sidebar-hover); }
.sb-item.router-link-exact-active:not(.hist), .sb-item.is-active { background: var(--sidebar-active); }
.sb-item.hist { justify-content: space-between; }
.hist-type { font-family: var(--mono); font-size: 11px; color: var(--text-3); opacity: 0; transition: opacity 0.15s; }
.sb-item.hist:hover .hist-type { opacity: 1; }
.ellipsis { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; min-width: 0; }
.sb-group { font-size: 12px; color: var(--text-3); padding: 18px 10px 6px; font-weight: 500; }

.sb-bottom { position: relative; padding-top: 6px; }
.user-row {
  display: flex; align-items: center; gap: 10px; padding: 8px; border-radius: 12px; cursor: pointer; width: 100%;
  border: none; background: transparent; color: var(--text); font: inherit; text-align: left; position: relative;
  transition: background-color 0.15s var(--ease);
}
.user-row:hover, .user-row.on { background: var(--sidebar-hover); }
.avatar {
  width: 32px; height: 32px; border-radius: 50%; background: var(--primary); color: var(--primary-fg);
  display: flex; align-items: center; justify-content: center; font-weight: 600; font-size: 14px; flex-shrink: 0;
}
.user-meta { min-width: 0; flex: 1; font-size: 14px; line-height: 1.3; }
.badge-dot { position: absolute; width: 8px; height: 8px; border-radius: 50%; background: var(--danger); right: 12px; top: 50%; margin-top: -4px; }
.badge-dot.top { right: 6px; top: 7px; margin: 0; }

.user-menu {
  position: absolute; bottom: calc(100% + 4px); left: 0; right: 0; background: var(--bg-elev);
  border: 1px solid var(--border); border-radius: 16px; box-shadow: var(--shadow-lg); padding: 6px; z-index: 30;
  transform-origin: bottom center;
}
.um-head { padding: 8px 12px 6px; }
.um-item {
  display: flex; align-items: center; gap: 10px; width: 100%; text-align: left; padding: 9px 12px;
  border: none; background: none; color: var(--text); font: inherit; font-size: 14px; border-radius: 10px;
  cursor: pointer; text-decoration: none; transition: background-color 0.12s;
}
.um-item svg { color: var(--text-2); }
.um-item:hover { background: var(--bg-hover); }
.um-badge { margin-left: auto; font-size: 12px; color: var(--text-2); font-variant-numeric: tabular-nums; }
.um-badge.dark { background: var(--danger); color: #fff; border-radius: 999px; padding: 0 7px; }
.um-sep { height: 1px; background: var(--border); margin: 6px 4px; }
.um-theme { display: flex; align-items: center; justify-content: space-between; padding: 4px 6px 4px 12px; }
.seg.mini button { padding: 4px 9px; display: inline-flex; }

.main { flex: 1; min-width: 0; display: flex; flex-direction: column; background: var(--bg); transition: background-color 0.25s var(--ease); }
.topbar { height: 52px; display: flex; align-items: center; gap: 4px; padding: 0 10px; flex-shrink: 0; }
.brand { font-weight: 600; font-size: 17px; display: flex; gap: 8px; align-items: baseline; padding: 0 6px; }
.crumb { font-size: 14px; font-weight: 400; color: var(--text-3); }
.crumb::before { content: '/'; margin-right: 8px; color: var(--border-strong); }
.main-scroll { flex: 1; overflow-y: auto; overflow-x: hidden; }
.scrim { display: none; }
.mobile-only { display: none; }

.notices { display: flex; flex-direction: column; gap: 8px; }
.notice { padding: 12px 14px; border-radius: 14px; background: var(--bg-soft); position: relative; }
.notice.unread::before { content: ''; position: absolute; left: 0; top: 14px; bottom: 14px; width: 3px; border-radius: 3px; background: var(--text); }

.pop-enter-active, .pop-leave-active { transition: opacity 0.16s var(--ease), transform 0.16s var(--ease); }
.pop-enter-from, .pop-leave-to { opacity: 0; transform: translateY(6px) scale(0.97); }
.fade-enter-active, .fade-leave-active { transition: opacity 0.2s; }
.fade-enter-from, .fade-leave-to { opacity: 0; }

@media (max-width: 860px) {
  .sidebar { position: fixed; z-index: 50; top: 0; bottom: 0; left: 0; margin-left: calc(-1 * var(--sidebar-w)); box-shadow: var(--shadow-lg); }
  .sidebar.open, .collapsed .sidebar.open { margin-left: 0; }
  .scrim { display: block; position: fixed; inset: 0; background: var(--scrim); z-index: 40; }
  .mobile-only { display: inline-flex; }
  .desktop-only { display: none !important; }
}
</style>
