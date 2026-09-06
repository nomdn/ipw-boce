<template>
  <div>
    <div class="grid-2" style="grid-template-columns: 340px 1fr">
      <!-- 左侧：配置项列表 -->
      <section class="panel">
        <h2 class="panel-title">托管配置 <span class="hl">/ node-configs</span></h2>
        <div style="display:flex;gap:6px;margin-bottom:10px">
          <input class="ak-input" v-model.trim="newId" placeholder="新增 nodeId 或 global" style="flex:1"
            @keyup.enter="addConfig" />
          <button class="ak-button" @click="addConfig">新增</button>
        </div>
        <div class="cfg-list">
          <div v-for="c in list" :key="c.nodeId" class="cfg-row"
            :class="{ active: current && current.nodeId === c.nodeId }" @click="select(c)">
            <div class="mono" style="font-weight:600;word-break:break-all">{{ c.nodeId }}</div>
            <div class="row2">
              <span class="dim" style="font-size:.72rem">@ {{ fmtTime(c.updatedAt) }} · {{ c.sizeBytes }}B</span>
              <span class="ops">
                <button class="ak-button ak-button--outline copy" :title="remoteConfigUrl(c.nodeId)"
                  @click.stop="copyConfigUrl(c.nodeId)">{{ copiedId === c.nodeId ? '✓ 已复制' : '复制' }}</button>
                <button class="ak-button ak-button--outline del" title="删除" @click.stop="remove(c)">✕</button>
              </span>
            </div>
          </div>
          <div v-if="!list.length" class="dim" style="padding:10px 4px">暂无托管配置</div>
        </div>
        <p class="field-hint" style="margin-top:12px">nodeId=global 即全局缺省配置，被各节点 /remote-config 拉取。api-keys/ws-keys 等密钥不随远端下发，由节点本地管理。</p>
      </section>

      <!-- 右侧：编辑 / 合并预览 -->
      <section class="panel">
        <h2 class="panel-title">编辑 <span class="hl">/ {{ current ? current.nodeId : '—' }}</span></h2>
        <div v-if="!current" class="dim" style="padding:20px 4px">从左侧选择一个配置项，或新增一个节点开始编辑。</div>
        <template v-else>
          <div class="toolbar">
            <button class="ak-button ak-button--outline" @click="reloadCurrent">重新载入</button>
            <button class="ak-button ak-button--action" @click="save" :disabled="saving">{{ saving ? '保存中…' : '保存配置' }}</button>
            <button class="ak-button" @click="showResolved" :disabled="resolving">{{ resolving ? '查询中…' : '查看合并 /remote-config' }}</button>
            <span v-if="msg" class="dim" style="font-size:.85rem">{{ msg }}</span>
            <span v-if="err" class="err" style="font-size:.85rem">{{ err }}</span>
          </div>

          <div class="form-field" style="margin-bottom:10px">
            <label>配置 JSON</label>
            <textarea class="ak-textarea" v-model="text" rows="18" spellcheck="false"
              style="font-family:var(--ak-font-mono);font-size:.82rem;line-height:1.5"></textarea>
          </div>
          <span class="field-hint">以 setting.json 同构的 JSON 对象保存；键用连接线（如 http-timeout-seconds）。非法 JSON 无法保存。</span>

          <div v-if="resolved" style="margin-top:14px">
            <div class="ak-divider" style="margin:10px 0"></div>
            <h3 style="margin:6px 0;font-family:var(--ak-font-command);font-size:.95rem">合并结果（global 底 + 本节点覆盖）</h3>
            <div class="probe-card"><div class="pc-body"><pre class="raw-json">{{ resolved }}</pre></div></div>
          </div>
        </template>
      </section>
    </div>
  </div>
</template>

<script setup>
import { ref, onMounted } from 'vue'
import {
  fetchNodeConfigs, fetchNodeConfig, putNodeConfig, deleteNodeConfig, fetchResolvedConfig,
} from '../api/boce.js'
import { fmtTime } from '../utils/format.js'
import { API_BASE } from '../config.js'

const list = ref([])
const current = ref(null)
const text = ref('')
const newId = ref('')
const msg = ref('')
const err = ref('')
const saving = ref(false)
const resolving = ref(false)
const resolved = ref('')
const copiedId = ref(null) // 最近复制成功的 nodeId，按钮短暂显示 ✓ 已复制

onMounted(load)
async function load() {
  try {
    list.value = await fetchNodeConfigs()
    if (list.value.length && !current.value) select(list.value[0])
  } catch (e) { err.value = e?.message }
}

async function select(c) {
  current.value = { nodeId: c.nodeId }
  msg.value = ''
  resolved.value = ''
  try {
    const raw = await fetchNodeConfig(c.nodeId)
    text.value = typeof raw === 'string' ? raw : JSON.stringify(raw, null, 2)
  } catch (e) {
    err.value = e?.message
    text.value = ''
  }
}

async function addConfig() {
  const id = newId.value.trim()
  if (!id) return
  if (list.value.some((c) => c.nodeId === id)) { select({ nodeId: id }); newId.value = ''; return }
  current.value = { nodeId: id }
  text.value = '{}'
  newId.value = ''
  msg.value = '新配置将使用空对象，保存后生效'
}

function validate() {
  try { return JSON.parse(text.value) } catch (e) { err.value = 'JSON 解析失败：' + e.message; return null }
}

async function save() {
  if (saving.value || !current.value) return
  const obj = validate()
  if (!obj) return
  if (Array.isArray(obj) || obj === null || typeof obj !== 'object') {
    err.value = '配置必须是 JSON 对象'
    return
  }
  saving.value = true
  msg.value = ''
  err.value = ''
  try {
    await putNodeConfig(current.value.nodeId, obj)
    msg.value = '已保存'
    await load()
  } catch (e) { err.value = e?.message } finally { saving.value = false }
}

async function remove(c) {
  try {
    await deleteNodeConfig(c.nodeId)
    if (current.value?.nodeId === c.nodeId) current.value = null
    await load()
  } catch (e) { err.value = e?.message }
}

// remoteConfigUrl 拼出该配置项供节点拉取的公开 URL（global → /remote-config，其余 → /remote-config/:nodeId）
function remoteConfigUrl(nodeId) {
  const seg = nodeId === 'global' ? 'remote-config' : `remote-config/${encodeURIComponent(nodeId)}`
  return `${API_BASE}/${seg}`
}

// copyConfigUrl 一键复制 config URL 到剪贴板（供粘为节点 remote-config-url）
async function copyConfigUrl(nodeId) {
  msg.value = ''
  const url = remoteConfigUrl(nodeId)
  try {
    await navigator.clipboard.writeText(url)
  } catch (e) {
    // 非安全上下文回退：临时 textarea 选中复制
    const ta = document.createElement('textarea')
    ta.value = url
    ta.style.position = 'fixed'
    ta.style.opacity = '0'
    document.body.appendChild(ta)
    ta.select()
    document.execCommand('copy')
    document.body.removeChild(ta)
  }
  copiedId.value = nodeId
  setTimeout(() => { if (copiedId.value === nodeId) copiedId.value = null }, 1600)
  msg.value = `已复制：${url}`
}

async function showResolved() {
  if (!current.value) return
  resolving.value = true
  resolved.value = ''
  try {
    const data = await fetchResolvedConfig(current.value.nodeId)
    resolved.value = typeof data === 'string' ? data : JSON.stringify(data, null, 2)
  } catch (e) {
    err.value = e?.message
    resolved.value = ''
  } finally { resolving.value = false }
}

async function reloadCurrent() {
  if (current.value) await select(current.value)
}
</script>

<style scoped>
.cfg-list { display: flex; flex-direction: column; gap: 6px; max-height: 60vh; overflow: auto; }
.cfg-row {
  display: flex; flex-direction: column; gap: 5px; padding: 9px 10px;
  border: var(--ak-line-hairline) solid rgba(255,255,255,.08);
  cursor: pointer; transition: background var(--ak-motion-fast);
}
.cfg-row:hover { background: rgba(255,255,255,.04); }
.cfg-row.active { border-left: 3px solid var(--ak-signal-info); background: rgba(74,171,234,.1); }
/* 第二行：元信息(左) + 操作按钮(右)；nodeId 独占首行，长名不挤压按钮 */
.cfg-row .row2 { display: flex; align-items: center; gap: 8px; }
.cfg-row .row2 .dim { flex: 1; min-width: 0; word-break: break-all; }
.cfg-row .row2 .ops { flex: none; display: flex; gap: 5px; }
.cfg-row .row2 .ops > button { min-width: 0; flex: none; width: auto; height: auto; padding: 4px 9px; font-weight: 400; }
.cfg-row .ops .del { padding: 2px 7px; font-size: .72rem; opacity: .6; }
.cfg-row .ops .del:hover { opacity: 1; }
.cfg-row .ops .copy { padding: 3px 8px; font-size: .72rem; white-space: nowrap; }
.cfg-row.active .copy { border-color: var(--ak-signal-info); color: var(--ak-signal-info); }
</style>
