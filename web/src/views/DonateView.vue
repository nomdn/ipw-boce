<template>
  <div class="page">
    <div class="page-head">
      <div>
        <h1>捐赠节点</h1>
        <div class="sub">把你的服务器或家里的设备接入拨测网络，审核通过后获得{{ meta?.currencyName || '积分' }}，之后每天在线还有奖励</div>
      </div>
      <button v-if="meta?.enabled" class="btn primary" @click="openForm()"><Icon name="Plus" :size="15" />捐赠节点</button>
    </div>

    <div v-if="meta && !meta.enabled" class="card soft">节点捐赠暂未开放</div>

    <div v-if="meta?.enabled" class="grid c2 anim-up" style="margin-bottom: 14px">
      <div v-for="r in RATES" :key="r.k" class="card soft rate">
        <div class="row between">
          <div class="row" style="gap: 10px"><span class="rate-ico"><Icon :name="r.icon" :size="18" /></span><b>{{ r.title }}</b></div>
          <span class="pill">{{ r.tag }}</span>
        </div>
        <div class="big">+{{ meta.bonus[r.k] }}</div>
        <div class="muted">审核通过一次性奖励</div>
        <div class="daily">每天在线满 {{ meta.minOnlineHours }} 小时 <b>+{{ meta.daily[r.k] }}</b></div>
      </div>
    </div>

    <div v-if="!list.length && meta?.enabled" class="empty"><Icon name="Server" :size="28" />你还没有捐赠节点</div>

    <div v-for="d in list" :key="d.id" class="card hover anim-up">
      <div class="row between">
        <div class="row">
          <StatusDot :on="d.online" />
          <b>{{ d.label }}</b>
          <span class="pill">{{ NET_TYPE[d.netType] }}</span>
          <span class="pill" :class="DONATE_STATUS[d.status]?.[1]">{{ DONATE_STATUS[d.status]?.[0] }}</span>
        </div>
        <div class="row">
          <button v-if="d.status === 'pending' || d.status === 'rejected'" class="btn sm" @click="openForm(d)">修改</button>
          <button v-if="d.status !== 'withdrawn'" class="btn sm danger" @click="withdraw(d)">撤回</button>
        </div>
      </div>
      <div class="faint" style="margin-top: 4px">
        {{ d.region }} · {{ d.isp }} · {{ d.stack }} · {{ d.channel.toUpperCase() }} 接入<template v-if="d.version"> · v{{ d.version }}</template>
        · 最后在线 {{ fmtTime(d.lastSeenAt) }}
      </div>
      <div v-if="d.reviewNote && d.status !== 'approved'" class="bad" style="font-size: 13px; margin-top: 6px">审核意见：{{ d.reviewNote }}</div>
      <div class="grid c3" style="margin-top: 10px" v-if="d.status === 'approved'">
        <div class="stat"><div class="k">捐赠奖励</div><div class="v">{{ d.bonusPaid }}</div></div>
        <div class="stat"><div class="k">在线奖励累计</div><div class="v">{{ d.dailyEarned }}</div></div>
        <div class="stat"><div class="k">达标天数</div><div class="v">{{ d.rewardDays }}</div></div>
      </div>

      <div v-if="d.status === 'approved' && d.channel === 'ws'" class="install anim-up">
        <div class="row between">
          <b class="row" style="gap: 6px"><Icon name="Terminal" :size="15" />一键安装</b>
          <button class="btn sm" @click="resetToken">{{ d.installCmd ? '重置节点 Token' : '生成安装命令' }}</button>
        </div>
        <template v-if="d.installCmd">
          <p class="muted">在节点机器上用 root 执行（Linux + systemd，x86_64 / arm64 / armv7）。命令里含你的节点 Token，不要发给别人。</p>
          <pre class="json">{{ d.installCmd }}</pre>
          <button class="btn sm" @click="copy(d.installCmd)">复制命令</button>
        </template>
        <p v-else class="muted">还没有节点 Token，点右上角生成后即可得到安装命令。</p>
      </div>
      <p v-else-if="d.status === 'pending' && d.channel === 'ws'" class="faint" style="margin-top: 10px">审核通过后这里会给出一键安装命令。</p>

      <details v-if="d.status !== 'withdrawn'" class="access">
        <summary>手动接入信息</summary>
        <template v-if="d.channel === 'ws'">
          <p class="muted">在节点程序的配置里填写下面三项（WS 接入不需要公网端口，家宽也能用）：</p>
          <pre class="json">{{ wsSnippet(d) }}</pre>
        </template>
        <template v-else>
          <p class="muted">中心会访问 <code>{{ d.url }}</code> 的 <code>/v1/*</code> 与 <code>/info</code>，并带上你填写的 access-token。</p>
        </template>
        <div class="row" style="margin-top: 8px">
          <button class="btn sm" @click="copy(wsSnippet(d))">复制配置</button>
          <button class="btn sm" @click="rotate(d)">重新生成 Key</button>
        </div>
      </details>
    </div>

    <Modal v-model="formOpen" width="min(560px, 94vw)" :title="form?.id ? '修改捐赠信息' : '捐赠节点'">
      <div v-if="form">
        <div class="field">
          <label>网络类型</label>
          <div class="seg">
            <button :class="{ on: form.netType === 'idc' }" @click="form.netType = 'idc'">机房</button>
            <button :class="{ on: form.netType === 'home' }" @click="form.netType = 'home'">家庭宽带</button>
          </div>
        </div>
        <div class="grid c2">
          <div class="field"><label>地区</label><input v-model.trim="form.region" class="inp" placeholder="中国 江苏 南京" /></div>
          <div class="field"><label>运营商</label><input v-model.trim="form.isp" class="inp" placeholder="电信 / 联通 / 移动 / 阿里云…" /></div>
          <div class="field"><label>显示名（选填）</label><input v-model.trim="form.label" class="inp" placeholder="缺省为 地区 + 运营商" /></div>
          <div class="field"><label>节点 ID（选填，创建后不可改）</label><input v-model.trim="form.nodeId" class="inp" :disabled="!!form.id" placeholder="自动生成" /></div>
          <div class="field">
            <label>IP 栈</label>
            <select v-model="form.stack" class="inp"><option value="DualStack">双栈</option><option value="IPv4">仅 IPv4</option><option value="IPv6">仅 IPv6</option></select>
          </div>
          <div class="field">
            <label>接入方式</label>
            <select v-model="form.channel" class="inp"><option value="ws">WS（推荐，无需公网端口）</option><option value="http">HTTP（需公网可访问）</option></select>
          </div>
        </div>
        <template v-if="form.channel === 'http'">
          <div class="field"><label>节点地址</label><input v-model.trim="form.url" class="inp" placeholder="https://node.example.com/" /></div>
          <div class="field"><label>access-token（中心访问节点用）</label><input v-model="form.apiKey" class="inp" type="password" /></div>
        </template>
        <div class="grid c2">
          <div class="field"><label>带宽（选填）</label><input v-model.trim="form.bandwidth" class="inp" placeholder="100M / 1G" /></div>
          <div class="field"><label>联系方式（选填）</label><input v-model.trim="form.contact" class="inp" placeholder="QQ / 邮箱 / Telegram" /></div>
        </div>
        <div class="field"><label>备注（选填）</label><input v-model="form.remark" class="inp" /></div>
        <div class="row" style="justify-content: flex-end">
          <button class="btn" @click="formOpen = false">取消</button>
          <button class="btn primary" @click="submit">{{ form.id ? '保存并重新提交' : '提交审核' }}</button>
        </div>
      </div>
    </Modal>
  </div>
</template>

<script setup>
import Modal from '../components/Modal.vue'
import StatusDot from '../components/StatusDot.vue'
import Icon from '../components/Icon.vue'
import { onMounted, ref } from 'vue'
import * as api from '../api/boce.js'
import { copyText, DONATE_STATUS, fmtTime, NET_TYPE } from '../utils/format.js'
import { confirm, run, toastOk } from '../utils/toast.js'

const RATES = [
  { k: 'idc', title: '机房节点', tag: 'IDC / 云服务器', icon: 'Building2' },
  { k: 'home', title: '家庭宽带节点', tag: '家宽 / 软路由 / NAS', icon: 'House' },
]

const meta = ref(null)
const list = ref([])
const formOpen = ref(false)
const form = ref(null)

// 与 ipw-cn 节点程序 setting.json 键名一致（环境变量为 WS_URL / NODE_ID / NODE_KEY）
const wsSnippet = (d) => `"ws-url": "${meta.value?.wsUrl}",
"node-id": "${d.nodeId}",
"node-key": "${d.wsKey}"`

async function load() {
  ;[meta.value, list.value] = await Promise.all([api.fetchDonateMeta(), api.fetchMyDonations()])
}
function openForm(d) {
  form.value = d
    ? { ...d, apiKey: '' }
    : { netType: 'idc', region: '', isp: '', label: '', nodeId: '', stack: 'DualStack', channel: 'ws', url: '', apiKey: '', bandwidth: '', contact: '', remark: '' }
  formOpen.value = true
}
async function submit() {
  const f = form.value
  if (f.id) await run(() => api.updateDonation(f.id, f), '已重新提交')
  else await run(() => api.createDonation(f), '已提交，请按接入信息配置节点')
  formOpen.value = false
  load()
}
async function withdraw(d) {
  if (!(await confirm(`撤回「${d.label}」？节点将从节点池移除，不再获得在线奖励。`))) return
  await run(() => api.withdrawDonation(d.id), '已撤回')
  load()
}
async function rotate(d) {
  if (!(await confirm('重新生成后旧 Key 立即失效，节点需要更新配置才能重新接入。'))) return
  await run(() => api.rotateDonationKey(d.id), '已重新生成')
  load()
}
async function resetToken() {
  const has = list.value.some((d) => d.installCmd)
  if (has && !(await confirm('重置后旧 Token 立即失效，已装好的节点需要用新命令重新安装。'))) return
  await run(() => api.resetAgentToken(), has ? '已重置' : '已生成')
  load()
}
async function copy(t) {
  if (await copyText(t)) toastOk('已复制')
}
onMounted(load)
</script>

<style scoped>
.rate { margin: 0; }
.rate .big { font-size: 32px; font-weight: 600; margin-top: 10px; letter-spacing: -0.02em; }
.rate-ico { width: 34px; height: 34px; border-radius: 10px; background: var(--bg); border: 1px solid var(--border); display: flex; align-items: center; justify-content: center; }
.rate .daily { margin-top: 8px; font-size: 14px; }
.access { margin-top: 12px; }
.install { margin-top: 12px; padding: 12px; border: 1px solid var(--border); border-radius: 12px; background: var(--bg); }
.install pre { white-space: pre-wrap; word-break: break-all; }
.access summary { cursor: pointer; color: var(--text-2); font-size: 13px; }
</style>
