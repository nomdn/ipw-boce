<template>
  <div class="login-page">
    <button class="btn icon theme-btn" :aria-label="theme === 'dark' ? '切换浅色' : '切换深色'" @click="toggle">
      <Icon :name="theme === 'dark' ? 'Sun' : 'Moon'" :size="18" />
    </button>
    <div class="login-box anim-up">
      <div class="logo"><Icon name="Activity" :size="26" :stroke="2" /></div>
      <Transition name="swap-title" mode="out-in">
        <h1 :key="mode">{{ mode === 'login' ? '欢迎回来' : '创建账号' }}</h1>
      </Transition>
      <p class="muted">1IPW-BOCE 拨测中心</p>

      <Transition name="swap-form" mode="out-in">
        <form v-if="mode === 'login'" key="login" @submit.prevent="doLogin">
          <input v-model.trim="form.username" class="inp big" placeholder="用户名" autocomplete="username" @keydown.enter.prevent="doLogin" />
          <input v-model="form.password" class="inp big" type="password" placeholder="密码" autocomplete="current-password" @keydown.enter.prevent="doLogin" />
          <Captcha v-if="capLoginMode !== 'off'" ref="capLogin" :mode="capLoginMode" :site-key="site.turnstileSiteKey" :theme="theme" @enter="doLogin" />
          <!-- 用原生 button：NutUI 按钮在 H5 是 view，不触发表单提交 -->
          <button type="submit" class="cta" :disabled="busy">
            <Icon v-if="busy" name="RefreshCw" :size="16" class="spin" />{{ busy ? '登录中' : '继续' }}
          </button>
        </form>

        <form v-else key="reg" @submit.prevent="doRegister">
          <input v-model.trim="reg.username" class="inp big" placeholder="用户名" autocomplete="username" />
          <input v-model.trim="reg.email" class="inp big" type="email" placeholder="邮箱" autocomplete="email" />
          <Captcha v-if="capRegMode !== 'off'" ref="capReg" :mode="capRegMode" :site-key="site.turnstileSiteKey" :theme="theme" />
          <div v-if="site.emailVerifyRequired" class="code-row">
            <input v-model.trim="reg.code" class="inp big" placeholder="邮箱验证码" inputmode="numeric" />
            <button type="button" class="btn code-btn" :disabled="cooldown > 0 || !reg.email" @click="sendCode">
              {{ cooldown > 0 ? `${cooldown}s` : '发送验证码' }}
            </button>
          </div>
          <input v-model="reg.password" class="inp big" type="password" placeholder="密码（至少 6 位）" autocomplete="new-password" />
          <button type="submit" class="cta" :disabled="busy">
            <Icon v-if="busy" name="RefreshCw" :size="16" class="spin" />{{ busy ? '提交中' : '注册' }}
          </button>
        </form>
      </Transition>

      <Transition name="fade"><p v-if="err" class="err">{{ err }}</p></Transition>
      <p class="switch">
        <template v-if="mode === 'login'">
          <template v-if="site.registrationEnabled">还没有账号？<a href="#" @click.prevent="switchMode('register')">注册</a></template>
          <span v-else-if="siteLoaded" class="faint">暂未开放注册，请联系管理员创建账号</span>
        </template>
        <template v-else>已有账号？<a href="#" @click.prevent="switchMode('login')">登录</a></template>
      </p>
    </div>
  </div>
</template>

<script setup>
import { computed, onMounted, reactive, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useAuthStore } from '../stores/auth.js'
import { useTheme } from '../composables/useTheme.js'
import * as api from '../api/boce.js'
import { toastOk } from '../utils/toast.js'
import Icon from '../components/Icon.vue'
import Captcha from '../components/Captcha.vue'

const auth = useAuthStore()
const router = useRouter()
const route = useRoute()
const { theme, toggle } = useTheme()
const mode = ref('login')
const busy = ref(false)
const err = ref('')
const cooldown = ref(0)
const form = reactive({ username: '', password: '' })
const reg = reactive({ username: '', email: '', code: '', password: '' })
// 站点开关（后台「系统设置」）；拉取失败时按最保守的方式显示：可登录、不显示注册入口
const site = reactive({ registrationEnabled: false, emailVerifyRequired: true, captcha: { login: 'off', register: 'off' }, turnstileSiteKey: '' })
const siteLoaded = ref(false)
const capLogin = ref(null)
const capReg = ref(null)
const capLoginMode = computed(() => site.captcha?.login || 'off')
const capRegMode = computed(() => site.captcha?.register || 'off')

onMounted(async () => {
  try {
    Object.assign(site, await api.fetchPublicSite())
  } catch {
    /* 保持默认 */
  }
  siteLoaded.value = true
})

// 取人机验证字段；未完成时写错误并返回 null
function capFields(ref_, m) {
  if (m === 'off' || !ref_.value) return {}
  const p = ref_.value.payload()
  if (p.error) {
    err.value = p.error
    return null
  }
  return p
}

function switchMode(m) {
  err.value = ''
  mode.value = m
}

async function doLogin() {
  if (busy.value) return
  err.value = ''
  if (!form.username || !form.password) return (err.value = '请输入用户名和密码')
  const extra = capFields(capLogin, capLoginMode.value)
  if (!extra) return
  busy.value = true
  try {
    await auth.login(form.username, form.password, extra)
    router.replace(route.query.redirect || '/')
  } catch (e) {
    err.value = e.message
    capLogin.value?.reset()
  } finally {
    busy.value = false
  }
}

async function sendCode() {
  err.value = ''
  const extra = capFields(capReg, capRegMode.value)
  if (!extra) return
  try {
    await api.sendRegisterCode(reg.email, reg.username, extra)
    toastOk('验证码已发送')
    cooldown.value = 60
    const t = setInterval(() => --cooldown.value <= 0 && clearInterval(t), 1000)
  } catch (e) {
    err.value = e.message
  } finally {
    capReg.value?.reset()
  }
}

async function doRegister() {
  if (busy.value) return
  err.value = ''
  // 需要邮箱验证码时，人机验证在发验证码那一步做过了
  let extra = {}
  if (!site.emailVerifyRequired) {
    extra = capFields(capReg, capRegMode.value)
    if (!extra) return
  }
  busy.value = true
  try {
    await api.register({ ...reg, ...extra })
    toastOk('注册成功，请登录')
    form.username = reg.username
    switchMode('login')
  } catch (e) {
    err.value = e.message
    if (!site.emailVerifyRequired) capReg.value?.reset()
  } finally {
    busy.value = false
  }
}
</script>

<style scoped>
.login-page { min-height: 100%; display: flex; align-items: center; justify-content: center; padding: 24px; background: var(--bg); position: relative; }
.theme-btn { position: absolute; top: 14px; right: 14px; }
.login-box { width: 100%; max-width: 340px; text-align: center; }
.logo { width: 48px; height: 48px; margin: 0 auto; border-radius: 14px; background: var(--primary); color: var(--primary-fg); display: flex; align-items: center; justify-content: center; }
h1 { font-size: 30px; font-weight: 600; margin: 22px 0 4px; letter-spacing: -0.02em; }
form { display: flex; flex-direction: column; gap: 12px; margin-top: 28px; }
.inp.big { padding: 13px 16px; border-radius: 12px; font-size: 15px; }
.code-row { display: flex; gap: 8px; }
.code-btn { flex-shrink: 0; border-radius: 12px; }
.cta {
  display: flex; align-items: center; justify-content: center; gap: 8px; height: 50px; margin-top: 4px;
  border: none; border-radius: 12px; background: var(--primary); color: var(--primary-fg);
  font: inherit; font-size: 15px; font-weight: 500; cursor: pointer; transition: opacity 0.15s, transform 0.1s var(--ease);
}
.cta:hover { opacity: 0.88; }
.cta:active { transform: scale(0.98); }
.cta:disabled { opacity: 0.6; cursor: default; }
.err { color: var(--danger); font-size: 13px; margin: 14px 0 0; }
.switch { margin-top: 22px; font-size: 14px; color: var(--text-2); }

.swap-title-enter-active, .swap-title-leave-active, .swap-form-enter-active, .swap-form-leave-active { transition: opacity 0.18s var(--ease), transform 0.18s var(--ease); }
.swap-title-enter-from, .swap-form-enter-from { opacity: 0; transform: translateY(6px); }
.swap-title-leave-to, .swap-form-leave-to { opacity: 0; transform: translateY(-4px); }
.fade-enter-active, .fade-leave-active { transition: opacity 0.2s; }
.fade-enter-from, .fade-leave-to { opacity: 0; }
</style>
