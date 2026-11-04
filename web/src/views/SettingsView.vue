<template>
  <div class="page">
    <div class="page-head">
      <div>
        <h1>系统设置</h1>
        <div class="sub">注册、人机验证与发信设置，保存后立即生效，不用重启</div>
      </div>
      <button class="btn primary" :disabled="!s || saving" @click="save"><Icon name="Check" :size="15" />保存</button>
    </div>

    <template v-if="s">
      <!-- 注册 -->
      <div class="card anim-up">
        <h3>用户注册</h3>
        <div class="set-row">
          <div><b>开放注册</b><div class="faint">关闭后登录页不显示注册入口，注册接口直接拒绝，只能由管理员在「用户管理」建号</div></div>
          <Switch v-model="s.registrationEnabled" />
        </div>
        <div class="set-row">
          <div><b>注册需邮箱验证码</b><div class="faint">开启需先配置下方 SMTP；关闭后邮箱只校验格式，账号标记为「邮箱未验证」</div></div>
          <Switch v-model="s.emailVerifyRequired" />
        </div>
      </div>

      <!-- 人机验证 -->
      <div class="card anim-up">
        <h3>人机验证</h3>
        <div class="seg" style="margin-bottom: 12px">
          <button v-for="p in PROVIDERS" :key="p.v" :class="{ on: s.captchaProvider === p.v }" @click="s.captchaProvider = p.v">{{ p.label }}</button>
        </div>
        <p class="faint" style="margin: 0 0 12px">{{ PROVIDERS.find((p) => p.v === s.captchaProvider)?.desc }}</p>

        <template v-if="s.captchaProvider === 'turnstile'">
          <div class="grid c2">
            <div class="field"><label>Site Key</label><input v-model.trim="s.turnstileSiteKey" class="inp mono" placeholder="0x4AAAAAAA…" /></div>
            <div class="field">
              <label>Secret Key {{ s.hasTurnstileSecret ? '（已设置，留空不修改）' : '' }}</label>
              <input v-model.trim="turnstileSecret" class="inp mono" type="password" autocomplete="new-password" :placeholder="s.hasTurnstileSecret ? '••••••••' : '0x4AAAAAAA…'" />
            </div>
          </div>
          <p class="faint" style="margin-top: 0">在 Cloudflare 控制台 → Turnstile 新建站点，域名填 <code>boce.1ipw.cn</code>，把两个 Key 填到这里。</p>
        </template>

        <template v-if="s.captchaProvider !== 'off'">
          <div class="set-row">
            <div><b>登录时验证</b><div class="faint">防撞库、防暴力破解密码</div></div>
            <Switch v-model="s.captchaOnLogin" />
          </div>
          <div class="set-row">
            <div><b>注册时验证</b><div class="faint">需要邮箱验证码时在「发送验证码」前验证，防止被刷邮件；否则在提交注册时验证</div></div>
            <Switch v-model="s.captchaOnRegister" />
          </div>
        </template>
      </div>

      <!-- 弹窗公告 -->
      <div class="card anim-up">
        <h3 class="row">弹窗公告 <Switch v-model="s.popup.enabled" /> <span class="faint">{{ s.popup.enabled ? '已开启' : '已关闭' }}</span></h3>
        <p class="faint" style="margin-top: 0">用户打开页面时弹出，上面图片、下面文字，带「关闭」和「我知道了」。同一份内容在 boce 和 1IPW 两边按勾选显示。</p>
        <div class="pop-grid">
          <div class="pop-form">
            <div class="row" style="gap: 18px; flex-wrap: wrap; margin-bottom: 4px">
              <label class="row chk"><Switch v-model="s.popup.showBoce" />在 boce 显示</label>
              <label class="row chk"><Switch v-model="s.popup.showIpw" />在 1IPW 显示</label>
            </div>
            <div class="field">
              <label>弹出频率</label>
              <div class="seg seg-wrap">
                <button v-for="f in FREQS" :key="f.v" :class="{ on: s.popup.frequency === f.v }" @click="s.popup.frequency = f.v">{{ f.label }}</button>
              </div>
              <div class="faint" style="margin-top: 6px">{{ FREQS.find((f) => f.v === s.popup.frequency)?.desc }}</div>
            </div>
            <div class="field"><label>标题（≤80 字）</label><input v-model="s.popup.title" class="inp" maxlength="80" placeholder="例如：欢迎使用 1IPW" /></div>
            <div class="field"><label>正文（≤2000 字，支持换行）</label><textarea v-model="s.popup.body" class="inp" rows="4" maxlength="2000" /></div>
            <div class="field">
              <label>图片（顶部，建议 16:9，png / jpg / gif / webp，≤3MB）</label>
              <div class="row pop-img-row" style="gap: 8px">
                <input v-model.trim="s.popup.imageUrl" class="inp" placeholder="粘贴图片链接，或点右侧上传" />
                <button class="btn" :disabled="uploading" @click="pickFile">{{ uploading ? '上传中…' : '上传图片' }}</button>
                <button v-if="s.popup.imageUrl" class="btn" @click="s.popup.imageUrl = ''">移除</button>
              </div>
              <input ref="fileInput" type="file" accept="image/png,image/jpeg,image/gif,image/webp" hidden @change="onFile" />
            </div>
            <div class="field"><label>点击图片跳转（选填，http(s) 链接）</label><input v-model.trim="s.popup.link" class="inp" placeholder="https://" /></div>
          </div>
          <div class="pop-prev">
            <div class="faint" style="margin-bottom: 8px">预览</div>
            <PopupAd :popup="previewPopup" preview />
            <button class="btn sm" style="margin-top: 10px" @click="resetSeen(); toastText('已清除本浏览器的已读记录，刷新页面可再次看到弹窗')">重置本浏览器已读记录</button>
          </div>
        </div>
      </div>

      <!-- SMTP -->
      <div class="card anim-up">
        <h3 class="row">邮件发送（SMTP） <span class="pill" :class="s.smtpReady ? 'ok' : ''">{{ s.smtpReady ? '已配置' : '未配置' }}</span></h3>
        <p class="faint" style="margin-top: 0">用于注册验证码、节点上下线告警、捐赠审核通知。主机留空 = 不发邮件。</p>
        <div class="grid c2">
          <div class="field"><label>SMTP 主机</label><input v-model.trim="s.smtp.host" class="inp" placeholder="smtp.qq.com" /></div>
          <div class="field"><label>端口</label><input v-model.number="s.smtp.port" class="inp" type="number" min="0" max="65535" placeholder="465" /></div>
          <div class="field"><label>用户名</label><input v-model.trim="s.smtp.user" class="inp" autocomplete="off" /></div>
          <div class="field">
            <label>密码 / 授权码 {{ s.hasSmtpPassword ? '（已设置，留空不修改）' : '' }}</label>
            <input v-model="smtpPassword" class="inp" type="password" autocomplete="new-password" :placeholder="s.hasSmtpPassword ? '••••••••' : ''" />
          </div>
          <div class="field"><label>发件地址（缺省同用户名）</label><input v-model.trim="s.smtp.from" class="inp" /></div>
          <div class="field"><label>发件人名称</label><input v-model.trim="s.smtp.fromName" class="inp" placeholder="1IPW-BOCE" /></div>
        </div>
        <div class="row" style="gap: 18px; flex-wrap: wrap">
          <label class="row chk"><Switch v-model="s.smtp.ssl" />SSL（465）</label>
          <label class="row chk"><Switch v-model="s.smtp.startTLS" />STARTTLS（587）</label>
          <label class="row chk"><Switch v-model="s.smtp.insecure" />跳过证书校验</label>
        </div>
        <div class="row" style="margin-top: 14px">
          <input v-model.trim="testTo" class="inp" style="max-width: 260px" placeholder="测试收件邮箱" />
          <button class="btn" :disabled="!testTo || testing" @click="testMail">{{ testing ? '发送中…' : '发送测试邮件' }}</button>
          <span class="faint">先保存再测试</span>
        </div>
      </div>
    </template>
  </div>
</template>

<script setup>
import { computed, onMounted, ref } from 'vue'
import Switch from '../components/Switch.vue'
import Icon from '../components/Icon.vue'
import PopupAd from '../components/PopupAd.vue'
import * as api from '../api/boce.js'
import { resetSeen } from '../utils/popup.js'
import { run, toastErr, toastText } from '../utils/toast.js'

const FREQS = [
  { v: 'once', label: '只弹一次', desc: '每个用户对同一份内容只弹一次；你改了内容后会再弹一次。' },
  { v: 'daily', label: '每天一次', desc: '同一份内容，每个用户每天最多弹一次。' },
  { v: 'session', label: '每次打开', desc: '每次新打开页面（新会话）都弹，同一会话内不重复。' },
]

const fileInput = ref(null)
const uploading = ref(false)

// 预览：相对地址的图片补上当前站点
const previewPopup = computed(() => ({
  ...s.value.popup,
  title: s.value.popup.title || '（标题）',
  body: s.value.popup.body || '（正文）',
}))

function pickFile() {
  fileInput.value?.click()
}

async function onFile(e) {
  const f = e.target.files?.[0]
  e.target.value = ''
  if (!f) return
  if (f.size > 3 * 1024 * 1024) return toastErr(new Error('图片不能超过 3MB'))
  uploading.value = true
  try {
    const d = await api.uploadPopupImage(f)
    s.value.popup.imageUrl = d.url
    toastText('图片已上传，记得点右上角「保存」')
  } catch (err) {
    toastErr(err)
  } finally {
    uploading.value = false
  }
}

const PROVIDERS = [
  { v: 'off', label: '关闭', desc: '不做人机验证，只靠单 IP 限流（每分钟 10 次）。' },
  { v: 'image', label: '图形验证码', desc: '内置字符验证码，不依赖外部服务，国内访问无障碍。' },
  { v: 'turnstile', label: 'Cloudflare Turnstile', desc: '大多数用户无感通过，防机器人效果更好；需要能访问 challenges.cloudflare.com。' },
]

const s = ref(null)
const saving = ref(false)
const turnstileSecret = ref('')
const smtpPassword = ref('')
const testTo = ref('')
const testing = ref(false)

async function load() {
  s.value = normalize(await api.fetchSiteSettings())
  turnstileSecret.value = ''
  smtpPassword.value = ''
}

// 端口 0 = 未填，输入框显示占位符
function normalize(v) {
  if (v?.smtp && !v.smtp.port) v.smtp.port = ''
  return v
}

async function save() {
  saving.value = true
  try {
    const v = s.value
    s.value = normalize(
      await run(
      () =>
        api.saveSiteSettings({
          registrationEnabled: v.registrationEnabled,
          emailVerifyRequired: v.emailVerifyRequired,
          captchaProvider: v.captchaProvider,
          captchaOnLogin: v.captchaOnLogin,
          captchaOnRegister: v.captchaOnRegister,
          turnstileSiteKey: v.turnstileSiteKey,
          turnstileSecret: turnstileSecret.value,
          popup: { ...v.popup },
          smtp: { ...v.smtp, port: Number(v.smtp.port) || 0, password: smtpPassword.value },
        }),
      '已保存',
      ),
    )
    turnstileSecret.value = ''
    smtpPassword.value = ''
  } finally {
    saving.value = false
  }
}

async function testMail() {
  testing.value = true
  try {
    await run(() => api.sendTestMail(testTo.value), '测试邮件已发出，请查收')
  } finally {
    testing.value = false
  }
}

onMounted(load)
</script>

<style scoped>
.card { margin-bottom: 14px; }
.card h3 { margin-top: 0; }
.set-row { display: flex; align-items: center; justify-content: space-between; gap: 16px; padding: 12px 0; border-top: 1px solid var(--border); }
.set-row .faint { font-size: 13px; margin-top: 2px; }
.chk { gap: 8px; font-size: 14px; cursor: pointer; }
/* 内容区只有 768px 宽，表单与预览上下排，避免左右挤出 */
.pop-grid { display: flex; flex-direction: column; gap: 8px; min-width: 0; }
.pop-form, .pop-prev { min-width: 0; max-width: 100%; }
.pop-prev { max-width: 460px; }
.pop-img-row { flex-wrap: nowrap; }
.pop-img-row .inp { flex: 1; min-width: 0; width: auto; }
.pop-img-row .btn { flex-shrink: 0; white-space: nowrap; }
.seg-wrap { max-width: 100%; flex-wrap: wrap; border-radius: 18px; }
textarea.inp { resize: vertical; min-height: 90px; font: inherit; line-height: 1.5; }
@media (max-width: 520px) { .pop-img-row { flex-wrap: wrap; } .pop-img-row .inp { flex-basis: 100%; } }
</style>
