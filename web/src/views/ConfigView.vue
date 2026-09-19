<template>
  <div>
    <!-- ===== 上游节点池（数据库托管，取代 setting.json 静态配置） ===== -->
    <div class="panel-head">
      <div>
        <h2 class="panel-title">上游节点池 <span class="hl">/ node-defs</span></h2>
        <p class="panel-sub">拨测转发与一键拨测的节点来源。存于数据库，保存即生效，无需改配置重启。一个节点可同时用于拨测与 IP 定位。</p>
      </div>
      <button class="ak-button ak-button--action" @click="openCreate">＋ 新增节点</button>
    </div>

    <!-- 已接入但库里没有对应配置 → 下拉框补录 -->
    <div v-if="online.length" class="adopt-bar">
      <span class="adopt-tip">检测到 <b>{{ online.length }}</b> 个已接入但尚未配置的节点：</span>
      <select class="ak-select" v-model="adoptId">
        <option value="">— 请选择节点 —</option>
        <option v-for="n in online" :key="n.nodeId" :value="n.nodeId">
          {{ n.nodeId }}{{ n.label ? ' · ' + n.label : '' }}{{ n.viaWs ? ' · WS' : '' }}
        </option>
      </select>
      <button class="ak-button" :disabled="!adoptId" @click="adopt">补录为节点</button>
      <span class="dim" style="font-size:.75rem">补录后该节点即可参与转发与拨测</span>
    </div>

    <div class="panel" style="margin-bottom:24px">
      <div class="ak-table-wrap">
        <table class="ak-table">
          <thead>
            <tr>
              <th>节点 ID</th><th>名称</th><th>归属池</th><th>协议栈</th><th>通道</th><th>上游地址</th><th>状态</th><th>凭据</th>
              <th style="width:230px">操作</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="n in defs" :key="n.id">
              <td class="mono">{{ n.nodeId }}</td>
              <td>{{ n.label || '—' }}</td>
              <td>
                <span class="pool-tags">
                  <span v-for="p in poolsOf(n.pool)" :key="p" class="ak-tag ch"
                    :class="p === 'location' ? 'ak-tag--neutral' : 'ak-tag--advanced'">{{ p }}</span>
                </span>
              </td>
              <td class="mono dim">{{ poolsOf(n.pool).includes('api') ? (n.stack || 'DualStack') : '—' }}</td>
              <td>{{ n.ws ? 'WS' : 'HTTP' }}</td>
              <td class="mono" :title="n.url">{{ n.url || '—' }}</td>
              <td>
                <span class="ak-tag ch state-tag" :class="n.enabled ? 'ak-tag--advanced' : 'ak-tag--neutral'">
                  {{ n.enabled ? '已启用' : '已停用' }}
                </span>
              </td>
              <td class="cred-cell">
                <span class="cred-mark" :class="{ on: n.hasWsKey }"
                  title="WS 注册密钥（ws-keys）：节点连上中间件时用它证明身份；未设置表示该节点无需验证">WS {{ n.hasWsKey ? '已设置' : '未设置' }}</span>
                <span class="cred-mark" :class="{ on: n.hasApiKey }"
                  title="HTTP 访问令牌（api-keys）：中间件访问该节点 HTTP 接口时携带；WS 通道的节点用不到">HTTP {{ n.hasApiKey ? '已设置' : '未设置' }}</span>
              </td>
              <td class="ops">
                <button class="link-btn" @click="toggleEnabled(n)">{{ n.enabled ? '停用' : '启用' }}</button>
                <button class="link-btn" @click="openEdit(n)">编辑</button>
                <button class="link-btn danger" @click="confirmDelete(n)">删除</button>
              </td>
            </tr>
            <tr v-if="!defs.length">
              <td colspan="9" class="dim">暂无节点。未配置任何节点时，转发会回退到配置文件里的默认上游。</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <!-- ===== 节点运行时配置（直连节点进程） ===== -->
    <section class="panel" style="margin-bottom:24px">
      <div class="panel-head">
        <div>
          <h2 class="panel-title">节点运行时配置 <span class="hl">/ 直连节点</span></h2>
          <p class="panel-sub">
            读取并修改节点进程<b>当前生效</b>的配置（与下方「托管配置」不同：那是节点启动时拉的远端配置）。
            优先走 WS 通道，WS 离线时回退 HTTP。凭据类字段节点只回显 ***，提交时自动剔除。
          </p>
        </div>
      </div>

      <div class="rt-bar">
        <div class="tb-group">
          <select class="ak-select" v-model="rt.nodeId" @change="rtReset">
            <option value="">— 选择节点 —</option>
            <option v-for="n in rtNodes" :key="n.id" :value="n.id">
              {{ n.id }}{{ n.label ? ' · ' + n.label : '' }}
            </option>
          </select>
        </div>
        <!-- 勾选项单独成组：不嵌进下拉组，避免组内换行后在下拉右侧留出空洞 -->
        <div class="tb-group">
          <label class="rt-persist">
            <input type="checkbox" v-model="rt.persist" /> 写回节点 setting.json
          </label>
        </div>
        <div class="tb-group tb-group--actions">
          <button class="ak-button" :disabled="!rt.nodeId || rt.busy" @click="rtPull">
            {{ rt.busy ? '处理中…' : '拉取配置' }}
          </button>
          <button class="ak-button" :disabled="!rt.nodeId || rt.busy" @click="rtRefresh">刷新远端配置</button>
        </div>
        <span v-if="rt.channel" class="rt-channel" :class="rt.channel">
          {{ rt.channel === 'ws' ? 'WS 通道' : 'HTTP 通道' }}
        </span>
      </div>

      <p v-if="rt.err" class="rt-err">{{ rt.err }}</p>

      <div v-if="rt.rows.length" class="ak-table-wrap">
        <table class="ak-table">
          <thead>
            <tr><th style="width:34%">配置项</th><th>值</th><th style="width:110px">状态</th></tr>
          </thead>
          <tbody>
            <tr v-for="r in rt.rows" :key="r.key">
              <td class="mono">
                {{ r.key }}
                <span v-if="rt.restartKeys.includes(r.key)" class="rt-lock warn" title="改动后需重启节点才生效">需重启</span>
                <span v-if="rt.protectedKeys.includes(r.key)" class="rt-lock"
                  title="托管配置（remote-config-url）里的同名键不会覆盖它，只能在节点本地用 ENV / setting.json 设置">远端不可覆盖</span>
              </td>
              <td>
                <span v-if="r.secret" class="dim mono">*** <span class="rt-lock">凭据 · 不下发</span></span>
                <select v-else-if="typeof r.origin === 'boolean'" class="ak-select" v-model="r.value">
                  <option :value="true">true</option>
                  <option :value="false">false</option>
                </select>
                <input v-else class="ak-input" v-model="r.value" :placeholder="String(r.origin ?? '')" />
              </td>
              <td>
                <span v-if="r.secret" class="dim">—</span>
                <span v-else-if="isChanged(r)" class="rt-changed">已改动</span>
                <span v-else class="dim">未改动</span>
              </td>
            </tr>
          </tbody>
        </table>

        <div style="margin-top:12px;display:flex;align-items:center;gap:12px;flex-wrap:wrap">
          <button class="ak-button ak-button--action" :disabled="rt.busy || !rtChangedCount" @click="rtSave">
            保存改动{{ rtChangedCount ? `（${rtChangedCount}）` : '' }}
          </button>
          <span class="dim" style="font-size:.75rem">只提交改动项</span>
        </div>
      </div>

      <!-- 下发结果回执 -->
      <div v-if="rt.result" class="rt-result">
        <div class="rt-line">
          <span class="dim">应用成功：</span>
          <span v-if="rt.result.applied?.length">{{ rt.result.applied.join('、') }}</span>
          <span v-else class="dim">无</span>
        </div>
        <div v-if="rt.result.unknown?.length" class="rt-line">
          <span class="dim">节点不识别：</span><span>{{ rt.result.unknown.join('、') }}</span>
        </div>
        <div v-if="rt.result.ignoredKeys?.length" class="rt-line">
          <span class="dim">已剔除：</span><span>{{ rt.result.ignoredKeys.join('、') }}（凭据类不下发）</span>
        </div>
        <div v-if="rt.result.protectedIgnored?.length" class="rt-line">
          <span class="dim">已跳过：</span>
          <span>{{ rt.result.protectedIgnored.join('、') }}（凭据由节点本地管理，远端配置不会覆盖）</span>
        </div>
        <div v-if="rt.result.restartRequired?.length" class="rt-warn">
          以下配置需<b>重启节点</b>才生效：{{ rt.result.restartRequired.join('、') }}
        </div>
        <div v-if="rt.result.unpersistedKeys?.length" class="rt-warn rt-persist-warn">
          <div>
            <b>持久化提醒</b>：{{ rt.result.unpersistedKeys.join('、') }}
            未写入托管配置——节点重启后 ENV 与本地 setting.json 会覆盖内存改动
            （"写回 setting.json" 的优先级也低于 ENV），这些改动会丢失。
          </div>
          <div style="margin-top:6px;display:flex;align-items:center;gap:10px;flex-wrap:wrap">
            <button class="ak-button ak-button--action" :disabled="rt.syncing" @click="rtSyncHosted">
              {{ rt.syncing ? '同步中…' : '同步到托管配置（推荐）' }}
            </button>
            <span v-if="rt.syncMsg" :class="rt.syncOk ? 'ok-text' : 'err'">{{ rt.syncMsg }}</span>
          </div>
        </div>
      </div>
    </section>

    <!-- ===== 节点远端配置托管 ===== -->
    <div class="grid-2" style="grid-template-columns: 340px minmax(0, 1fr)">
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
          <span class="field-hint">以 setting.json 同构的 JSON 对象保存；键用连接线（如 http-timeout-seconds）。非法 JSON 无法保存。
            注意 <span class="mono">access-token</span> / <span class="mono">report-token</span>
            属节点本地凭据，远端下发不会覆盖（写在这里也不生效），请在节点本地用 ENV 或 setting.json 设置。</span>

          <div v-if="resolved" style="margin-top:14px">
            <div class="ak-divider" style="margin:10px 0"></div>
            <h3 style="margin:6px 0;font-family:var(--ak-font-command);font-size:.95rem">合并结果（global 底 + 本节点覆盖）</h3>
            <div class="probe-card"><div class="pc-body"><pre class="raw-json">{{ resolved }}</pre></div></div>
          </div>
        </template>
      </section>
    </div>

    <!-- 节点新增 / 编辑对话框 -->
    <div v-if="dlg.show" class="mask" @click.self="closeDialog">
      <div class="dlg">
        <div class="dlg-title">{{ dlg.isEdit ? '编辑节点' : '新增节点' }}</div>
        <div class="ak-form-stack">
          <label class="ak-field">
            <span class="ak-label">节点 ID</span>
            <input class="ak-input" v-model.trim="dlg.form.nodeId" :disabled="dlg.isEdit"
              placeholder="如 cn-jiangsu（转发路径里的 backendID，唯一）" />
          </label>
          <label class="ak-field">
            <span class="ak-label">名称</span>
            <input class="ak-input" v-model.trim="dlg.form.label" placeholder="如 中国 江苏 移动" />
          </label>
          <div class="switch-row">
            <span class="ak-label">归属池</span>
            <button class="ak-toggle" :class="{ on: hasPool('api') }" @click.prevent="togglePool('api')">拨测 api</button>
            <button class="ak-toggle" :class="{ on: hasPool('location') }" @click.prevent="togglePool('location')">定位 location</button>
            <span class="dim sw-hint">可多选；双归属节点 location/asn 请求走定位池，其余走拨测池</span>
          </div>
          <label class="ak-field" v-if="hasPool('api')">
            <span class="ak-label">栈分组</span>
            <select class="ak-select" v-model="dlg.form.stack">
              <option value="DualStack">DualStack</option>
              <option value="IPv4">IPv4</option>
              <option value="IPv6">IPv6</option>
            </select>
          </label>
          <label class="ak-field">
            <span class="ak-label">上游地址</span>
            <input class="ak-input" v-model.trim="dlg.form.url" placeholder="https://node.example.com/（WS 节点可留空）" />
          </label>
          <div class="switch-row">
            <span class="ak-label">通道</span>
            <button class="ak-toggle" :class="{ on: dlg.form.ws }" @click.prevent="dlg.form.ws = !dlg.form.ws">
              {{ dlg.form.ws ? 'WS 通道' : 'HTTP 转发' }}
            </button>
            <span class="dim sw-hint">WS 节点须已注册到中间件并保持长连接</span>
          </div>
          <label class="ak-field">
            <span class="ak-label">WS 注册密钥</span>
            <div class="key-row">
              <input class="ak-input" type="password" autocomplete="new-password" v-model="dlg.form.wsKey"
                :placeholder="keyPlaceholder('wsKey')" @input="dlg.form.wsKeyTouched = true" />
              <button v-if="dlg.isEdit && dlg.hasWsKey" class="link-btn danger"
                @click.prevent="clearCred('wsKey')">清除</button>
            </div>
            <span class="dim key-hint">
              节点连上中间件时用它证明身份（对应配置文件的 ws-keys）。留空表示该节点无需验证；
              保存即生效，<b>无需重启中间件</b>。明文不回显，只显示是否已设置。
            </span>
          </label>
          <label class="ak-field">
            <span class="ak-label">HTTP 访问令牌</span>
            <div class="key-row">
              <input class="ak-input" type="password" autocomplete="new-password" v-model="dlg.form.apiKey"
                :placeholder="keyPlaceholder('apiKey')" @input="dlg.form.apiKeyTouched = true" />
              <button v-if="dlg.isEdit && dlg.hasApiKey" class="link-btn danger"
                @click.prevent="clearCred('apiKey')">清除</button>
            </div>
            <span class="dim key-hint">
              中间件访问该节点 HTTP 接口时携带（对应配置文件的 api-keys）。走 WS 通道的节点用不到，可留空。
            </span>
          </label>
          <div class="switch-row">
            <span class="ak-label">状态</span>
            <button class="ak-toggle" :class="{ on: dlg.form.enabled }" @click.prevent="dlg.form.enabled = !dlg.form.enabled">
              {{ dlg.form.enabled ? '启用' : '停用' }}
            </button>
          </div>
          <label class="ak-field">
            <span class="ak-label">排序</span>
            <input class="ak-input" type="number" v-model.number="dlg.form.sortOrder" placeholder="0" />
          </label>
        </div>
        <div class="dlg-err" v-if="dlg.err">{{ dlg.err }}</div>
        <div class="dlg-foot">
          <button class="ak-button ak-button--outline" @click="closeDialog">取消</button>
          <button class="ak-button ak-button--action" :disabled="dlg.busy" @click="saveDialog">
            {{ dlg.busy ? '保存中…' : '保存' }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, reactive, computed, onMounted } from 'vue'
import {
  fetchNodeConfigs, fetchNodeConfig, putNodeConfig, deleteNodeConfig, fetchResolvedConfig,
  mergeNodeHostedConfig,
  fetchNodeDefs, createNodeDef, updateNodeDef, deleteNodeDef, fetchUnconfiguredNodes,
  fetchNodeRuntimeConfig, applyNodeRuntimeConfig,
} from '../api/boce.js'
import { fmtTime } from '../utils/format.js'
import { API_BASE } from '../config.js'
import { useDialog } from '../composables/useDialog.js'

// ===== 托管配置（原有） =====
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

// ===== 上游节点池 =====
const defs = ref([])            // 节点定义列表（含停用）
const online = ref([])          // 已接入但未配置的节点（补录下拉框数据源）
const adoptId = ref('')         // 下拉框当前选中
const uiDialog = useDialog()

// 凭据两项：输入框留空 = 不改；*Touched 记录用户是否动过输入框，决定提交时是否带上该字段
// （不带 = 后端保持原值，带空串 = 清除）
const emptyForm = {
  nodeId: '', label: '', pools: ['api'], stack: 'DualStack', url: '', ws: false,
  enabled: true, sortOrder: 0, apiKey: '', wsKey: '', apiKeyTouched: false, wsKeyTouched: false,
}

// poolsOf 解析存储的 pool 字段（"api,location" → ['api','location']）；空值按 api 处理
function poolsOf(raw) {
  const arr = String(raw || '').split(',').map((s) => s.trim()).filter(Boolean)
  return arr.length ? arr : ['api']
}
const dlg = reactive({ show: false, isEdit: false, id: 0, form: { ...emptyForm }, err: '', busy: false, hasApiKey: false, hasWsKey: false })

onMounted(() => { load(); loadDefs() })
async function load() {
  try {
    list.value = await fetchNodeConfigs()
    if (list.value.length && !current.value) select(list.value[0])
  } catch (e) { err.value = e?.message }
}

// 节点定义 + 未配置候选一起拉（补录成功后候选自动减少）
async function loadDefs() {
  try { defs.value = await fetchNodeDefs() } catch { defs.value = [] }
  try { online.value = await fetchUnconfiguredNodes() } catch { online.value = [] }
}

function openCreate() {
  dlg.show = true
  dlg.isEdit = false
  dlg.id = 0
  dlg.form = { ...emptyForm, pools: [...emptyForm.pools] } // 数组需拷贝，避免共享引用
  dlg.hasApiKey = false
  dlg.hasWsKey = false
  dlg.err = ''
}

// hasPool / togglePool 归属池多选开关（至少一个）
function hasPool(p) {
  return dlg.form.pools.includes(p)
}
function togglePool(p) {
  const set = new Set(dlg.form.pools)
  if (set.has(p)) {
    if (set.size === 1) { dlg.err = '至少归属一个池'; return }
    set.delete(p)
  } else {
    set.add(p)
  }
  dlg.form.pools = ['api', 'location'].filter((x) => set.has(x)) // 固定顺序，与后端存储一致
  dlg.err = ''
}

function openEdit(n) {
  dlg.show = true
  dlg.isEdit = true
  dlg.id = n.id
  dlg.form = {
    nodeId: n.nodeId, label: n.label || '', pools: poolsOf(n.pool),
    stack: n.stack || 'DualStack', url: n.url || '', ws: !!n.ws,
    enabled: !!n.enabled, sortOrder: n.sortOrder || 0,
    // 凭据明文不回显：输入框始终为空，靠 has* 标记提示"已设置"；留空即不改，要清除用旁边的按钮
    apiKey: '', wsKey: '', apiKeyTouched: false, wsKeyTouched: false,
  }
  dlg.hasApiKey = !!n.hasApiKey
  dlg.hasWsKey = !!n.hasWsKey
  dlg.err = ''
}

// keyPlaceholder 凭据输入框的占位提示：编辑态且已设置时说明"留空即不改"
function keyPlaceholder(k) {
  const set = k === 'wsKey' ? dlg.hasWsKey : dlg.hasApiKey
  if (dlg.isEdit && set) return '已设置，留空则保持不变'
  return k === 'wsKey' ? '留空 = 该节点无需验证身份' : '留空 = 不设置'
}

// clearCred 清除某项凭据：置空并标记为已改动，保存后即从库中移除（改回用配置文件的同名键）
function clearCred(k) {
  dlg.form[k] = ''
  dlg.form[k + 'Touched'] = true
}

function closeDialog() {
  if (dlg.busy) return
  dlg.show = false
}

// adopt 把下拉框选中的在线节点预填进新增表单（WS 节点自动切到 WS 通道）
function adopt() {
  const pick = online.value.find((n) => n.nodeId === adoptId.value)
  if (!pick) return
  openCreate()
  dlg.form.nodeId = pick.nodeId
  dlg.form.label = pick.label || pick.nodeId
  dlg.form.ws = !!pick.viaWs
  dlg.err = ''
}

async function saveDialog() {
  dlg.err = ''
  const form = dlg.form
  if (!form.nodeId) { dlg.err = '节点 ID 必填'; return }
  if (!form.ws && !form.url) { dlg.err = 'HTTP 节点必须填写上游地址'; return }
  if (!form.pools.length) { dlg.err = '至少选择一个归属池'; return }
  dlg.busy = true
  try {
    const payload = {
      nodeId: form.nodeId, label: form.label, url: form.url, ws: form.ws,
      pool: form.pools.join(','),
      stack: form.pools.includes('api') ? form.stack : '',
      enabled: form.enabled, sortOrder: form.sortOrder || 0,
    }
    // 凭据：只有用户动过输入框（或点了清除）才带上该字段——不带即不动库里的值
    if (form.apiKeyTouched) payload.apiKey = form.apiKey
    if (form.wsKeyTouched) payload.wsKey = form.wsKey
    if (dlg.isEdit) await updateNodeDef(dlg.id, payload)
    else await createNodeDef(payload)
    dlg.show = false
    adoptId.value = ''
    await loadDefs()
  } catch (e) {
    dlg.err = e?.message || '保存失败'
  } finally { dlg.busy = false }
}

async function toggleEnabled(n) {
  try {
    await updateNodeDef(n.id, { enabled: !n.enabled })
    await loadDefs()
  } catch (e) { dlg.err = e?.message || '操作失败' }
}

async function confirmDelete(n) {
  const ok = await uiDialog.confirm({
    title: `删除节点 ${n.nodeId}？`,
    message: '删除后该节点立即从转发与拨测节点池中移除（历史拨测数据不受影响）。',
    kind: 'danger',
    confirmText: '删除',
    cancelText: '取消',
  })
  if (!ok) return
  try {
    await deleteNodeDef(n.id)
    await loadDefs()
  } catch (e) { /* 后端已给错误提示 */ }
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

// ===== 节点运行时配置（直连节点进程） =====
const rt = reactive({
  nodeId: '', rows: [], busy: false, err: '',
  channel: '', secretKeys: [], restartKeys: [], persist: false, result: null,
  protectedKeys: [],  // 远端下发不会覆盖的凭据键（节点回报，见 configRemoteProtectedKeys）
  lastCfg: null,   // 最近一次成功下发的改动键值（同步托管配置用）
  syncing: false,  // 同步托管配置进行中
  syncMsg: '',     // 同步结果提示
  syncOk: false,   // 同步是否成功（着色）
})

// 候选节点：已配置的节点池 + 已接入但未补录的在线节点（后者 WS 在线，可直接管理）
const rtNodes = computed(() => {
  const seen = new Set()
  const out = []
  defs.value.forEach(n => {
    if (!seen.has(n.nodeId)) { seen.add(n.nodeId); out.push({ id: n.nodeId, label: n.label }) }
  })
  online.value.forEach(n => {
    if (!seen.has(n.nodeId)) { seen.add(n.nodeId); out.push({ id: n.nodeId, label: n.label || '未配置' }) }
  })
  return out
})

// 行是否改动过（布尔走 select、数字走 input，统一按字符串比较避免类型误判）
function isChanged(r) {
  return !r.secret && String(r.value) !== String(r.origin)
}

const rtChangedCount = computed(() => rt.rows.filter(isChanged).length)

// 把节点返回的配置快照铺成可编辑行（凭据行只读）
function rtRowsFrom(cfg) {
  return Object.keys(cfg || {}).sort().map(k => ({
    key: k, origin: cfg[k], value: cfg[k],
    secret: rt.secretKeys.includes(k),
  }))
}

function rtReset() {
  rt.rows = []; rt.result = null; rt.err = ''; rt.channel = ''
  rt.lastCfg = null; rt.syncMsg = ''; rt.syncOk = false
}

async function rtPull() {
  if (!rt.nodeId) return
  rt.busy = true; rt.err = ''; rt.result = null
  try {
    const d = await fetchNodeRuntimeConfig(rt.nodeId)
    rt.channel = d.channel
    rt.secretKeys = d.secretKeys || []
    rt.restartKeys = d.restartKeys || []
    rt.protectedKeys = d.remoteProtectedKeys || []
    rt.rows = rtRowsFrom(d.config)
  } catch (e) {
    rt.err = e?.message || '拉取失败'
    rt.rows = []
  } finally {
    rt.busy = false
  }
}

async function rtRefresh() {
  if (!rt.nodeId) return
  rt.busy = true; rt.err = ''; rt.result = null
  try {
    const d = await applyNodeRuntimeConfig(rt.nodeId, { action: 'refresh' })
    rt.channel = d.channel
    rt.secretKeys = d.secretKeys || rt.secretKeys
    rt.protectedKeys = d.remoteProtectedKeys || rt.protectedKeys
    rt.rows = rtRowsFrom(d.config)
    rt.result = d
  } catch (e) {
    rt.err = e?.message || '刷新失败'
  } finally {
    rt.busy = false
  }
}

async function rtSave() {
  const cfg = {}
  rt.rows.forEach(r => {
    if (!isChanged(r)) return
    // 保持原值类型：布尔走 select、数字转 Number，其余按字符串序列化
    cfg[r.key] = typeof r.origin === 'number' && r.value !== '' ? Number(r.value) : r.value
  })
  if (!Object.keys(cfg).length) return
  rt.busy = true; rt.err = ''; rt.result = null; rt.syncMsg = ''; rt.syncOk = false
  try {
    const d = await applyNodeRuntimeConfig(rt.nodeId, { action: 'patch', config: cfg, persist: rt.persist })
    rt.channel = d.channel
    rt.rows = rtRowsFrom(d.config)
    rt.result = d
    rt.lastCfg = cfg // 留档本次改动值，供"同步到托管配置"一键持久化
  } catch (e) {
    rt.err = e?.message || '保存失败'
  } finally {
    rt.busy = false
  }
}

// rtSyncHosted 把本次下发的改动键值合并进该节点的托管配置（持久化）。
// 远端配置优先级最高（远端 > ENV > setting.json），重启后自动生效；
// 未配 remote-config-url 的节点此同步不生效，需另行处理（提示语见后端 persistHint）。
async function rtSyncHosted() {
  if (!rt.nodeId || !rt.lastCfg || !rt.result?.unpersistedKeys?.length) return
  const pick = {}
  rt.result.unpersistedKeys.forEach((k) => {
    if (rt.lastCfg[k] !== undefined) pick[k] = rt.lastCfg[k]
  })
  if (!Object.keys(pick).length) return
  rt.syncing = true
  rt.syncMsg = ''
  try {
    await mergeNodeHostedConfig(rt.nodeId, pick)
    rt.result.unpersistedKeys = []
    rt.syncOk = true
    rt.syncMsg = '已同步到托管配置：重启后以远端配置生效'
    await load() // 左侧托管配置列表刷新（新增了该节点的配置项）
  } catch (e) {
    rt.syncOk = false
    rt.syncMsg = e?.message || '同步失败'
  } finally {
    rt.syncing = false
  }
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
/* ===== 节点池区块 ===== */
.panel-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 14px;
}
.panel-title { margin: 0; font-size: 1.05rem; font-family: var(--ak-font-command); }
.panel-sub { margin: 4px 0 0; color: var(--ak-text-secondary); font-size: 0.82rem; }

/* 未配置节点提醒条（下拉框补录） */
.adopt-bar {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
  padding: 10px 14px;
  margin-bottom: 14px;
  background: color-mix(in srgb, var(--ak-signal-warn, #ffab00) 12%, transparent);
  border: 1px solid color-mix(in srgb, var(--ak-signal-warn, #ffab00) 34%, transparent);
  font-size: 0.82rem;
}
.adopt-bar .ak-select { max-width: 300px; flex: 1 1 220px; }
.adopt-tip b { color: var(--ak-signal-warn, #ffab00); }

.ops { white-space: nowrap; }
.state-tag { text-transform: none; }

/* ===== 节点运行时配置 ===== */
.rt-bar {
  display: flex; align-items: center; gap: 10px; flex-wrap: wrap;
  margin-bottom: 12px;
}
.rt-bar .ak-select { max-width: 260px; }
.rt-persist { display: inline-flex; align-items: center; gap: 6px; font-size: .82rem; }
.rt-channel {
  font-size: .75rem; padding: 2px 8px; border-radius: 3px; letter-spacing: .05em;
}
.rt-channel.ws { background: color-mix(in srgb, var(--ak-signal-success) 18%, transparent); color: var(--ak-signal-success); }
.rt-channel.http { background: color-mix(in srgb, var(--ak-signal-warn) 18%, transparent); color: var(--ak-signal-warn); }
.rt-err {
  margin: 0 0 12px; padding: 8px 12px; font-size: .82rem;
  background: color-mix(in srgb, var(--ak-signal-danger) 18%, transparent); color: var(--ak-signal-danger); border-radius: 4px;
}
.rt-lock {
  font-size: .68rem; padding: 1px 6px; margin-left: 6px; border-radius: 3px;
  background: var(--ui-tint);
}
.rt-lock.warn { background: color-mix(in srgb, var(--ak-signal-warn) 20%, transparent); color: var(--ak-signal-warn); }
.rt-changed { color: var(--ak-signal-success); font-size: .78rem; }
.rt-result {
  margin-top: 14px; padding: 10px 12px; border-radius: 4px; font-size: .82rem;
  background: var(--ui-tint-ghost);
}
.rt-line { margin-bottom: 4px; word-break: break-all; }
.rt-warn {
  margin-top: 8px; padding: 6px 10px; border-radius: 3px;
  background: color-mix(in srgb, var(--ak-signal-warn) 16%, transparent); color: var(--ak-signal-warn);
}
.rt-persist-warn { line-height: 1.5; }
.ok-text { color: var(--ak-signal-success); }
.pool-tags { display: flex; gap: 4px; flex-wrap: wrap; }
.link-btn {
  background: none;
  border: none;
  color: var(--ak-signal-info);
  cursor: pointer;
  font-size: 0.78rem;
  padding: 2px 6px;
}
.link-btn.danger { color: var(--ak-signal-danger); }

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

/* 对话框 */
.mask {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.45);
  display: flex;
  align-items: flex-start;
  justify-content: center;
  padding-top: 10vh;
  z-index: 50;
}
.dlg {
  width: min(440px, calc(100vw - 40px));
  max-height: 84vh;
  overflow: auto;
  background: var(--ak-surface-raised);
  border: var(--ak-line-hairline) solid rgba(0, 0, 0, 0.12);
  border-top: 3px solid var(--ak-signal-info);
  padding: 20px 22px;
  box-shadow: 0 20px 50px rgba(0, 0, 0, 0.35);
}
.dlg-title { font-family: var(--ak-font-command); font-weight: 600; margin-bottom: 14px; }
.dlg-err { margin-top: 10px; color: var(--ak-signal-danger); font-size: 0.8rem; }
.dlg-foot { display: flex; justify-content: flex-end; gap: 10px; margin-top: 18px; }
.switch-row { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
.switch-row .ak-label { min-width: 56px; }
.switch-row .sw-hint { font-size: 0.72rem; }

/* ===== 托管配置区块（原有） ===== */
.cfg-list { display: flex; flex-direction: column; gap: 6px; max-height: 60vh; overflow: auto; }
.cfg-row {
  display: flex; flex-direction: column; gap: 5px; padding: 9px 10px;
  border: var(--ak-line-hairline) solid var(--ui-line);
  cursor: pointer; transition: background var(--ak-motion-fast);
}
.cfg-row:hover { background: var(--ui-tint); }
.cfg-row.active { border-left: 3px solid var(--ak-signal-info); background: color-mix(in srgb, var(--ak-signal-info) 16%, transparent); }
/* 第二行：元信息(左) + 操作按钮(右)；nodeId 独占首行，长名不挤压按钮 */
.cfg-row .row2 { display: flex; align-items: center; gap: 8px; }
.cfg-row .row2 .dim { flex: 1; min-width: 0; word-break: break-all; }
.cfg-row .row2 .ops { flex: none; display: flex; gap: 5px; }
.cfg-row .row2 .ops > button { min-width: 0; flex: none; width: auto; height: auto; padding: 4px 9px; font-weight: 400; }
.cfg-row .ops .del { padding: 2px 7px; font-size: .72rem; opacity: .6; }
.cfg-row .ops .del:hover { opacity: 1; }
.cfg-row .ops .copy { padding: 3px 8px; font-size: .72rem; white-space: nowrap; }
.cfg-row.active .copy { border-color: var(--ak-signal-info); color: var(--ak-signal-info); }
/* 凭据列：只显示"已设置 / 未设置"，明文由后端保管、不回显 */
.cred-cell { white-space: nowrap; font-size: 0.72rem; }
.cred-mark { display: inline-block; margin-right: 8px; color: var(--ak-text-secondary); }
.cred-mark.on { color: var(--ak-signal-success); }
/* 凭据输入行：输入框占满，清除按钮贴右 */
.key-row { display: flex; align-items: center; gap: 8px; }
.key-row .ak-input { flex: 1; }
.key-hint { display: block; margin-top: 4px; font-size: 0.72rem; line-height: 1.55; }
</style>
