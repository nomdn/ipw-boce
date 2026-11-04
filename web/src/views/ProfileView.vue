<template>
  <div class="page">
    <div class="page-head"><div><h1>个人资料</h1><div class="sub">{{ me?.username }} · {{ me?.role === 'admin' ? '管理员' : '普通用户' }}</div></div></div>

    <div v-if="me?.staticToken" class="card soft">当前使用静态 admin-token 登录，没有可编辑的账号。</div>
    <template v-else-if="me">
      <div class="card" :class="{ highlight: route.query.verify }">
        <h3>邮箱</h3>
        <div class="row">
          <input v-model.trim="email" class="inp" style="flex: 1; min-width: 200px" placeholder="you@example.com" />
          <button class="btn" @click="saveEmail">保存</button>
        </div>
        <div v-if="me.email && !me.emailVerified" class="row" style="margin-top: 10px">
          <span class="pill warn">未验证</span>
          <button class="btn sm" :disabled="cooldown > 0" @click="sendCode">{{ cooldown > 0 ? `${cooldown}s` : '发送验证码' }}</button>
          <input v-model.trim="code" class="inp" style="width: 140px" placeholder="验证码" />
          <button class="btn sm accent" :disabled="!code" @click="verify">验证</button>
        </div>
        <div v-else-if="me.email" class="row" style="margin-top: 8px; font-size: 13px; gap: 6px"><Icon name="CircleCheck" :size="15" />已验证</div>
      </div>

      <div class="card">
        <h3>通知 Webhook</h3>
        <div class="grid c2">
          <div class="field"><label>地址</label><input v-model.trim="wh.url" class="inp" placeholder="https://" /></div>
          <div class="field"><label>格式</label><select v-model="wh.type" class="inp"><option value="generic">通用 JSON</option><option value="wecom">企业微信 / 钉钉</option><option value="feishu">飞书</option></select></div>
        </div>
        <div class="row"><button class="btn" @click="saveWh">保存</button><button class="btn" :disabled="!me.webhookUrl" @click="testWh">发送测试</button></div>
      </div>

      <div class="card">
        <h3>个人 API Token</h3>
        <p class="muted" style="font-size: 13px">用于脚本调用 <code>/api/v1/*</code>，权限与本账号一致，不受登录过期影响。重新生成会让旧 Token 立即失效。</p>
        <div class="row">
          <span class="mono">{{ me.hasApiToken ? `ipt_…${me.apiTokenHint}` : '未生成' }}</span>
          <button class="btn" @click="genToken">{{ me.hasApiToken ? '重新生成' : '生成' }}</button>
          <button v-if="me.hasApiToken" class="btn danger" @click="revoke">吊销</button>
        </div>
        <div v-if="newToken" class="card soft" style="margin-top: 10px">
          <div class="faint">只显示这一次，请妥善保存</div>
          <div class="row"><code style="word-break: break-all">{{ newToken }}</code><button class="btn sm" @click="copy(newToken)">复制</button></div>
        </div>
      </div>

      <div class="card">
        <h3>修改密码</h3>
        <div class="grid c2">
          <input v-model="pw.old" type="password" class="inp" placeholder="原密码" />
          <input v-model="pw.new" type="password" class="inp" placeholder="新密码（≥6 位）" />
        </div>
        <button class="btn primary" style="margin-top: 10px" :disabled="!pw.old || !pw.new" @click="changePw">修改</button>
      </div>
    </template>
  </div>
</template>

<script setup>
import { onMounted, reactive, ref } from 'vue'
import { useRoute } from 'vue-router'
import * as api from '../api/boce.js'
import { useAuthStore } from '../stores/auth.js'
import { copyText } from '../utils/format.js'
import { confirm, run, toastOk } from '../utils/toast.js'
import Icon from '../components/Icon.vue'

const route = useRoute()
const auth = useAuthStore()
const me = ref(null)
const email = ref('')
const code = ref('')
const cooldown = ref(0)
const wh = reactive({ url: '', type: 'generic' })
const newToken = ref('')
const pw = reactive({ old: '', new: '' })

async function load() {
  me.value = await api.fetchMe()
  email.value = me.value.email || ''
  wh.url = me.value.webhookUrl || ''
  wh.type = me.value.webhookType || 'generic'
  if (!me.value.staticToken) auth.refreshMeta(me.value)
}
async function saveEmail() {
  await run(() => api.updateMe({ email: email.value }), '已保存')
  load()
}
async function sendCode() {
  await run(() => api.sendVerifyCode(), '验证码已发送')
  cooldown.value = 60
  const t = setInterval(() => --cooldown.value <= 0 && clearInterval(t), 1000)
}
async function verify() {
  await run(() => api.submitVerifyCode(code.value), '邮箱已验证')
  code.value = ''
  load()
}
async function saveWh() {
  await run(() => api.updateMe({ webhookUrl: wh.url, webhookType: wh.type }), '已保存')
  load()
}
const testWh = () => run(() => api.testWebhook(), '测试消息已送达')
async function genToken() {
  if (me.value.hasApiToken && !(await confirm('重新生成后旧 Token 立即失效，确定？'))) return
  newToken.value = (await run(() => api.createMyToken())).token
  load()
}
async function revoke() {
  if (!(await confirm('吊销后使用该 Token 的脚本将无法访问，确定？'))) return
  await run(() => api.revokeMyToken(), '已吊销')
  newToken.value = ''
  load()
}
async function changePw() {
  await run(() => api.changePassword(pw.old, pw.new), '密码已修改')
  pw.old = pw.new = ''
}
async function copy(t) {
  if (await copyText(t)) toastOk('已复制')
}
onMounted(load)
</script>

<style scoped>
.highlight { border-color: var(--warn); }
</style>
