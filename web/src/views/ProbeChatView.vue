<template>
  <div class="chat" :class="{ empty: !messages.length }">
    <div v-if="messages.length" ref="threadEl" class="thread">
      <TransitionGroup name="msg">
        <div v-for="m in messages" :key="m.id" class="msg" :class="m.role">
          <!-- 用户消息：右侧灰色气泡 -->
          <div v-if="m.role === 'user'" class="bubble">
            <span class="chip mono">{{ TYPE_LABEL[m.apiType] || m.apiType }}</span>{{ m.raw }}
            <span v-if="m.query" class="faint"> ?{{ m.query }}</span>
          </div>

          <!-- 回复：无气泡，直接正文 -->
          <div v-else class="answer">
            <div v-if="m.loading" class="thinking">
              <span class="shimmer-text">正在向 {{ m.nodeHint }} 下发拨测…</span>
            </div>
            <div v-else-if="m.error" class="err-line"><Icon name="CircleAlert" :size="16" /> {{ m.error }}</div>
            <template v-else>
              <p class="summary">
                共 <b>{{ m.data.targeted }}</b> 个节点，<b>{{ m.data.ok }}</b> 个成功<template v-if="m.data.failed">，<b class="bad">{{ m.data.failed }}</b> 个失败</template>
                <template v-if="m.data.skipped?.length">，跳过离线节点 {{ m.data.skipped.length }} 个</template>。
              </p>
              <div class="results">
                <div
                  v-for="(r, i) in m.data.results"
                  :key="r.nodeId"
                  class="res"
                  :class="{ fail: !isOk(r), open: r._open }"
                  :style="{ animationDelay: i * 40 + 'ms' }"
                >
                  <button class="res-head" @click="r._open = !r._open">
                    <StatusDot :on="isOk(r)" :err="!isOk(r)" />
                    <span class="node ellipsis">{{ r.label || r.nodeId }}</span>
                    <span class="brief ellipsis">{{ brief(m.apiType, r) }}</span>
                    <span class="meta mono">{{ r.status || '—' }} · {{ r.latencyMs }}ms</span>
                    <Icon name="ChevronRight" :size="16" class="chev" />
                  </button>
                  <div class="res-body">
                    <div class="res-body-inner"><pre class="json">{{ r.error || prettyJSON(r.body) }}</pre></div>
                  </div>
                </div>
              </div>
              <div class="actions">
                <button class="btn icon" title="复制 JSON" @click="copy(m)"><Icon :name="m._copied ? 'Check' : 'Copy'" :size="16" /></button>
                <button class="btn icon" title="重新拨测" @click="resend(m)"><Icon name="RotateCcw" :size="16" /></button>
              </div>
            </template>
          </div>
        </div>
      </TransitionGroup>
    </div>

    <div class="composer-wrap" :class="{ centered: !messages.length }">
      <Transition name="hero">
        <h1 v-if="!messages.length" class="hero">今天想拨测什么？</h1>
      </Transition>

      <div class="composer" :class="{ focus: focused }">
        <textarea
          ref="inputEl"
          v-model="text"
          rows="1"
          :placeholder="placeholder"
          @focus="focused = true"
          @blur="focused = false"
          @keydown.enter.exact.prevent="send"
          @input="autoGrow"
        />
        <div class="bar">
          <!-- 拨测类型：工具弹出菜单 -->
          <div ref="typeRoot" class="tool">
            <button class="tool-btn" :class="{ on: typeMenu }" @click="typeMenu = !typeMenu">
              <Icon :name="TYPES.find((t) => t.v === apiType).icon" :size="16" />
              {{ TYPES.find((t) => t.v === apiType).label }}
              <Icon name="ChevronDown" :size="14" class="caret" :class="{ up: typeMenu }" />
            </button>
            <Transition name="pop">
              <div v-if="typeMenu" class="tool-menu">
                <button v-for="t in TYPES" :key="t.v" class="tm-item" :class="{ on: apiType === t.v }" @click="pickType(t.v)">
                  <Icon :name="t.icon" :size="16" />
                  <div><div>{{ t.label }}</div><div class="faint">{{ t.tip }}</div></div>
                  <Icon v-if="apiType === t.v" name="Check" :size="16" class="tm-check" />
                </button>
              </div>
            </Transition>
          </div>

          <Transition name="fade">
            <div v-if="apiType === 'dns'" class="opt">
              <select v-model="dnsType" class="mini"><option v-for="r in DNS_TYPES" :key="r" :value="r">{{ r.toUpperCase() }}</option></select>
            </div>
            <div v-else-if="apiType === 'tcping'" class="opt">
              <input v-model="port" class="mini" placeholder="端口" style="width: 64px" />
              <input v-model="count" class="mini" placeholder="次数" style="width: 56px" />
            </div>
            <div v-else-if="apiType === 'speed'" class="opt">
              <select v-model="speedStack" class="mini"><option value="v4">IPv4</option><option value="v6">IPv6</option></select>
            </div>
          </Transition>

          <select v-model="nodeSel" class="mini node-sel" title="拨测节点">
            <option value="">全部在线节点</option>
            <option v-for="n in poolNodes" :key="n.nodeId" :value="n.nodeId">{{ n.label || n.nodeId }}{{ n.online ? '' : '（离线）' }}</option>
          </select>

          <span class="spacer" />
          <button class="send" :class="{ busy: sending }" :disabled="!text.trim() && !sending" :aria-label="sending ? '拨测中' : '发送'" @click="send">
            <Transition name="swap" mode="out-in">
              <Icon v-if="sending" key="s" name="Square" :size="14" :stroke="2.5" />
              <Icon v-else key="a" name="ArrowUp" :size="18" :stroke="2.25" />
            </Transition>
          </button>
        </div>
      </div>

      <div class="hint faint">
        <template v-if="probeCost > 0 && !auth.isAdmin">每次拨测消耗 {{ probeCost }} {{ auth.currencyName }} · </template>
        结果会写入拨测明细
      </div>

      <Transition name="hero">
        <div v-if="!messages.length" class="suggest">
          <button v-for="(s, i) in SUGGEST" :key="s.raw" class="sg" :style="{ animationDelay: 120 + i * 50 + 'ms' }" @click="useSuggest(s)">
            <Icon :name="TYPES.find((t) => t.v === s.apiType).icon" :size="16" />
            <span>{{ s.text }}</span>
          </button>
        </div>
      </Transition>
    </div>
  </div>
</template>

<script setup>
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import * as api from '../api/boce.js'
import { useAuthStore } from '../stores/auth.js'
import { useProbeHistory } from '../composables/useProbeHistory.js'
import { copyText, prettyJSON } from '../utils/format.js'
import Icon from '../components/Icon.vue'
import StatusDot from '../components/StatusDot.vue'

const TYPES = [
  { v: 'detail', label: '网站', icon: 'Globe', tip: 'HTTP/HTTPS 双栈可达性与耗时' },
  { v: 'ssl', label: 'SSL 证书', icon: 'Lock', tip: '证书颁发者、有效期' },
  { v: 'dns', label: 'DNS', icon: 'Network', tip: '各地 DNS 解析结果' },
  { v: 'tcping', label: 'TCPing', icon: 'Zap', tip: 'TCP 端口连通与 RTT' },
  { v: 'speed', label: '测速', icon: 'Gauge', tip: '文件下载速度' },
  { v: 'location', label: 'IP 归属地', icon: 'MapPin', tip: 'IP 地理位置与运营商' },
  { v: 'whois', label: 'Whois', icon: 'FileSearch', tip: '域名注册信息' },
]
const TYPE_LABEL = Object.fromEntries(TYPES.map((t) => [t.v, t.label]))
const DNS_TYPES = ['a', 'aaaa', 'cname', 'mx', 'ns', 'txt', 'caa', 'srv', 'ptr']
const SUGGEST = [
  { apiType: 'detail', raw: 'https://www.qq.com', text: '检查 qq.com 在各地能否访问' },
  { apiType: 'ssl', raw: 'github.com', text: '查看 github.com 的证书' },
  { apiType: 'dns', raw: 'example.com', text: '解析 example.com 的 A 记录' },
  { apiType: 'tcping', raw: '1.1.1.1', text: 'TCPing 测试 1.1.1.1' },
]

const auth = useAuthStore()
const route = useRoute()
const router = useRouter()
const { add, get } = useProbeHistory()

const apiType = ref('detail')
const dnsType = ref('a')
const port = ref('')
const count = ref('')
const speedStack = ref('v4')
const nodeSel = ref('')
const text = ref('')
const sending = ref(false)
const focused = ref(false)
const typeMenu = ref(false)
const typeRoot = ref(null)
const messages = ref([])
const nodes = ref([])
const probeCost = ref(0)
const threadEl = ref(null)
const inputEl = ref(null)

const placeholder = computed(() => ({
  detail: '输入网址，如 https://example.com',
  ssl: '输入域名，如 example.com',
  dns: '输入要解析的域名',
  tcping: '输入主机或 IP',
  speed: '输入测速文件 URL',
  location: '输入 IP 地址',
  whois: '输入域名',
}[apiType.value]))

const poolNodes = computed(() =>
  nodes.value.filter((n) => {
    const pools = (n.pools || '').split(',')
    return apiType.value === 'location' ? pools.includes('location') : pools.includes('api')
  }),
)

const isOk = (r) => !r.error && r.status >= 200 && r.status < 300

function brief(type, r) {
  if (r.error) return r.error
  const b = r.body
  if (!b || typeof b !== 'object') return typeof b === 'string' ? b.slice(0, 100) : '无返回内容'
  const s = b.ipv4 || b.ipv6
  try {
    if (type === 'detail' && s) return `HTTP ${s.http_status_code ?? '—'} · HTTPS ${s.https_status_code ?? '—'} · ${s.total_time ?? '—'}ms`
    if (type === 'ssl' && s) return `${s.issuer_common_name || '—'} · 剩余 ${s.cert_validity_days ?? '—'} 天`
    if (type === 'dns' && Array.isArray(b.record)) return `${b.record.length} 条：${b.record.slice(0, 2).map((x) => (typeof x === 'object' ? JSON.stringify(x) : x)).join('，')}`
    if (type === 'tcping' && b.ipv4) return `成功 ${b.ipv4.success ?? '—'} 次 · RTT ${b.ipv4.avg_rtt ?? '—'}ms`
    if (type === 'speed') return `${b.is_reachable === false ? '不可达' : '可达'} · ${b.total_time ?? '—'}ms`
  } catch {}
  return JSON.stringify(b).slice(0, 100)
}

function buildRaw(input) {
  let raw = input.trim().replace(/^\/+/, '')
  const t = apiType.value
  if (['ssl', 'dns', 'whois', 'tcping', 'location'].includes(t)) raw = raw.replace(/^https?:\/\//, '').replace(/\/.*$/, '')
  if (t === 'dns') raw = `${dnsType.value}/${raw}`
  if (t === 'speed' && !/^v[46]\//.test(raw)) raw = `${speedStack.value}/${raw}`
  return raw
}

function scrollDown(smooth = true) {
  nextTick(() => {
    const el = document.querySelector('.main-scroll')
    if (el) el.scrollTo({ top: el.scrollHeight, behavior: smooth ? 'smooth' : 'auto' })
  })
}

function autoGrow() {
  const el = inputEl.value
  if (!el) return
  el.style.height = 'auto'
  el.style.height = Math.min(el.scrollHeight, 200) + 'px'
}

function pickType(v) {
  apiType.value = v
  typeMenu.value = false
  nextTick(() => inputEl.value?.focus())
}

async function probe(type, raw, query, nodesArg) {
  const id = Date.now().toString(36)
  const qs = new URLSearchParams(query).toString()
  const hint = nodesArg.length ? (nodes.value.find((n) => n.nodeId === nodesArg[0])?.label || nodesArg[0]) : '所有在线节点'
  messages.value.push({ id: id + 'u', role: 'user', apiType: type, raw, query: qs })
  messages.value.push({ id: id + 'a', role: 'bot', apiType: type, loading: true, nodeHint: hint })
  scrollDown()
  sending.value = true
  const target = () => messages.value.find((m) => m.id === id + 'a')
  try {
    const data = await api.runBatchProbe(type, raw, nodesArg, query)
    Object.assign(target(), { loading: false, data })
    add({ id, apiType: type, raw, query, nodes: nodesArg, data, at: Date.now() })
    router.replace({ name: 'probe', query: { h: id } })
  } catch (e) {
    Object.assign(target(), { loading: false, error: e.message })
  } finally {
    sending.value = false
    auth.refreshPoints()
    scrollDown()
  }
}

function send() {
  if (!text.value.trim() || sending.value) return
  const query = {}
  if (apiType.value === 'tcping') {
    if (port.value) query.port = port.value
    if (count.value) query.count = count.value
  }
  const raw = buildRaw(text.value)
  text.value = ''
  nextTick(autoGrow)
  probe(apiType.value, raw, query, nodeSel.value ? [nodeSel.value] : [])
}

function resend(m) {
  const u = messages.value[messages.value.indexOf(m) - 1]
  probe(m.apiType, u.raw, Object.fromEntries(new URLSearchParams(u.query || '')), nodeSel.value ? [nodeSel.value] : [])
}

async function copy(m) {
  if (await copyText(JSON.stringify(m.data, null, 2))) {
    m._copied = true
    setTimeout(() => (m._copied = false), 1500)
  }
}

function useSuggest(s) {
  apiType.value = s.apiType
  text.value = s.raw
  nextTick(() => {
    inputEl.value?.focus()
    autoGrow()
  })
}

function loadFromRoute() {
  const h = route.query.h && get(route.query.h)
  if (h) {
    if (messages.value.some((m) => m.id === h.id + 'a')) return
    messages.value = [
      { id: h.id + 'u', role: 'user', apiType: h.apiType, raw: h.raw, query: new URLSearchParams(h.query || {}).toString() },
      { id: h.id + 'a', role: 'bot', apiType: h.apiType, data: h.data },
    ]
    scrollDown(false)
  } else {
    messages.value = []
    nextTick(() => inputEl.value?.focus())
  }
}
watch(() => route.query, loadFromRoute)

function onDocClick(e) {
  if (typeMenu.value && typeRoot.value && !typeRoot.value.contains(e.target)) typeMenu.value = false
}

onMounted(async () => {
  document.addEventListener('mousedown', onDocClick)
  loadFromRoute()
  try {
    nodes.value = await api.fetchNodesBrief()
  } catch {}
  const p = await auth.refreshPoints()
  probeCost.value = p?.rules?.probeCost || 0
})
onUnmounted(() => document.removeEventListener('mousedown', onDocClick))
</script>

<style scoped>
.chat { min-height: 100%; display: flex; flex-direction: column; }
.chat.empty { justify-content: center; }

.thread { flex: 1; width: 100%; max-width: var(--content-w); margin: 0 auto; padding: 20px 24px 8px; }
.msg { margin: 0 0 28px; }
.msg.user { display: flex; justify-content: flex-end; }
.bubble { background: var(--bubble-user); padding: 10px 18px; border-radius: 22px; max-width: 75%; word-break: break-all; line-height: 1.55; }
.chip { font-size: 11px; color: var(--text-2); background: var(--bg); border: 1px solid var(--border); border-radius: 6px; padding: 1px 6px; margin-right: 8px; vertical-align: 1px; }

.answer { line-height: 1.7; }
.thinking { padding: 4px 0; font-size: 15px; }
.err-line { display: flex; align-items: center; gap: 8px; color: var(--danger); }
.summary { margin: 0 0 12px; }

.results { display: flex; flex-direction: column; border: 1px solid var(--border); border-radius: 16px; overflow: hidden; }
.res { border-bottom: 1px solid var(--border); animation: fadeUp 0.3s var(--ease) both; }
.res:last-child { border-bottom: none; }
.res-head {
  display: flex; align-items: center; gap: 12px; width: 100%; padding: 11px 14px; background: none; border: none;
  color: inherit; font: inherit; font-size: 14px; text-align: left; cursor: pointer; transition: background-color 0.15s;
}
.res-head:hover { background: var(--bg-soft); }
.node { flex: 0 1 170px; font-weight: 500; }
.brief { flex: 1; color: var(--text-2); font-size: 13px; }
.res.fail .brief { color: var(--danger); }
.meta { color: var(--text-3); font-size: 12px; flex-shrink: 0; }
.chev { color: var(--text-3); transition: transform 0.22s var(--ease); flex-shrink: 0; }
.res.open .chev { transform: rotate(90deg); }
/* 展开：grid-template-rows 0fr → 1fr 平滑高度动画 */
.res-body { display: grid; grid-template-rows: 0fr; transition: grid-template-rows 0.28s var(--ease); }
.res.open .res-body { grid-template-rows: 1fr; }
.res-body-inner { overflow: hidden; }
.res-body pre { margin: 0 14px 12px; }
.ellipsis { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; min-width: 0; }

.actions { display: flex; gap: 2px; margin-top: 6px; opacity: 0; transition: opacity 0.2s; }
.msg:hover .actions, .msg:last-child .actions { opacity: 1; }

/* ---- 输入框 ---- */
.composer-wrap { position: sticky; bottom: 0; width: 100%; max-width: var(--content-w); margin: 0 auto; padding: 6px 24px 14px; background: linear-gradient(to bottom, transparent, var(--bg) 30%); z-index: 5; }
.composer-wrap.centered { position: static; background: none; padding-bottom: 12vh; }
.hero { text-align: center; font-size: 28px; font-weight: 500; margin: 0 0 28px; letter-spacing: -0.01em; }

.composer {
  background: var(--composer); border-radius: 28px; padding: 12px 12px 10px 20px; box-shadow: var(--composer-ring);
  transition: box-shadow 0.2s var(--ease);
}
.composer.focus { box-shadow: 0 0 0 1px var(--border-strong), 0 6px 24px rgba(0, 0, 0, 0.08); }
textarea {
  width: 100%; border: none; outline: none; resize: none; background: transparent; color: var(--text);
  font: inherit; font-size: 16px; padding: 4px 0 8px; max-height: 200px; line-height: 1.5; display: block;
}
textarea::placeholder { color: var(--text-3); }
.bar { display: flex; align-items: center; gap: 6px; margin-left: -8px; flex-wrap: wrap; }

.tool { position: relative; }
.tool-btn {
  display: inline-flex; align-items: center; gap: 6px; border: 1px solid var(--border); background: transparent;
  color: var(--text); border-radius: 999px; padding: 5px 10px 5px 12px; font: inherit; font-size: 13px; cursor: pointer;
  transition: background-color 0.15s;
}
.tool-btn:hover, .tool-btn.on { background: var(--bg-hover); }
.caret { color: var(--text-3); transition: transform 0.2s var(--ease); }
.caret.up { transform: rotate(180deg); }
.tool-menu {
  position: absolute; left: 0; bottom: calc(100% + 8px); width: 280px; background: var(--bg-elev);
  border: 1px solid var(--border); border-radius: 16px; box-shadow: var(--shadow-lg); padding: 6px; z-index: 20;
  transform-origin: bottom left;
}
.composer-wrap.centered .tool-menu { bottom: auto; top: calc(100% + 8px); transform-origin: top left; }
.tm-item {
  display: flex; align-items: center; gap: 12px; width: 100%; padding: 8px 10px; border: none; background: none;
  color: var(--text); font: inherit; font-size: 14px; text-align: left; border-radius: 10px; cursor: pointer; line-height: 1.35;
}
.tm-item svg { color: var(--text-2); flex-shrink: 0; }
.tm-item:hover { background: var(--bg-hover); }
.tm-check { margin-left: auto; color: var(--text) !important; }

.opt { display: inline-flex; gap: 6px; }
.mini {
  border: 1px solid var(--border); background: transparent; color: var(--text); border-radius: 999px;
  padding: 5px 10px; font: inherit; font-size: 13px; outline: none; cursor: pointer;
}
.mini:focus { border-color: var(--border-strong); }
.node-sel { max-width: 180px; color: var(--text-2); }

.send {
  width: 36px; height: 36px; border-radius: 50%; border: none; background: var(--primary); color: var(--primary-fg);
  display: flex; align-items: center; justify-content: center; cursor: pointer; flex-shrink: 0;
  transition: opacity 0.15s, transform 0.12s var(--ease);
}
.send:hover:not(:disabled) { opacity: 0.85; }
.send:active:not(:disabled) { transform: scale(0.92); }
.send:disabled { opacity: 0.2; cursor: default; }
.hint { text-align: center; margin-top: 10px; }

.suggest { display: flex; flex-wrap: wrap; justify-content: center; gap: 8px; margin-top: 22px; }
.sg {
  display: inline-flex; align-items: center; gap: 8px; border: 1px solid var(--border); background: transparent;
  color: var(--text-2); border-radius: 999px; padding: 8px 14px; font: inherit; font-size: 13px; cursor: pointer;
  transition: background-color 0.15s, color 0.15s; animation: fadeUp 0.35s var(--ease) both;
}
.sg:hover { background: var(--bg-hover); color: var(--text); }

/* 动画 */
.msg-enter-active { transition: opacity 0.3s var(--ease), transform 0.3s var(--ease); }
.msg-enter-from { opacity: 0; transform: translateY(10px); }
.hero-enter-active { transition: opacity 0.35s var(--ease), transform 0.35s var(--ease); }
.hero-leave-active { transition: opacity 0.15s; }
.hero-enter-from { opacity: 0; transform: translateY(8px); }
.hero-leave-to { opacity: 0; }
.pop-enter-active, .pop-leave-active { transition: opacity 0.16s var(--ease), transform 0.16s var(--ease); }
.pop-enter-from, .pop-leave-to { opacity: 0; transform: translateY(6px) scale(0.97); }
.fade-enter-active, .fade-leave-active { transition: opacity 0.15s; }
.fade-enter-from, .fade-leave-to { opacity: 0; }
.swap-enter-active, .swap-leave-active { transition: opacity 0.12s, transform 0.12s; }
.swap-enter-from, .swap-leave-to { opacity: 0; transform: scale(0.6); }

@media (max-width: 760px) {
  .thread, .composer-wrap { padding-left: 14px; padding-right: 14px; }
  .bubble { max-width: 88%; }
  .node { flex-basis: 110px; }
  .brief { display: none; }
  .node-sel { max-width: 120px; }
  .hero { font-size: 24px; }
}
</style>
