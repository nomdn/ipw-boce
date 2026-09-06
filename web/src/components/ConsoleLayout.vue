<template>
  <div class="console">
    <!-- 侧边导航 -->
    <aside class="console-aside">
      <div class="console-brand">
        <h1 class="brand-title">ipw-boce</h1>
        <p class="brand-sub">拨测收集中心 · 控制台</p>
      </div>

      <nav class="console-nav">
        <router-link v-for="it in navs" :key="it.to" :to="it.to" class="nav-item" :title="it.tip">
          <!-- 图标：svg 优先（避免 emoji 跨平台渲染差异），否则回退文本字符 -->
          <span class="nav-ico nav-ico--svg" v-if="it.svg" v-html="it.svg"></span>
          <span class="nav-ico" v-else>{{ it.ico }}</span>
          <span>{{ it.label }}</span>
        </router-link>
      </nav>

      <div class="console-foot">
        <span class="dim" title="登录用户">{{ auth.username || 'admin' }}<i v-if="auth.role" class="role-pill">{{ auth.role }}</i></span>
        <button class="ak-button ak-button--outline" style="padding:4px 10px;font-size:.75rem" @click="onLogout">退出</button>
      </div>
    </aside>

    <!-- 主区 -->
    <div class="console-main">
      <header class="console-topbar">
        <div class="crumb">{{ route.meta.title || '' }}</div>
        <div class="top-actions">
          <!-- 站内信铃铛（仅 JWT 登录用户可见；静态 token 无可视用户则不显示） -->
          <div v-if="auth.userId" class="notice-wrap" ref="noticeWrap">
            <button class="bell-btn" :class="{ has: unread > 0 }" @click="toggleNotices" title="站内信">
              <span class="bell-ico" v-html="bellSvg"></span>
              <span v-if="unread > 0" class="bell-badge">{{ unread > 99 ? '99+' : unread }}</span>
            </button>

            <div v-if="open" class="notice-panel">
              <div class="notice-head">
                <span>站内信</span>
                <div class="np-ops">
                  <button v-if="list.some((n) => !n.read)" class="link" @click="readAll">全部已读</button>
                  <button v-if="list.length" class="link danger" @click="clearAll">清空</button>
                </div>
              </div>
              <div class="notice-body">
                <div v-if="!loading && !list.length" class="empty">暂无通知</div>
                <div v-for="n in list" :key="n.id" class="notice-item" :class="{ unread: !n.read }" @click="viewNotice(n)">
                  <div class="ni-title"><span class="kind-tag">{{ kindText(n.kind) }}</span>{{ n.title }}<span class="ni-time">{{ fmtAgo(n.createdAt) }}</span></div>
                  <div v-if="n.body" class="ni-body">{{ shortBody(n.body) }}</div>
                </div>
              </div>
              <div class="notice-foot">
                <span class="dim">掉线告警（所有者未配置邮箱时在此显示）</span>
              </div>
            </div>
          </div>

          <span class="ak-tag ch" :class="onlineCtl.tagClass">{{ onlineCtl.text }}</span>
        </div>
      </header>

      <!-- 邮箱待验证横幅：仅普通账号(填了邮箱未验证)显示；admin 无此约束 -->
      <div v-if="auth.emailPending" class="verify-banner">
        <span>⚠ 你的邮箱尚未验证，SLA 监控与建/启停定时拨测任务暂不可用。</span>
        <button class="ak-button ak-button--outline sm" @click="goVerify">去验证邮箱</button>
      </div>

      <main class="console-body">
        <router-view />
      </main>
    </div>
  </div>
</template>

<script setup>
import { ref, onMounted, onBeforeUnmount, computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useAuthStore } from '../stores/auth.js'
import { http } from '../api/http.js'
import { fetchNotices, fetchUnreadCount, markNoticesRead, clearNotices } from '../api/boce.js'
import { useDialog } from '../composables/useDialog.js'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()
const dialog = useDialog()

const boltSvg = '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M13 2 3.6 13.6H10L8.5 22l9.9-11.9H12L13 2z" fill="currentColor"/></svg>'
const usersSvg = '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 12a4 4 0 1 0 0-8 4 4 0 0 0 0 8zm0 2c-4 0-8 2-8 5v1h16v-1c0-3-4-5-8-5z" fill="currentColor"/></svg>'
const userSvg = '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 12a4 4 0 1 0 0-8 4 4 0 0 0 0 8zm0 2c-5 0-9 2.5-9 6v1h18v-1c0-3.5-4-6-9-6z" fill="currentColor"/></svg>'
const bellSvg = '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 22a2 2 0 0 0 2-2h-4a2 2 0 0 0 2 2zm6-6v-5a6 6 0 0 0-4.5-5.8V4.5a1.5 1.5 0 1 0-3 0v.7A6 6 0 0 0 6 11v5l-2 2v1h16v-1l-2-2z" fill="currentColor"/></svg>'

const allNavs = [
  { to: '/', label: '统计大盘', ico: '◫', tip: '汇总 / 按类型 / 按节点趋势', admin: false },
  { to: '/nodes', label: '节点状态', ico: '❖', tip: '在线节点 / 事件历史', admin: true },
  { to: '/probe', label: '一键拨测', svg: boltSvg, tip: '对全部或指定节点批量拨测', admin: false },
  { to: '/records', label: '拨测明细', ico: '≣', tip: '最近 probe_results', admin: false },
  { to: '/sla', label: 'SLA 监控', ico: '◔', tip: '定时拨测可用率 / 延迟 / 达标', admin: false },
  { to: '/config', label: '配置分发', ico: '⚙', tip: '节点远端配置托管', admin: true },
  { to: '/users', label: '用户管理', svg: usersSvg, tip: '管理控制台账号 / 角色 / 告警邮箱', admin: true },
  { to: '/profile', label: '个人资料', svg: userSvg, tip: '管理本人邮箱 / 密码', admin: false },
]

// admin-only 页面（节点/配置/用户）仅 admin 可见；其余登录可见
const navs = computed(() => allNavs.filter((it) => !it.admin || auth.isAdmin))

// 顶栏：健康心跳（轮询 /admin/status，仅显示在线节点数，不阻塞）
const wsPeers = ref(0)
let timer = null
const onlineCtl = computed(() => ({
  text: `WS 在线 ${wsPeers.value}`,
  tagClass: wsPeers.value > 0 ? 'ak-tag--advanced' : 'ak-tag--neutral',
}))

async function pollStatus() {
  try {
    const s = await http.get('/admin/status')
    wsPeers.value = s?.wsPeers ?? 0
  } catch {
    /* 状态接口失败静默 */
  }
}

// 未验证邮箱 → 跳到个人资料的"验证邮箱"区
function goVerify() {
  router.push({ name: 'profile', query: { verify: '1' } })
}

// ===== 站内信（铃铛下拉）=====
const open = ref(false)
const list = ref([])
const unread = ref(0)
const loading = ref(false)
let noticeTimer = null

async function refreshUnread() {
  if (!auth.userId) return
  try {
    const r = await fetchUnreadCount()
    unread.value = r?.unread ?? 0
  } catch {
    /* 静默 */
  }
}
async function loadNotices() {
  if (!auth.userId) return
  loading.value = true
  try {
    const r = await fetchNotices(50)
    list.value = r?.list || []
    unread.value = r?.unread ?? 0
  } catch {
    /* 静默 */
  } finally {
    loading.value = false
  }
}
async function toggleNotices() {
  open.value = !open.value
  if (open.value) loadNotices()
}
// 全部已读
async function readAll() {
  const r = await markNoticesRead()
  if (r) unread.value = r.unread ?? 0
  list.value = list.value.map((n) => ({ ...n, read: true }))
}
// 点一条 → 标已读
async function readOne(n) {
  if (n.read) return
  const r = await markNoticesRead([n.id])
  if (r) unread.value = r.unread ?? 0
  n.read = true
}
// 点一条 → 弹窗显示完整内容 + 标已读
async function viewNotice(n) {
  await readOne(n)
  const title = n.title || kindText(n.kind) || '通知'
  const time = n.createdAt ? new Date(n.createdAt).toLocaleString() : ''
  // 正文 = 完整 body（不再截断），前附发送时间便于溯源
  const body = (time ? `时间：${time}\n\n` : '') + (n.body || '（无正文）')
  dialog.open({
    title,
    kind: 'info',
    actions: true,
    showCancel: false,
    confirmText: '知道了',
    closable: true,
    overlayClose: true,
    message: body,
  })
}
// 通知类别文案（与后端 noticeKind 对应）
function kindText(kind) {
  return { node_down: '节点掉线', sla_down: '任务掉线' }[kind] || '通知'
}
// 清空
async function clearAll() {
  const ok = await dialog.confirm({
    title: '清空全部站内信？',
    message: '将删除所有通知记录，此操作不可恢复。',
    kind: 'danger',
    confirmText: '清空',
    cancelText: '取消',
  })
  if (!ok) return
  await clearNotices()
  list.value = []
  unread.value = 0
}
// 时间/正文显示
function fmtAgo(iso) {
  if (!iso) return ''
  const d = new Date(iso)
  const s = Math.floor((Date.now() - d.getTime()) / 1000)
  if (s < 60) return '刚刚'
  if (s < 3600) return `${Math.floor(s / 60)} 分钟前`
  if (s < 86400) return `${Math.floor(s / 3600)} 小时前`
  return `${Math.floor(s / 86400)} 天前`
}
function shortBody(b) {
  return b.length > 180 ? b.slice(0, 180) + '…' : b
}
// 点击外部关闭下拉
function onDocClick(e) {
  if (open.value && noticeWrap.value && !noticeWrap.value.contains(e.target)) open.value = false
}

const noticeWrap = ref(null)
onMounted(() => {
  pollStatus()
  timer = setInterval(pollStatus, 20000)
  if (auth.userId) {
    refreshUnread()
    noticeTimer = setInterval(refreshUnread, 30000)
  }
  document.addEventListener('click', onDocClick)
})
onBeforeUnmount(() => {
  clearInterval(timer)
  if (noticeTimer) clearInterval(noticeTimer)
  document.removeEventListener('click', onDocClick)
})

function onLogout() {
  auth.logout()
  router.push({ name: 'login' })
}
</script>

<style scoped>
.verify-banner {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 8px 16px;
  margin: 0 0 0;
  background: color-mix(in srgb, var(--ak-signal-danger, #e33b3b) 14%, transparent);
  border-bottom: 1px solid color-mix(in srgb, var(--ak-signal-danger, #e33b3b) 30%, transparent);
  color: var(--ak-text, #f0ece2);
  font-size: 0.82rem;
}
.verify-banner .ak-button {
  margin-left: auto;
}
</style>
