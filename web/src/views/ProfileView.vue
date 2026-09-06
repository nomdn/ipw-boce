<template>
  <div>
    <div v-if="loading" class="loading-center"><span class="ak-loading"></span></div>
    <div v-else class="profile-wrap">
      <!-- 账号信息卡 -->
      <section class="panel" style="max-width:640px;margin-bottom:16px">
        <h2 class="panel-title">账号信息 <span class="hl">/ me</span></h2>
        <div class="acc-grid">
          <div class="acc-item"><div class="acc-key">用户名</div><div class="acc-val mono">{{ me.username || '—' }}</div></div>
          <div class="acc-item"><div class="acc-key">角色</div><div class="acc-val"><span class="ak-tag ch">{{ me.role || '—' }}</span></div></div>
          <div class="acc-item"><div class="acc-key">账号 ID</div><div class="acc-val mono">{{ me.id || '—' }}</div></div>
          <div class="acc-item"><div class="acc-key">注册时间</div><div class="acc-val">{{ fmtTime(me.createdAt) }}</div></div>
          <div class="acc-item acc-wide"><div class="acc-key">邮箱（用于任务掉线告警收件）</div>
            <div class="acc-val mono">{{ me.email || '未设置' }}</div>
          </div>
        </div>
      </section>

      <!-- 无账号的静态 token 提示 -->
      <div v-if="me.staticToken" class="panel" style="max-width:640px;margin-bottom:16px">
        <p class="dim" style="margin:0">当前通过 admin-token（静态）登录，无可管理的账号资料。请改用账号密码登录以修改邮箱/密码。</p>
      </div>

      <div v-if="!me.staticToken" class="profile-cols">
        <!-- 邮箱验证（未验证/换新邮箱后） -->
        <section class="panel verify-panel">
          <h2 class="panel-title">邮箱验证 <span class="hl">/ verify</span></h2>
          <template v-if="me.email">
            <p class="dim" style="margin:0 0 8px">
              <span v-if="me.emailVerified" class="ok-200">✓ 邮箱已验证</span>
              <span v-else class="err">当前邮箱未验证，SLA 监控/定时拨测受限。</span>
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
          <h2 class="panel-title">修改邮箱 <span class="hl">/ email</span></h2>
          <div class="form-field" style="margin-bottom:10px">
            <label>新邮箱</label>
            <input class="ak-input" v-model.trim="emailForm" placeholder="owner@example.com" style="max-width:360px" />
          </div>
          <button class="ak-button ak-button--action" @click="saveEmail" :disabled="saving">保存邮箱</button>
          <div v-if="emailMsg" :class="emailErr ? 'err' : 'ok-200'" style="margin-top:8px;font-size:.8rem">{{ emailMsg }}</div>
        </section>

        <!-- 修改密码 -->
        <section class="panel">
          <h2 class="panel-title">修改密码 <span class="hl">/ password</span></h2>
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
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, onMounted, onBeforeUnmount } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useAuthStore } from '../stores/auth.js'
import { fetchMe, updateMeEmail, changeMyPassword, sendVerifyCode, submitVerifyCode } from '../api/boce.js'

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

onMounted(async () => {
  try {
    me.value = (await fetchMe()) || {}
    emailForm.value = me.value.email || ''
    // 若从横幅 / SLA 拦截跳来(verify=1)且未验证，自动发码提示
    if (route.query.verify === '1' && me.value.email && !me.value.emailVerified) {
      verifyMsg.value = '请先验证邮箱：点击「获取验证码」，填入邮件中的 6 位码后提交。'
    }
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
    emailMsg.value = '邮箱已更新，请重新验证邮箱'
    me.value.email = r?.email ?? emailForm.value
    // 换邮箱后需重新验证（后端已置 email_verified=false）
    me.value.emailVerified = false
    auth.refreshMeta({ email: me.value.email, emailVerified: false })
    verifyMsg.value = '新邮箱需重新验证：点「获取验证码」并填入收到的 6 位码。'
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
    const r = await submitVerifyCode(vcode.value)
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

async function changePw() {
  emailMsg.value = ''
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
</style>
