<template>
  <div>
    <div class="panel-head">
      <div>
        <h2 class="panel-title">控制台用户</h2>
        <p class="panel-sub">账号 / 角色 / 状态 / 告警邮箱。仅 admin 可在此管理；普通用户看不到本页。</p>
      </div>
      <button class="ak-button ak-button--action" @click="openCreate">＋ 新增用户</button>
    </div>

    <div class="panel">
      <div class="ak-table-wrap">
        <table class="ak-table">
          <thead>
            <tr>
              <th>用户名</th><th>角色</th><th>状态</th><th>告警邮箱</th><th>创建时间</th><th style="width:170px">操作</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="u in users" :key="u.id">
              <td class="mono">
                {{ u.username }}
                <span v-if="isMe(u.id)" class="me-tag">当前</span>
              </td>
              <td>
                <span class="ak-tag ch" :class="u.role === 'admin' ? 'ak-tag--advanced' : 'ak-tag--neutral'">
                  {{ u.role === 'admin' ? 'admin' : 'user' }}
                </span>
              </td>
              <td>
                <button class="ak-toggle" :class="{ on: u.enabled }" @click="toggleEnabled(u)">
                  {{ u.enabled ? '启用' : '禁用' }}
                </button>
              </td>
              <td class="mono" :title="u.email">{{ u.email || '—' }}
                <span v-if="u.email" class="vtag" :class="u.emailVerified ? 'vok' : 'vno'" :title="u.emailVerified ? '邮箱已验证' : '邮箱未验证（该用户登录后需验证，否则 SLA 受限）'">
                  {{ u.emailVerified ? '✓' : '待验证' }}
                </span>
              </td>
              <td class="mono nowrap dim">{{ fmtTime(u.createdAt) }}</td>
              <td class="ops">
                <button class="link-btn" @click="openEdit(u)">编辑</button>
                <button class="link-btn danger" :disabled="isMe(u.id)" @click="openReset(u)">重置密码</button>
                <button class="link-btn danger" :disabled="isMe(u.id)" @click="confirmDelete(u)">删除</button>
              </td>
            </tr>
            <tr v-if="!loading && !users.length">
              <td colspan="6" class="dim">暂无用户。首个 admin 会在服务启动时由 admin-user/admin-password 自动创建。</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <!-- 新增 / 编辑对话框 -->
    <div v-if="dialog.show" class="mask" @click.self="closeDialog">
      <div class="dlg">
        <div class="dlg-title">{{ dialog.isEdit ? '编辑用户' : '新增用户' }}</div>
        <div class="ak-form-stack">
          <label class="ak-field">
            <span class="ak-label">用户名</span>
            <input class="ak-input" v-model.trim="dialog.form.username" :disabled="dialog.isEdit"
              placeholder="登录名（唯一）" />
          </label>
          <label class="ak-field" v-if="!dialog.isEdit">
            <span class="ak-label">初始口令</span>
            <input class="ak-input" type="password" v-model="dialog.form.password"
              placeholder="至少 6 位" autocomplete="new-password" />
          </label>
          <label class="ak-field">
            <span class="ak-label">告警邮箱</span>
            <input class="ak-input" v-model.trim="dialog.form.email"
              placeholder="用于接收 SLA 掉线告警（可选）" />
          </label>
          <label class="ak-field">
            <span class="ak-label">角色</span>
            <select class="ak-select" v-model="dialog.form.role" :disabled="dialog.isEdit && isMe(dialog.id)">
              <option value="user">user（普通用户）</option>
              <option value="admin">admin（管理员）</option>
            </select>
          </label>
        </div>
        <div class="dlg-err" v-if="dialog.err">{{ dialog.err }}</div>
        <div class="dlg-foot">
          <button class="ak-button ak-button--outline" @click="closeDialog">取消</button>
          <button class="ak-button ak-button--action" :disabled="dialog.busy" @click="saveDialog">
            {{ dialog.busy ? '保存中…' : '保存' }}
          </button>
        </div>
      </div>
    </div>

    <!-- 重置口令对话框 -->
    <div v-if="pwDlg.show" class="mask" @click.self="pwDlg.show = false">
      <div class="dlg">
        <div class="dlg-title">重置口令 · {{ pwDlg.user?.username }}</div>
        <div class="ak-form-stack">
          <label class="ak-field">
            <span class="ak-label">新口令</span>
            <input class="ak-input" type="password" v-model="pwDlg.password"
              placeholder="至少 6 位" autocomplete="new-password" @keyup.enter="saveReset" />
          </label>
        </div>
        <div class="dlg-err" v-if="pwDlg.err">{{ pwDlg.err }}</div>
        <div class="dlg-foot">
          <button class="ak-button ak-button--outline" @click="pwDlg.show = false">取消</button>
          <button class="ak-button ak-button--action" :disabled="pwDlg.busy" @click="saveReset">
            {{ pwDlg.busy ? '重置中…' : '确认重置' }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, reactive, onMounted } from 'vue'
import { fetchUsers, createUser, updateUser, resetUserPassword, deleteUser } from '../api/boce.js'
import { useAuthStore } from '../stores/auth.js'
import { fmtTime } from '../utils/format.js'
import { useDialog } from '../composables/useDialog.js'

const auth = useAuthStore()
const uiDialog = useDialog()
const users = ref([])
const loading = ref(false)

const emptyForm = { username: '', password: '', email: '', role: 'user' }
const dialog = reactive({ show: false, isEdit: false, id: 0, form: { ...emptyForm }, err: '', busy: false })
const pwDlg = reactive({ show: false, user: null, password: '', err: '', busy: false })

function isMe(id) { return auth.userId != null && String(auth.userId) === String(id) }

onMounted(load)

async function load() {
  loading.value = true
  try { users.value = await fetchUsers() } catch { users.value = [] } finally { loading.value = false }
}

function openCreate() {
  dialog.show = true
  dialog.isEdit = false
  dialog.id = 0
  dialog.form = { ...emptyForm }
  dialog.err = ''
}

function openEdit(u) {
  dialog.show = true
  dialog.isEdit = true
  dialog.id = u.id
  dialog.form = { username: u.username, email: u.email || '', role: u.role }
  dialog.err = ''
}

function closeDialog() {
  if (dialog.busy) return
  dialog.show = false
}

async function saveDialog() {
  dialog.err = ''
  if (!dialog.isEdit && (!dialog.form.username || dialog.form.password.length < 6)) {
    dialog.err = '用户名必填，口令至少 6 位'
    return
  }
  dialog.busy = true
  try {
    if (dialog.isEdit) {
      await updateUser(dialog.id, { email: dialog.form.email, role: dialog.form.role })
    } else {
      await createUser({
        username: dialog.form.username,
        password: dialog.form.password,
        email: dialog.form.email,
        role: dialog.form.role,
      })
    }
    dialog.show = false
    await load()
  } catch (e) {
    dialog.err = e?.message || '保存失败'
  } finally {
    dialog.busy = false
  }
}

async function toggleEnabled(u) {
  const next = !u.enabled
  // 防止禁用当前 admin（后端也有保护）
  if (!next && isMe(u.id)) return
  try {
    await updateUser(u.id, { enabled: next })
    await load()
  } catch (e) { /* 后端已给错误提示；此处静默 */ }
}

function openReset(u) {
  pwDlg.show = true
  pwDlg.user = u
  pwDlg.password = ''
  pwDlg.err = ''
}

async function saveReset() {
  pwDlg.err = ''
  if (pwDlg.password.length < 6) { pwDlg.err = '新口令至少 6 位'; return }
  pwDlg.busy = true
  try {
    await resetUserPassword(pwDlg.user.id, pwDlg.password)
    pwDlg.show = false
  } catch (e) {
    pwDlg.err = e?.message || '重置失败'
  } finally {
    pwDlg.busy = false
  }
}

async function confirmDelete(u) {
  const ok = await uiDialog.confirm({
    title: `删除用户 ${u.username}？`,
    message: '将同时删除其创建的定时拨测任务与站内信，此操作不可恢复。',
    kind: 'danger',
    confirmText: '删除',
    cancelText: '取消',
  })
  if (!ok) return
  try {
    await deleteUser(u.id)
    await load()
  } catch (e) { /* 后端已给错误提示 */ }
}
</script>

<style scoped>
.panel-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 16px;
}
.panel-title { margin: 0; font-size: 1.05rem; font-family: var(--ak-font-command); }
.panel-sub { margin: 4px 0 0; color: var(--ak-text-secondary); font-size: 0.82rem; }

.me-tag {
  margin-left: 6px;
  font-size: 0.6rem;
  padding: 1px 5px;
  border-radius: 3px;
  background: var(--ak-signal-info);
  color: #fff;
  vertical-align: 1px;
}

.ops { white-space: nowrap; }
.link-btn {
  background: none;
  border: none;
  color: var(--ak-signal-info);
  cursor: pointer;
  font-size: 0.78rem;
  padding: 2px 6px;
}
.link-btn.danger { color: var(--ak-signal-danger); }
.link-btn:disabled { opacity: 0.35; cursor: not-allowed; }

.vtag {
  font-size: 0.68rem;
  margin-left: 6px;
  padding: 1px 5px;
  border-radius: 4px;
  letter-spacing: 0.02em;
  vertical-align: 1px;
}
.vtag.vok { color: var(--ak-signal-success, #46c47c); border: 1px solid currentColor; }
.vtag.vno { color: var(--ak-signal-warn, #ffab00); border: 1px solid currentColor; }

.ak-toggle {
  border: var(--ak-line-hairline) solid rgba(0, 0, 0, 0.15);
  background: transparent;
  color: var(--ak-text-secondary);
  font-size: 0.72rem;
  padding: 2px 10px;
  border-radius: 10px;
  cursor: pointer;
}
.ak-toggle.on { color: var(--ak-signal-success); border-color: currentColor; }

.mask {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.45);
  display: flex;
  align-items: flex-start;
  justify-content: center;
  padding-top: 12vh;
  z-index: 50;
}
.dlg {
  width: min(420px, calc(100vw - 40px));
  background: var(--ak-surface-raised);
  border: var(--ak-line-hairline) solid rgba(0, 0, 0, 0.12);
  border-top: 3px solid var(--ak-signal-info);
  padding: 20px 22px;
  box-shadow: 0 20px 50px rgba(0, 0, 0, 0.35);
}
.dlg-title { font-family: var(--ak-font-command); font-weight: 600; margin-bottom: 14px; }
.dlg-err { margin-top: 10px; color: var(--ak-signal-danger); font-size: 0.8rem; }
.dlg-foot { display: flex; justify-content: flex-end; gap: 10px; margin-top: 18px; }
</style>
