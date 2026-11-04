<template>
  <div class="page wide">
    <div class="page-head">
      <div><h1>用户管理</h1><div class="sub">共 {{ users.length }} 个账号</div></div>
      <button class="btn primary" @click="createOpen = true">+ 新建用户</button>
    </div>
    <div class="table-wrap">
      <table class="tbl">
        <thead><tr><th>ID</th><th>用户名</th><th>邮箱</th><th>角色</th><th>积分</th><th>Token</th><th>启用</th><th>创建</th><th /></tr></thead>
        <tbody>
          <tr v-for="u in users" :key="u.id">
            <td>{{ u.id }}</td><td><b>{{ u.username }}</b></td>
            <td>{{ u.email || '—' }}<span v-if="u.email && !u.emailVerified" class="pill warn">未验证</span></td>
            <td>
              <select :value="u.role" class="inp" style="width: 90px; padding: 3px 8px" @change="(e) => patch(u, { role: e.target.value })">
                <option value="user">user</option><option value="admin">admin</option>
              </select>
            </td>
            <td class="mono">{{ u.points }}</td>
            <td class="faint">{{ u.hasApiToken ? `ipt_…${u.apiTokenHint}` : '—' }}</td>
            <td><Switch :model-value="u.enabled" @change="(v) => patch(u, { enabled: v })" /></td>
            <td class="faint">{{ fmtTime(u.createdAt) }}</td>
            <td><button class="btn sm" @click="resetPw(u)">重置密码</button><button class="btn sm danger" @click="del(u)">删除</button></td>
          </tr>
        </tbody>
      </table>
    </div>

    <Modal v-model="createOpen" width="min(440px, 92vw)" title="新建用户">
      <div>
        <div class="field"><label>用户名</label><input v-model.trim="nu.username" class="inp" /></div>
        <div class="field"><label>密码（≥6 位）</label><input v-model="nu.password" type="password" class="inp" /></div>
        <div class="field"><label>邮箱（选填）</label><input v-model.trim="nu.email" class="inp" /></div>
        <div class="field"><label>角色</label><select v-model="nu.role" class="inp"><option value="user">普通用户</option><option value="admin">管理员</option></select></div>
        <div class="row" style="justify-content: flex-end"><button class="btn" @click="createOpen = false">取消</button><button class="btn primary" @click="create">创建</button></div>
      </div>
    </Modal>

    <Modal v-model="pwOpen" width="min(400px, 92vw)" :title="'重置 ' + (pwUser?.username || '') + ' 的密码'">
      <div v-if="pwUser">
        <div class="field"><input v-model="pw" type="password" class="inp" placeholder="新密码（≥6 位）" /></div>
        <div class="row" style="justify-content: flex-end"><button class="btn" @click="pwOpen = false">取消</button><button class="btn primary" @click="doReset">确定</button></div>
      </div>
    </Modal>
  </div>
</template>

<script setup>
import Modal from '../components/Modal.vue'
import Switch from '../components/Switch.vue'
import { onMounted, reactive, ref } from 'vue'
import * as api from '../api/boce.js'
import { fmtTime } from '../utils/format.js'
import { confirm, run } from '../utils/toast.js'

const users = ref([])
const createOpen = ref(false)
const nu = reactive({ username: '', password: '', email: '', role: 'user' })
const pwOpen = ref(false)
const pwUser = ref(null)
const pw = ref('')

const load = async () => (users.value = await api.fetchUsers())
async function create() {
  await run(() => api.createUser({ ...nu }), '已创建')
  createOpen.value = false
  Object.assign(nu, { username: '', password: '', email: '', role: 'user' })
  load()
}
async function patch(u, p) {
  try {
    await run(() => api.updateUser(u.id, p), '已更新')
  } finally {
    load()
  }
}
function resetPw(u) {
  pwUser.value = u
  pw.value = ''
  pwOpen.value = true
}
async function doReset() {
  await run(() => api.resetUserPassword(pwUser.value.id, pw.value), '已重置')
  pwOpen.value = false
}
async function del(u) {
  if (!(await confirm(`删除用户 ${u.username}？其任务、站内信与积分账户会一并删除。`))) return
  await run(() => api.deleteUser(u.id), '已删除')
  load()
}
onMounted(load)
</script>

