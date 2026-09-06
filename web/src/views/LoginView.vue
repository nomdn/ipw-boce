<template>
  <div class="login-scene">
    <div class="login-grid"></div>

    <form class="login-card" novalidate @submit.prevent="onSubmit">
      <div class="login-badge">LEMON / IPW</div>
      <h1 class="lc-command">IPW DASHBOARD</h1>
      <p class="lc-sub">{{ mode === 'login' ? '' : '创建账号需先验证邮箱（6 位验证码）' }}</p>

      <div class="ak-form-stack" style="margin-top: 22px">
        <!-- 登录态 -->
        <template v-if="mode === 'login'">
          <label class="ak-field">
            <span class="ak-label">用户名</span>
            <input class="ak-input" v-model.trim="username" name="username" autocomplete="username"
              placeholder="admin" autofocus />
          </label>
          <label class="ak-field">
            <span class="ak-label">口令</span>
            <input class="ak-input" type="password" v-model="password" name="password"
              autocomplete="current-password" placeholder="••••••••" />
          </label>
        </template>

        <!-- 注册态 -->
        <template v-else>
          <label class="ak-field">
            <span class="ak-label">用户名</span>
            <input class="ak-input" v-model.trim="reg.username" name="username" autocomplete="username"
              placeholder="登录用户名" autofocus />
          </label>
          <label class="ak-field">
            <span class="ak-label">口令</span>
            <input class="ak-input" type="password" v-model="reg.password" name="new-password"
              autocomplete="new-password" placeholder="至少 6 位" />
          </label>
          <label class="ak-field">
            <span class="ak-label">邮箱</span>
            <input class="ak-input" v-model.trim="reg.email" type="email" name="email" autocomplete="email"
              placeholder="you@example.com" />
          </label>
          <label class="ak-field">
            <span class="ak-label">邮箱验证码</span>
            <div class="code-row">
              <input class="ak-input" v-model.trim="reg.code" name="code" inputmode="numeric"
                maxlength="6" placeholder="6 位验证码" autocomplete="one-time-code" />
              <button type="button" class="ak-button ak-button--outline sm code-btn" :disabled="codeBusy || cd > 0"
                @click="sendCode">
                {{ codeBusy ? '发送中…' : cd > 0 ? `重新获取 ${cd}s` : '获取验证码' }}
              </button>
            </div>
          </label>
        </template>
      </div>

      <div class="login-err" v-if="error">{{ error }}</div>
      <div class="login-ok" v-if="okMsg">{{ okMsg }}</div>

      <button class="ak-button ak-button--block ak-button--action" type="submit" :disabled="busy" style="margin-top: 18px">
        <template v-if="busy">{{ mode === 'login' ? '登录中…' : '注册中…' }}</template>
        <template v-else>{{ mode === 'login' ? '进入控制台' : '注册并登录' }}</template>
      </button>

      <div class="login-switch">
        <button type="button" class="link" @click="switchMode">
          {{ mode === 'login' ? '没有账号？注册一个' : '已有账号？去登录' }}
        </button>
      </div>
    </form>
  </div>
</template>

<script setup>
import { ref, onBeforeUnmount } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useAuthStore } from '../stores/auth.js'
import { sendRegisterCode, register } from '../api/boce.js'

const mode = ref('login')
const username = ref('')
const password = ref('')
const error = ref('')
const okMsg = ref('')
const busy = ref(false)
const route = useRoute()
const router = useRouter()
const auth = useAuthStore()

const reg = ref({ username: '', password: '', email: '', code: '' })
const codeBusy = ref(false)
const cd = ref(0)
let cdTimer = null

function switchMode() {
  mode.value = mode.value === 'login' ? 'register' : 'login'
  error.value = ''
  okMsg.value = ''
}

function startCd() {
  cd.value = 60
  cdTimer && clearInterval(cdTimer)
  cdTimer = setInterval(() => {
    cd.value -= 1
    if (cd.value <= 0) cdTimer && clearInterval(cdTimer)
  }, 1000)
}

async function sendCode() {
  if (codeBusy.value || cd.value > 0) return
  error.value = ''
  okMsg.value = ''
  if (!/^\S+@\S+\.\S+$/.test(reg.value.email)) {
    error.value = '请先填写有效邮箱'
    return
  }
  codeBusy.value = true
  try {
    await sendRegisterCode(reg.value.email, reg.value.username)
    okMsg.value = '验证码已发送，请查收邮箱（5 分钟内有效）'
    startCd()
  } catch (e) {
    error.value = e?.message || '发送失败'
  } finally {
    codeBusy.value = false
  }
}

async function onSubmit() {
  if (busy.value) return
  error.value = ''
  okMsg.value = ''
  if (mode.value === 'login') {
    if (!username.value || !password.value) {
      error.value = '请输入用户名与口令'
      return
    }
    busy.value = true
    try {
      await auth.login(username.value, password.value)
      router.push(route.query.redirect || '/')
    } catch (e) {
      error.value = e?.message || '登录失败'
    } finally {
      busy.value = false
    }
    return
  }
  // register
  const { username: un, password: pw, email, code } = reg.value
  if (!un || !pw || !email || !code) {
    error.value = '请完整填写用户名、口令、邮箱与验证码'
    return
  }
  if (pw.length < 6) {
    error.value = '口令至少 6 位'
    return
  }
  busy.value = true
  try {
    await register({ username: un, password: pw, email, code })
    okMsg.value = '注册成功，正在登录…'
    mode.value = 'login'
    username.value = un
    password.value = pw
    // 注册即已验邮箱，直接登录进控制台
    await auth.login(un, pw)
    router.push(route.query.redirect || '/')
  } catch (e) {
    error.value = e?.message || '注册失败'
  } finally {
    busy.value = false
  }
}

onBeforeUnmount(() => cdTimer && clearInterval(cdTimer))
</script>

<style scoped>
.login-scene {
  position: relative;
  min-height: 100vh;
  display: flex;
  align-items: center;
  justify-content: center;
  background: var(--ak-surface-canvas);
  overflow: hidden;
}
/* 战术网格底纹（纯几何，非素材） */
.login-grid {
  position: absolute;
  inset: 0;
  background-image:
    linear-gradient(rgba(255, 255, 255, 0.035) 1px, transparent 1px),
    linear-gradient(90deg, rgba(255, 255, 255, 0.035) 1px, transparent 1px);
  background-size: 44px 44px;
  mask-image: radial-gradient(circle at 50% 45%, #000 0%, transparent 72%);
}
.login-card {
  position: relative;
  width: min(400px, calc(100vw - 40px));
  background: var(--ak-surface-inverse);
  border: var(--ak-line-hairline) solid rgba(255, 255, 255, 0.12);
  border-top: 4px solid var(--ak-signal-info);
  padding: 34px 32px 30px;
  clip-path: polygon(0 0, calc(100% - 16px) 0, 100% 16px, 100% 100%, 0 100%);
  box-shadow: 0 24px 60px rgba(0, 0, 0, 0.5);
}
.login-badge {
  font-family: var(--ak-font-mono);
  font-size: 0.7rem;
  letter-spacing: 0.3em;
  color: var(--ak-signal-info);
  margin-bottom: 14px;
}
.lc-command {
  font-family: var(--ak-font-command);
  font-size: 1.9rem;
  font-weight: 700;
  margin: 0;
  letter-spacing: 0.02em;
}
.lc-sub {
  margin: 4px 0 0;
  color: var(--ak-text-secondary);
  font-size: 0.82rem;
}
.login-err {
  margin-top: 12px;
  color: var(--ak-signal-danger);
  font-size: 0.82rem;
}
.login-ok {
  margin-top: 12px;
  color: var(--ak-signal-success, #46c47c);
  font-size: 0.82rem;
}
.code-row {
  display: flex;
  gap: 8px;
}
.code-row .ak-input {
  flex: 1;
}
.code-btn {
  flex: none;
  white-space: nowrap;
}
.login-switch {
  margin-top: 14px;
  text-align: center;
}
.link {
  background: none;
  border: none;
  color: var(--ak-signal-info);
  cursor: pointer;
  font-size: 0.82rem;
  padding: 0;
}
.link:hover {
  text-decoration: underline;
}
</style>
