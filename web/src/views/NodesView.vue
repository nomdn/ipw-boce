<template>
  <div>
    <div class="toolbar">
      <button class="ak-button ak-button--outline" @click="load">刷新节点</button>
      <span v-if="error" class="err">{{ error }}</span>
      <span class="dim" style="font-size:.85rem">在线 {{ onlineCount }} / {{ nodes.length }}</span>
    </div>
    <div v-if="loading" class="loading-center"><span class="ak-loading"></span></div>

    <div class="node-wall">
      <div v-for="n in nodes" :key="n.nodeId" class="node-card" :class="{ online: n.online }">
        <div class="node-head" style="display:flex;justify-content:space-between;align-items:center">
          <span class="node-id">
            <span class="dot" :class="n.online ? 'online' : 'offline'"></span>{{ n.nodeId }}
          </span>
          <span class="ak-tag ch" :class="n.online ? 'ak-tag--advanced' : 'ak-tag--neutral'">
            {{ n.online ? '在线' : '离线' }}
          </span>
        </div>
        <div class="node-label">{{ n.label || '（未命名）' }}</div>
        <div class="node-meta">
          <span>远端 {{ n.remoteAddr || '—' }}</span>
          <span>版本 {{ n.version || '—' }}</span>
        </div>
        <div class="node-meta">
          <span>最后活跃 {{ timeAgo(n.lastSeenAt) }}</span>
          <span>首见 {{ timeAgo(n.firstSeenAt) }}</span>
        </div>
        <div v-if="capsHint(n).hint" class="node-meta node-note" :class="capsHint(n).tone">
          <span>{{ capsHint(n).hint }}</span>
        </div>
        <div class="node-actions">
          <button class="ak-button ak-button--outline" @click="openEvents(n)">事件历史</button>
          <button class="ak-button ak-button--outline" @click="openOta(n)">OTA 升级</button>
        </div>
      </div>
      <div v-if="!nodes.length && !loading" class="panel" style="grid-column:1/-1">
        <h2 class="panel-title">暂无节点</h2>
        <p class="dim" style="margin:0">节点接入后会自动出现在这里。</p>
      </div>
    </div>

    <!-- ===== OTA 任务列表 ===== -->
    <div class="panel" style="margin-top:24px">
      <div class="ota-head">
        <h2 class="panel-title">OTA 升级任务 <span class="dim" style="font-size:.78rem;font-weight:400">节点重连后版本号变化即判定成功</span></h2>
        <button class="ak-button ak-button--outline" @click="loadOta">刷新</button>
      </div>
      <div class="ak-table-wrap">
        <table class="ak-table">
          <thead>
            <tr><th>#</th><th>节点</th><th>版本变化</th><th>通道</th><th>状态</th><th>阶段 / 原因</th><th>操作人</th><th>下发时间</th></tr>
          </thead>
          <tbody>
            <tr v-for="t in otaTasks" :key="t.id">
              <td class="mono dim">{{ t.id }}</td>
              <td class="mono">{{ t.nodeId }}</td>
              <td class="mono">{{ t.fromVersion || '—' }} → {{ t.targetVersion || (t.url ? '按下载地址' : '—') }}</td>
              <td>{{ t.channel === 'ws' ? 'WS' : 'HTTP' }}</td>
              <td>
                <span class="ota-status" :class="t.status">
                  {{ t.status === 'dispatched' ? '进行中' : t.status === 'success' ? '成功' : '失败' }}
                </span>
              </td>
              <td style="word-break:break-all">
                <span v-if="t.status === 'dispatched'" class="dim">{{ t.stage || '等待节点响应…' }}</span>
                <span v-else-if="t.error" class="err">{{ t.error }}</span>
                <span v-else class="dim">—</span>
              </td>
              <td class="dim">{{ t.dispatchedBy || '—' }}</td>
              <td class="mono nowrap">{{ fmtTime(t.dispatchedAt) }}</td>
            </tr>
            <tr v-if="!otaTasks.length"><td colspan="8" class="dim">暂无 OTA 任务</td></tr>
          </tbody>
        </table>
      </div>
    </div>

    <!-- OTA 下发对话框 -->
    <div v-if="ota.show" class="ev-mask" @click.self="ota.show = false">
      <div class="ev-panel">
        <header class="ev-head">
          <span class="mono">{{ ota.nodeId }}</span>
          <span class="dim">OTA 升级</span>
          <button class="ak-button ak-button--outline" style="margin-left:auto;padding:3px 10px" @click="ota.show = false">关闭</button>
        </header>
        <div class="ak-form-stack" style="margin-top:12px">
          <div class="switch-row">
            <span class="ak-label">方式</span>
            <button class="ak-toggle" :class="{ on: ota.mode === 'version' }" @click.prevent="ota.mode = 'version'">按版本（自动匹配发布资产）</button>
            <button class="ak-toggle" :class="{ on: ota.mode === 'url' }" @click.prevent="ota.mode = 'url'">直发下载地址</button>
          </div>
          <label class="ak-field" v-if="ota.mode === 'version'">
            <span class="ak-label">目标版本</span>
            <input class="ak-input" v-model.trim="ota.version" placeholder="如 v1.2.3（发布标签）" />
          </label>
          <label class="ak-field" v-if="ota.mode === 'url'">
            <span class="ak-label">下载地址</span>
            <input class="ak-input" v-model.trim="ota.url" placeholder="https://…（对应节点平台的二进制）" />
          </label>
          <label class="ak-field">
            <span class="ak-label">SHA256 <span class="dim">（可选校验）</span></span>
            <input class="ak-input" v-model.trim="ota.sha256" placeholder="64 位 hex，兼容 sha256: 前缀" />
          </label>
        </div>
        <p class="dim" style="font-size:.75rem;line-height:1.5;margin:10px 0 0">
          节点将下载并替换自身二进制后重启，期间会短暂断开连接，属正常现象；成败以重连后的版本号为准。
          老版本节点不认识该指令，会按任务超时处理。
        </p>
        <div v-if="ota.msg" class="ok-text" style="font-size:.8rem;margin-top:10px;word-break:break-all">{{ ota.msg }}</div>
        <div v-if="ota.err" class="err" style="font-size:.8rem;margin-top:10px;word-break:break-all">{{ ota.err }}</div>
        <div class="dlg-foot">
          <button class="ak-button ak-button--outline" @click="ota.show = false">取消</button>
          <button class="ak-button ak-button--action" :disabled="ota.busy" @click="submitOta">
            {{ ota.busy ? '下发中…' : '下发 OTA' }}
          </button>
        </div>
      </div>
    </div>

    <!-- 事件抽屉 -->
    <div v-if="drawer" class="ev-mask" @click.self="drawer = null">
      <div class="ev-panel">
        <header class="ev-head">
          <span class="mono">{{ drawer.nodeId }}</span>
          <span class="dim">在线/离线事件</span>
          <button class="ak-button ak-button--outline" style="margin-left:auto;padding:3px 10px" @click="drawer=null">关闭</button>
        </header>
        <div class="ak-table-wrap" style="max-height:60vh;overflow:auto">
          <table class="ak-table">
            <thead><tr><th>时间</th><th>事件</th><th>原因</th></tr></thead>
            <tbody>
              <tr v-for="e in events" :key="e.id">
                <td class="mono nowrap">{{ fmtTime(e.createdAt) }}</td>
                <td><span class="dot" :class="e.event === 'online' ? 'online' : 'offline'"></span>
                  <span :class="e.event === 'online' ? 'ok-200' : 'err'">{{ e.event === 'online' ? '上线' : '下线' }}</span></td>
                <td class="dim" style="word-break:break-all">{{ e.reason || '—' }}</td>
              </tr>
              <tr v-if="!events.length"><td colspan="3" class="dim">暂无事件</td></tr>
            </tbody>
          </table>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, reactive, computed, onMounted, onUnmounted } from 'vue'
import { fetchNodes, fetchNodeEvents, dispatchNodeOta, fetchOtaTasks } from '../api/boce.js'
import { timeAgo, fmtTime } from '../utils/format.js'

const nodes = ref([])
const events = ref([])
const drawer = ref(null)
const loading = ref(false)
const error = ref('')

// ===== OTA =====
const otaTasks = ref([])
const ota = reactive({
  show: false, nodeId: '', mode: 'version', version: '', url: '', sha256: '',
  busy: false, err: '', msg: '',
})
let otaTimer = null

const onlineCount = computed(() => nodes.value.filter((n) => n.online).length)

// 节点上报的能力清单（逗号分隔，如 "probe,report,config"；老版本节点不上报，为空串）。
// 节点版本号由 n.version 直接展示——节点不自更新，升级部署后重新注册即为新版本。
// 这里只在"未上报能力清单"时给一句排障提示：这类旧程序可能不认识控制台下发的运行时配置指令。
const capsOf = (n) => String(n?.capabilities || '').split(',').map((s) => s.trim()).filter(Boolean)
function capsHint(n) {
  if (capsOf(n).length) return { tone: '', hint: '' }
  return {
    tone: 'warn',
    hint: '旧版节点：未上报能力清单，运行时配置指令可能不被识别，建议升级节点程序',
  }
}

onMounted(() => {
  load()
  loadOta()
})
onUnmounted(() => { if (otaTimer) clearInterval(otaTimer) })

async function load() {
  loading.value = true
  error.value = ''
  try {
    nodes.value = await fetchNodes()
  } catch (e) {
    error.value = e?.message || '加载失败'
  } finally {
    loading.value = false
  }
}

async function openEvents(n) {
  drawer.value = n
  events.value = []
  try {
    events.value = await fetchNodeEvents(n.nodeId, 100)
  } catch (e) {
    // 拉取失败不能静默成"暂无事件"，否则会被误读为该节点从未有过事件
    error.value = e?.message || '事件历史加载失败'
  }
}

// ===== OTA =====
async function loadOta() {
  try {
    otaTasks.value = await fetchOtaTasks(50)
  } catch {
    otaTasks.value = []
  }
  // 有进行中的任务时 10s 轮询（节点重连/超时收敛后状态会变化），空闲即停
  if (otaTasks.value.some((t) => t.status === 'dispatched')) {
    if (!otaTimer) otaTimer = setInterval(loadOta, 10000)
  } else if (otaTimer) {
    clearInterval(otaTimer)
    otaTimer = null
  }
}

function openOta(n) {
  ota.show = true
  ota.nodeId = n.nodeId
  ota.mode = 'version'
  ota.version = ''
  ota.url = ''
  ota.sha256 = ''
  ota.err = ''
  ota.msg = ''
}

async function submitOta() {
  const payload = { sha256: ota.sha256 }
  if (ota.mode === 'version') {
    if (!ota.version) { ota.err = '请填写目标版本'; return }
    payload.version = ota.version
  } else {
    if (!ota.url) { ota.err = '请填写下载地址'; return }
    payload.url = ota.url
  }
  ota.busy = true; ota.err = ''; ota.msg = ''
  try {
    const d = await dispatchNodeOta(ota.nodeId, payload)
    ota.msg = d.hint || '已下发'
    await loadOta()
  } catch (e) {
    ota.err = e?.message || '下发失败'
  } finally {
    ota.busy = false
  }
}
</script>

<style scoped>
/* 旧版节点提示：贴在卡片上，按严重程度着色 */
.node-card .node-note { font-size: 0.75rem; line-height: 1.4; word-break: break-all; }
.node-card .node-note.ok { color: var(--ak-signal-success); }
.node-card .node-note.warn { color: var(--ak-signal-info); }
.node-card .node-note.err { color: var(--ak-signal-danger); }

/* ===== OTA ===== */
.ota-head {
  display: flex; align-items: center; justify-content: space-between; gap: 12px;
  margin-bottom: 12px;
}
.ota-head .panel-title { margin: 0; }
.ota-status {
  font-size: .75rem; padding: 2px 8px; border-radius: 3px;
  background: rgba(132, 131, 131, .18);
}
.ota-status.dispatched { background: rgba(74, 171, 234, .16); color: var(--ak-signal-info); }
.ota-status.success { background: rgba(0, 176, 80, .14); color: #00b050; }
.ota-status.failed { background: rgba(255, 59, 48, .12); color: #ff3b30; }
.dlg-foot { display: flex; justify-content: flex-end; gap: 10px; margin-top: 18px; }
.switch-row { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
.switch-row .ak-label { min-width: 56px; }
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

.ev-mask {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.55);
  z-index: 50;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 20px;
}
.ev-panel {
  width: min(640px, 100%);
  background: var(--ak-surface-inverse);
  border: var(--ak-line-hairline) solid rgba(255, 255, 255, 0.12);
  border-top: 3px solid var(--ak-signal-info);
  padding: 16px 18px;
  max-height: 80vh;
  display: flex;
  flex-direction: column;
}
.ev-head { display: flex; align-items: center; gap: 12px; padding-bottom: 10px; border-bottom: 1px solid rgba(255,255,255,.08); }
</style>
