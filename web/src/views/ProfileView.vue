<template>
  <div>
    <div v-if="loading" class="loading-center"><span class="ak-loading"></span></div>
    <div v-else class="profile-wrap">
      <!-- 账号信息卡 -->
      <section class="panel" style="max-width:640px;margin-bottom:16px">
        <h2 class="panel-title">账号信息</h2>
        <div class="acc-grid">
          <div class="acc-item"><div class="acc-key">用户名</div><div class="acc-val mono">{{ me.username || '—' }}</div></div>
          <div class="acc-item"><div class="acc-key">角色</div><div class="acc-val"><span class="ak-tag ch">{{ me.role || '—' }}</span></div></div>
          <div class="acc-item"><div class="acc-key">账号 ID</div><div class="acc-val mono">{{ me.id || '—' }}</div></div>
          <div class="acc-item"><div class="acc-key">注册时间</div><div class="acc-val">{{ fmtTime(me.createdAt) }}</div></div>
          <div class="acc-item acc-wide"><div class="acc-key">邮箱（用于接收任务掉线告警）</div>
            <div class="acc-val mono">{{ me.email || '未设置' }}</div>
          </div>
        </div>
      </section>

      <!-- 无账号的静态 token 提示 -->
      <div v-if="me.staticToken" class="panel" style="max-width:640px;margin-bottom:16px">
        <p class="dim" style="margin:0">当前使用管理员静态 Token 登录，无可管理的账号资料。请改用「用户名 + 密码」登录，以修改邮箱与密码。</p>
      </div>

      <div v-if="!me.staticToken" class="profile-cols">
        <!-- 邮箱验证（未验证/换新邮箱后） -->
        <section class="panel verify-panel">
          <h2 class="panel-title">邮箱验证</h2>
          <template v-if="me.email">
            <p class="dim" style="margin:0 0 8px">
              <span v-if="me.emailVerified" class="ok-200">✓ 邮箱已验证</span>
              <span v-else class="err">当前邮箱未验证，SLA 监控与定时拨测暂不可用。</span>
              <span class="dim">（{{ me.email }}）</span>
            </p>
            <template v-if="!me.emailVerified">
              <div class="code-row" style="max-width:360px">
                <input class="ak-input" v-model.trim="vcode" inputmode="numeric" maxlength="6" placeholder="6 位验证码" style="flex:1" />
                <button class="ak-button ak-button--outline" :disabled="vsending || vcd > 0" @click="sendCode">
                  {{ vsending ? '发送中…' : vcd > 0 ? `重新获取 ${vcd}s` : '获取验证码' }}
                </button>
              </div>
              <button class="ak-button ak-button--action" style="margin-top:10px" :disabled="saving" @click="doVerify">提交验证</button>
              <div v-if="verifyMsg" :class="verifyErr ? 'err' : 'ok-200'" style="margin-top:8px;font-size:.8rem">{{ verifyMsg }}</div>
            </template>
          </template>
          <p v-else class="dim" style="margin:0">请先在「修改邮箱」里设置邮箱，再进行验证。</p>
        </section>

        <!-- 修改邮箱 -->
        <section class="panel">
          <h2 class="panel-title">修改邮箱</h2>
          <div class="form-field" style="margin-bottom:10px">
            <label>新邮箱</label>
            <input class="ak-input" v-model.trim="emailForm" placeholder="owner@example.com" style="max-width:360px" />
          </div>
          <button class="ak-button ak-button--action" @click="saveEmail" :disabled="saving">保存邮箱</button>
          <div v-if="emailMsg" :class="emailErr ? 'err' : 'ok-200'" style="margin-top:8px;font-size:.8rem">{{ emailMsg }}</div>
        </section>

        <!-- 修改密码 -->
        <section class="panel">
          <h2 class="panel-title">修改密码</h2>
          <div class="form-field" style="margin-bottom:10px">
            <label>当前密码</label>
            <input class="ak-input" type="password" v-model="pwForm.old" style="max-width:360px" />
          </div>
          <div class="form-field" style="margin-bottom:10px">
            <label>新密码</label>
            <input class="ak-input" type="password" v-model="pwForm.now" style="max-width:360px" />
          </div>
          <div class="form-field" style="margin-bottom:12px">
            <label>确认新密码</label>
            <input class="ak-input" type="password" v-model="pwForm.confirm" style="max-width:360px" />
          </div>
          <button class="ak-button ak-button--action" @click="changePw" :disabled="saving">更新密码</button>
          <div v-if="pwMsg" :class="pwErr ? 'err' : 'ok-200'" style="margin-top:8px;font-size:.8rem">{{ pwMsg }}</div>
        </section>

        <!-- Webhook 通知渠道 -->
        <section class="panel">
          <h2 class="panel-title">Webhook 通知</h2>
          <p class="dim" style="margin:0 0 10px;font-size:.78rem">
            配置后，你的 SLA 任务掉线告警会同时推送到该地址（邮件/站内信照发）。
            格式按接收端选择：企业微信/钉钉群机器人选 WeCom，飞书自定义机器人选飞书，自建服务选通用。
          </p>
          <div class="form-field" style="margin-bottom:10px">
            <label>接收地址</label>
            <input class="ak-input" v-model.trim="whForm.url" placeholder="https://…（POST，JSON）" style="max-width:420px" />
          </div>
          <div class="form-field" style="margin-bottom:10px">
            <label>报文格式</label>
            <select class="ak-select" v-model="whForm.type" style="max-width:220px">
              <option value="generic">通用 JSON（自建接收端）</option>
              <option value="wecom">企业微信 / 钉钉机器人</option>
              <option value="feishu">飞书自定义机器人</option>
            </select>
          </div>
          <div style="display:flex;gap:8px;flex-wrap:wrap">
            <button class="ak-button ak-button--action" @click="saveWebhook" :disabled="saving">保存 Webhook</button>
            <button class="ak-button ak-button--outline" @click="testWebhook" :disabled="saving || !me.webhookUrl">发送测试消息</button>
            <button v-if="me.webhookUrl" class="ak-button ak-button--outline" @click="clearWebhook" :disabled="saving">清除</button>
          </div>
          <div v-if="whMsg" :class="whErr ? 'err' : 'ok-200'" style="margin-top:8px;font-size:.8rem">{{ whMsg }}</div>
        </section>

        <!-- 个人 API Token -->
        <section class="panel">
          <h2 class="panel-title">个人 API Token</h2>
          <p class="dim" style="margin:0 0 10px;font-size:.78rem">
            供脚本、自动化程序调用控制台接口与转发接口，身份与本人登录等效（权限随账号角色），
            不受 24 小时登录有效期限制。<b>明文只在生成时显示一次</b>，请立即复制保存；重新生成会使旧 Token 立即失效。
          </p>
          <template v-if="me.hasApiToken">
            <div class="acc-item" style="margin-bottom:10px">
              <div class="acc-key">当前 Token</div>
              <div class="acc-val mono">****{{ me.apiTokenHint }} <span class="dim">（{{ fmtTime(me.apiTokenCreatedAt) }} 生成）</span></div>
            </div>
            <div v-if="newToken" class="token-box">
              <span class="mono">{{ newToken }}</span>
              <button class="ak-button ak-button--outline" style="padding:2px 8px;font-size:.72rem" @click="copyToken">复制</button>
            </div>
            <div style="display:flex;gap:8px;flex-wrap:wrap;margin-top:10px">
              <button class="ak-button ak-button--action" @click="genToken" :disabled="tokenBusy">
                {{ newToken ? '重新生成（旧 Token 失效）' : '重新生成' }}
              </button>
              <button class="ak-button ak-button--outline" @click="revokeToken" :disabled="tokenBusy">吊销</button>
            </div>
          </template>
          <template v-else>
            <p class="dim" style="margin:0 0 10px;font-size:.8rem">尚未生成 Token。</p>
            <button class="ak-button ak-button--action" @click="genToken" :disabled="tokenBusy">生成 Token</button>
          </template>
          <div v-if="tokenMsg" :class="tokenErr ? 'err' : 'ok-200'" style="margin-top:8px;font-size:.8rem">{{ tokenMsg }}</div>
          <div class="form-field" style="margin-top:12px">
            <label>调用示例</label>
            <pre class="mono" style="margin:0;padding:8px 10px;background:rgba(132,131,131,.1);font-size:.72rem;overflow:auto">curl -H "Authorization: Bearer &lt;你的Token&gt;" \
  {{ apiBase }}/admin/tasks</pre>
          </div>
        </section>

        <!-- 我的用量 -->
        <section class="panel">
          <h2 class="panel-title">我的用量
            <select class="ak-select" v-model.number="usageHours" @change="loadUsage" style="width:120px;margin-left:8px;font-size:.78rem">
              <option :value="24">近 24 小时</option>
              <option :value="168">近 7 天</option>
              <option :value="720">近 30 天</option>
            </select>
          </h2>
          <p class="dim" style="margin:0 0 10px;font-size:.76rem">
            统计范围：你创建任务的定时拨测样本 + 你发起的一键拨测，不含其他用户的数据。
          </p>
          <div class="usage-kpis">
            <div class="kpi"><div class="k">{{ '样本总数' }}</div><div class="v mono">{{ usage.total }}</div></div>
            <div class="kpi ok"><div class="k">成功</div><div class="v mono">{{ usage.up }}</div></div>
            <div class="kpi bad"><div class="k">失败</div><div class="v mono">{{ usage.down }}</div></div>
            <div class="kpi"><div class="k">不明</div><div class="v mono">{{ usage.invalid ?? 0 }}</div></div>
          </div>
          <div v-if="usage.byType && usage.byType.length" class="ak-table-wrap" style="margin-top:10px">
            <table class="ak-table">
              <thead><tr><th>类型</th><th>样本</th><th>成功</th><th>失败</th><th>不明</th><th>平均耗时</th></tr></thead>
              <tbody>
                <tr v-for="r in usage.byType" :key="r.apiType">
                  <td>{{ apiLabel(r.apiType) }}</td>
                  <td class="mono">{{ r.total }}</td>
                  <td class="mono ok-200">{{ r.up }}</td>
                  <td class="mono err">{{ r.down }}</td>
                  <td class="mono dim">{{ r.invalid ?? 0 }}</td>
                  <td class="mono">{{ r.avgMs ? r.avgMs + 'ms' : '—' }}</td>
                </tr>
              </tbody>
            </table>
          </div>
          <div v-else class="dim" style="font-size:.8rem">窗口内暂无样本。</div>
        </section>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, onMounted, onBeforeUnmount } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useAuthStore } from '../stores/auth.js'
import { fetchMe, updateMeProfile, updateMeEmail, changeMyPassword, sendVerifyCode, submitVerifyCode, fetchMyUsage, testMyWebhook, createMyToken, revokeMyToken } from '../api/boce.js'
import { apiLabel } from '../utils/probeMeta.js'
import { API_BASE } from '../config.js'

const auth = useAuthStore()
const route = useRoute()
const router = useRouter()
const me = ref({})
const loading = ref(true)
const saving = ref(false)

const emailForm = ref('')
const emailMsg = ref('')
const emailErr = ref(false)
const pwForm = ref({ old: '', now: '', confirm: '' })
const pwMsg = ref('')
const pwErr = ref(false)
// 邮箱验证
const vcode = ref('')
const vsending = ref(false)
const vcd = ref(0)
let cdTimer = null
const verifyMsg = ref('')
const verifyErr = ref(false)

// Webhook 通知渠道
const whForm = ref({ url: '', type: 'generic' })
const whMsg = ref('')
const whErr = ref(false)

// 我的用量
const usageHours = ref(24)
const usage = ref({ total: 0, up: 0, down: 0, byType: [] })

// 个人 API Token
const newToken = ref('')
const tokenBusy = ref(false)
const tokenMsg = ref('')
const tokenErr = ref(false)
const apiBase = API_BASE

onMounted(async () => {
  try {
    me.value = (await fetchMe()) || {}
    emailForm.value = me.value.email || ''
    whForm.value.url = me.value.webhookUrl || ''
    whForm.value.type = me.value.webhookType || 'generic'
    // 若从横幅 / SLA 拦截跳来(verify=1)且未验证，自动发码提示
    if (route.query.verify === '1' && me.value.email && !me.value.emailVerified) {
      verifyMsg.value = '请先验证邮箱：点击「获取验证码」，填入邮件中的 6 位码后提交。'
    }
    await loadUsage()
  } catch {
    /* 静默：http 401 走登出 */
  } finally {
    loading.value = false
  }
})
onBeforeUnmount(() => cdTimer && clearInterval(cdTimer))

function fmtTime(iso) {
  if (!iso) return '—'
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString('zh-CN', { hour12: false })
}

async function saveEmail() {
  saving.value = true
  emailMsg.value = ''
  emailErr.value = false
  try {
    const r = await updateMeEmail(emailForm.value)
    me.value.email = r?.email ?? emailForm.value
    // 后端只在邮箱真的变化时才重置验证态（原值提交、清空邮箱都不重置），
    // 因此必须以响应里的 emailVerified 为准：无条件置 false 会把已验证账号误判成
    // "待验证"，进而让路由守卫在进入 SLA 页时把人拦去个人资料页。
    const verified = r?.emailVerified !== undefined ? !!r.emailVerified : me.value.emailVerified
    me.value.emailVerified = verified
    auth.refreshMeta({ email: me.value.email, emailVerified: verified })
    if (verified) {
      emailMsg.value = '邮箱已更新'
    } else {
      emailMsg.value = '邮箱已更新，请重新验证邮箱'
      verifyMsg.value = '新邮箱需重新验证：点「获取验证码」并填入收到的 6 位码。'
    }
  } catch (e) {
    emailErr.value = true
    emailMsg.value = e?.message || '保存失败'
  } finally {
    saving.value = false
  }
}

// ---- 邮箱验证 ----
function startCd() {
  vcd.value = 60
  cdTimer && clearInterval(cdTimer)
  cdTimer = setInterval(() => {
    vcd.value -= 1
    if (vcd.value <= 0) cdTimer && clearInterval(cdTimer)
  }, 1000)
}
async function sendCode() {
  if (vsending.value || vcd.value > 0) return
  verifyMsg.value = ''
  verifyErr.value = false
  vsending.value = true
  try {
    await sendVerifyCode()
    verifyMsg.value = '验证码已发送，请查收邮箱（5 分钟内有效）'
    startCd()
  } catch (e) {
    verifyErr.value = true
    verifyMsg.value = e?.message || '发送失败'
  } finally {
    vsending.value = false
  }
}
async function doVerify() {
  if (saving.value) return
  verifyMsg.value = ''
  verifyErr.value = false
  saving.value = true
  try {
    await submitVerifyCode(vcode.value)
    me.value.emailVerified = true
    auth.refreshMeta({ emailVerified: true })
    verifyMsg.value = '邮箱验证成功，SLA 监控已解锁'
    vcode.value = ''
  } catch (e) {
    verifyErr.value = true
    verifyMsg.value = e?.message || '验证失败'
  } finally {
    saving.value = false
  }
}

// ---- Webhook ----
async function saveWebhook() {
  whMsg.value = ''
  whErr.value = false
  saving.value = true
  try {
    await updateMeProfile({ webhookUrl: whForm.value.url, webhookType: whForm.value.type })
    me.value.webhookUrl = whForm.value.url
    me.value.webhookType = whForm.value.type
    whMsg.value = whForm.value.url ? 'Webhook 已保存，任务掉线告警将同步推送' : 'Webhook 已清除'
  } catch (e) {
    whErr.value = true
    whMsg.value = e?.message || '保存失败'
  } finally {
    saving.value = false
  }
}
async function testWebhook() {
  whMsg.value = ''
  whErr.value = false
  try {
    await testMyWebhook()
    whMsg.value = '测试消息已送达（HTTP 2xx）。检查一下接收端是否收到。'
  } catch (e) {
    whErr.value = true
    whMsg.value = e?.message || '测试失败'
  }
}
function clearWebhook() {
  whForm.value.url = ''
  saveWebhook()
}

// ---- 我的用量 ----
async function loadUsage() {
  try {
    usage.value = await fetchMyUsage(usageHours.value) || { total: 0, up: 0, down: 0, byType: [] }
  } catch {
    usage.value = { total: 0, up: 0, down: 0, byType: [] }
  }
}

// ---- 个人 API Token ----
async function genToken() {
  tokenBusy.value = true
  tokenMsg.value = ''
  tokenErr.value = false
  try {
    const r = await createMyToken()
    newToken.value = r.token
    me.value.hasApiToken = true
    me.value.apiTokenHint = r.hint
    me.value.apiTokenCreatedAt = r.createdAt
    tokenMsg.value = 'Token 已生成，明文仅此一次显示，请立即复制保存。'
  } catch (e) {
    tokenErr.value = true
    tokenMsg.value = e?.message || '生成失败'
  } finally {
    tokenBusy.value = false
  }
}
async function revokeToken() {
  tokenBusy.value = true
  tokenMsg.value = ''
  tokenErr.value = false
  try {
    await revokeMyToken()
    newToken.value = ''
    me.value.hasApiToken = false
    me.value.apiTokenHint = ''
    tokenMsg.value = 'Token 已吊销，使用它的脚本会立即 401。'
  } catch (e) {
    tokenErr.value = true
    tokenMsg.value = e?.message || '吊销失败'
  } finally {
    tokenBusy.value = false
  }
}
async function copyToken() {
  try {
    await navigator.clipboard.writeText(newToken.value)
    tokenMsg.value = '已复制到剪贴板'
  } catch {
    tokenErr.value = true
    tokenMsg.value = '复制失败，请手动选中复制'
  }
}

async function changePw() {
  const { old: oldPw, now: np, confirm: cf } = pwForm.value
  if (np.length < 6) { pwErr.value = true; pwMsg.value = '新密码至少 6 位'; return }
  if (np !== cf) { pwErr.value = true; pwMsg.value = '两次新密码不一致'; return }
  saving.value = true
  pwMsg.value = ''
  pwErr.value = false
  try {
    await changeMyPassword(oldPw, np)
    pwMsg.value = '密码已更新，请用新密码重新登录'
    pwForm.value = { old: '', now: '', confirm: '' }
    // 修改成功后引导重新登录，使新 token/凭证生效
    setTimeout(() => {
      auth.logout()
      router.push({ name: 'login', query: { redirect: '/profile' } })
    }, 1200)
  } catch (e) {
    pwErr.value = true
    pwMsg.value = e?.message || '更新失败'
  } finally {
    saving.value = false
  }
}
</script>

<style scoped>
.profile-wrap { max-width: 1080px; }
.acc-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(180px, 1fr)); gap: 12px; }
.acc-item.acc-wide { grid-column: 1 / -1; }
.acc-key { font-size: 0.72rem; color: var(--ak-text-secondary); margin-bottom: 4px; letter-spacing: var(--ak-type-wide); }
.acc-val { font-size: 0.9rem; }
.profile-cols { display: grid; grid-template-columns: repeat(auto-fit, minmax(360px, 1fr)); gap: 16px; }
.profile-cols .panel h2 { margin-top: 0; }
.code-row { display: flex; gap: 8px; }
.code-row .ak-input { flex: 1; min-width: 0; }
.verify-panel { grid-column: 1 / -1; }
/* 我的用量 KPI（复用 SLA 页同款配色变量） */
.usage-kpis { display: flex; gap: 10px; flex-wrap: wrap; }
.usage-kpis .kpi { border: var(--ak-line-hairline) solid rgba(255,255,255,.08); padding: 8px 14px; min-width: 96px; }
.usage-kpis .kpi .k { font-size: .68rem; color: var(--ak-text-secondary); margin-bottom: 3px; }
.usage-kpis .kpi .v { font-size: 1rem; font-weight: 600; }
.usage-kpis .kpi.ok .v { color: var(--ak-signal-success); }
.usage-kpis .kpi.bad .v { color: var(--ak-signal-danger); }
.token-box {
  display: flex; align-items: center; gap: 10px; max-width: 560px;
  padding: 8px 12px; background: rgba(74, 171, 234, .1);
  border: 1px solid rgba(74, 171, 234, .35); border-radius: 4px;
  font-size: .78rem; word-break: break-all;
}
</style>
