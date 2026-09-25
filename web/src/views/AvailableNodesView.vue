<template>
  <div>
    <div class="toolbar">
      <div class="tb-group">
        <div class="form-field">
          <label>筛选</label>
          <input class="ak-input" v-model.trim="kw" placeholder="节点标识或地区" style="width:200px" />
        </div>
      </div>
      <div class="tb-group tb-group--actions">
        <button class="ak-button ak-button--outline" @click="load">刷新</button>
      </div>
      <span v-if="error" class="err">{{ error }}</span>
      <span class="dim" style="font-size:.85rem">在线 {{ onlineCount }} / {{ nodes.length }}</span>
    </div>

    <div v-if="loading" class="loading-center"><span class="ak-loading"></span></div>

    <div class="ak-table-wrap">
      <table class="ak-table">
        <thead>
          <tr>
            <th>节点标识</th><th>地区</th><th>归属池</th><th>协议栈</th><th>通道</th><th>状态</th><th>版本</th>
            <th style="width:160px">调用前缀</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="n in filtered" :key="n.nodeId">
            <td class="mono nowrap">{{ n.nodeId }}</td>
            <td>{{ n.label || '—' }}</td>
            <td>
              <span class="pool-tags">
                <span v-for="p in poolsOf(n.pools)" :key="p" class="ak-tag ch"
                  :class="p === 'location' ? 'ak-tag--neutral' : 'ak-tag--advanced'">{{ poolText(p) }}</span>
              </span>
            </td>
            <td class="mono dim">{{ stackText(n) }}</td>
            <td>{{ n.ws ? 'WS 长连接' : 'HTTP 转发' }}</td>
            <td class="nowrap">
              <span class="dot" :class="n.online ? 'online' : 'offline'"></span>
              <span :class="n.online ? 'ok-text' : 'dim'">{{ n.online ? '在线' : '离线' }}</span>
            </td>
            <td class="mono dim">{{ n.version || '—' }}</td>
            <td>
              <button class="ak-button ak-button--outline sm" :title="prefix(n)" @click="copy(prefix(n), n.nodeId)">
                {{ copied === n.nodeId ? '✓ 已复制' : '复制' }}
              </button>
            </td>
          </tr>
          <tr v-if="!filtered.length && !loading">
            <td colspan="8" class="dim">{{ nodes.length ? '没有匹配的节点，换个关键字试试' : '暂无可用节点' }}</td>
          </tr>
        </tbody>
      </table>
    </div>

    <p v-if="nodes.length" class="dim hint">
      列表只含已启用的节点；离线的节点调用会失败（返回 502），请优先选在线节点。
      节点不是固定不变的，新接入的节点会自动出现在这里。
    </p>

    <!-- 用法说明与接口类型总览：一律放在节点列表之后 -->
    <div class="panel callout">
      <h2 class="panel-title">怎么调用这些节点 <span class="hl">/ 节点标识 + 接口类型 + 目标</span></h2>
      <p class="dim callout-p">
        表格里的<strong>节点标识</strong>是调用地址中的一段。在<strong>本控制台的接口地址</strong>后面接上
        <code class="mono">/v1/&lt;节点标识&gt;/&lt;接口类型&gt;/&lt;目标&gt;</code> 就是一条完整地址；
        目标可以自带一段斜杠（如 <code class="mono">v4/example.com</code>、<code class="mono">a/example.com</code>），
        站点类接口也可以直接写成 <code class="mono">https://</code> 开头。
      </p>
      <p class="dim callout-hint">
        下面每种接口类型都给出一条可直接粘贴执行的实例，用的是当前在线的节点；换节点只需把中间那段标识换掉。
      </p>

      <h3 class="type-title">支持的接口类型 <span class="hl">/ 共 {{ apiTypes.length }} 种</span></h3>
      <p class="dim callout-p">
        接口类型决定了走哪个归属池：<strong>拨测</strong>池接连通性、证书、解析类请求，
        <strong>定位</strong>池接 IP 归属与 ASN 查询；两个池都有的节点两种都能接。
        不在下表里的类型会被拒绝（返回 <code class="mono">400 Invalid API type</code>）。
      </p>
      <div v-for="g in typeGroups" :key="g.pool" class="type-group">
        <h4 class="type-group-title">
          <span class="ak-tag ch" :class="g.pool === 'location' ? 'ak-tag--neutral' : 'ak-tag--advanced'">
            {{ poolText(g.pool) }}池
          </span>
          <span class="dim">{{ g.hint }}</span>
        </h4>
        <ul class="type-list">
          <li v-for="t in g.items" :key="t.slug" class="type-item">
            <div class="type-head">
              <code class="mono type-slug">{{ t.slug }}</code>
              <span class="type-name">{{ t.name }}</span>
              <span class="dim type-target">目标 <code class="mono">{{ t.target }}</code></span>
            </div>
            <div class="type-ex">
              <code class="mono type-url" :title="sampleUrl(t)">{{ sampleUrl(t) }}</code>
              <button class="ak-button ak-button--outline sm" @click="copy(sampleUrl(t), 'type-' + t.slug)">
                {{ copied === 'type-' + t.slug ? '✓ 已复制' : '复制' }}
              </button>
            </div>
            <p class="type-desc dim">{{ t.desc }}</p>
          </li>
        </ul>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, onMounted } from 'vue'
import { fetchNodesBrief } from '../api/boce.js'
import { API_BASE } from '../config.js'

// 登录即可访问（后端 /admin/nodes/brief 对 user 开放，且已脱敏：不给上游地址与凭据）
const nodes = ref([])
const loading = ref(false)
const error = ref('')
const kw = ref('')
const copied = ref('')

const onlineCount = computed(() => nodes.value.filter((n) => n.online).length)

const filtered = computed(() => {
  const k = kw.value.toLowerCase()
  if (!k) return nodes.value
  return nodes.value.filter((n) =>
    String(n.nodeId || '').toLowerCase().includes(k) || String(n.label || '').toLowerCase().includes(k))
})

// poolsOf 解析存储的 pool 字段（"api,location" → ['api','location']）；空值按 api 处理
function poolsOf(raw) {
  const list = String(raw || '').split(',').map((s) => s.trim()).filter(Boolean)
  return list.length ? list : ['api']
}
const poolText = (p) => (p === 'location' ? '定位' : '拨测')
// 协议栈只对拨测池有意义（定位池是纯数组，栈分组留空）
const stackText = (n) => (poolsOf(n.pools).includes('api') ? (n.stack || 'DualStack') : '—')

// ==================== 支持的接口类型（中间件转发白名单，与 main.go 的 switch 一致） ====================
// target 写的是「目标段」示例；desc 只讲调用方关心的输入输出，不提实现细节。
const apiTypes = [
  {
    slug: 'detail', pool: 'api', name: '网站检查', target: 'example.com',
    desc: '一次拿到 HTTP / HTTPS 状态码，以及解析、连接、TLS、首字节各阶段耗时（IPv4 与 IPv6 各一组）。',
  },
  {
    slug: 'ssl', pool: 'api', name: 'SSL 证书', target: 'example.com',
    desc: '证书剩余有效天数、起止时间、颁发者，以及协商到的协议版本。',
  },
  {
    slug: 'tcping', pool: 'api', name: 'TCP 端口连通', target: '223.5.5.5?port=443&count=4',
    desc: '对目标端口做多次握手，给出成功数、丢包率与最大 / 最小 / 平均 RTT。目标可以是 IP 或域名；端口与次数走 query：端口默认 80，次数 1–20 默认 4。',
  },
  {
    slug: 'speed', pool: 'api', name: '下载测速', target: 'v4/example.com',
    desc: '网页下载测速。目标的第一段必须标明 v4 或 v6，与节点的协议栈匹配。',
  },
  {
    slug: 'dns', pool: 'api', name: 'DNS 解析', target: 'a/example.com',
    desc: '返回解析记录值与 TTL。记录类型支持 a、aaaa、cname、mx、ns、txt、srv、caa、ptr。',
  },
  {
    slug: 'dnssec', pool: 'api', name: 'DNSSEC 校验', target: 'example.com',
    desc: '是否启用 DNSSEC、签名链是否有效，以及算法与 key tag。',
  },
  {
    slug: 'whois', pool: 'api', name: 'WHOIS 查询', target: 'qq.com',
    desc: '域名注册商、状态列表、注册与到期时间、注册人信息（具体字段随注册局返回而异）。',
  },
  {
    slug: 'location', pool: 'location', name: 'IP 归属地', target: '1.1.1.1',
    desc: '多个 IP 库各自给出一组国家 / 省市 / 运营商与经纬度。注意「查自己」没有目标段，不能走转发，只能直连节点。',
  },
  {
    slug: 'asn', pool: 'location', name: 'ASN 查询', target: '1.1.1.1',
    desc: '多个库给出的 ASN 编号与所属机构。',
  },
]

const typeGroups = computed(() => [
  { pool: 'api', hint: '连通性 / 证书 / 解析类', items: apiTypes.filter((t) => t.pool === 'api') },
  { pool: 'location', hint: 'IP 归属与 ASN', items: apiTypes.filter((t) => t.pool === 'location') },
])

const prefix = (n) => `${API_BASE}/v1/${n.nodeId}/`

// 每种接口类型给一条可直接执行的实例：优先取该池里在线的节点（离线节点调用会 502）
function pickNode(pool) {
  const list = nodes.value.filter((n) => poolsOf(n.pools).includes(pool))
  return list.find((n) => n.online) || list[0] || null
}
const sampleUrl = (t) => {
  const n = pickNode(t.pool)
  return `${API_BASE}/v1/${n ? n.nodeId : '<节点标识>'}/${t.slug}/${t.target}`
}

async function copy(text, key) {
  try {
    await navigator.clipboard.writeText(text)
  } catch {
    // 非安全上下文回退：临时 textarea 选中复制
    const ta = document.createElement('textarea')
    ta.value = text
    ta.style.position = 'fixed'
    ta.style.opacity = '0'
    document.body.appendChild(ta)
    ta.select()
    document.execCommand('copy')
    document.body.removeChild(ta)
  }
  copied.value = key
  setTimeout(() => { if (copied.value === key) copied.value = '' }, 1600)
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    nodes.value = await fetchNodesBrief()
  } catch (e) {
    error.value = e?.message || '加载失败'
  } finally {
    loading.value = false
  }
}

onMounted(load)
</script>

<style scoped>
.pool-tags { display: flex; gap: 4px; flex-wrap: wrap; }
.ok-text { color: var(--ak-signal-success); }
.callout { margin-bottom: 16px; }
.callout-p { font-size: .82rem; line-height: 1.6; margin: 8px 0 0; }
.callout-hint { font-size: .75rem; line-height: 1.5; margin: 10px 0 0; }
.hint { font-size: .75rem; line-height: 1.5; margin: 12px 0 0; }
/* 窄屏：让表格在 .ak-table-wrap 里横向滚动，而不是把每列压到逐字换行（地区名 / 归属池标签尤甚） */
.ak-table { min-width: 720px; }
.ak-table td:nth-child(2) { white-space: nowrap; }
.ak-table .ak-tag { white-space: nowrap; }

/* 接口类型总览：每种类型独占一行，行内给出可直接执行的完整实例 */
.type-title { font-size: .95rem; margin: 20px 0 0; }
.type-group { margin-top: 14px; }
.type-group-title {
  display: flex; align-items: center; gap: 8px; flex-wrap: wrap;
  font-size: .85rem; font-weight: 600; margin: 0 0 8px;
}
.type-list {
  list-style: none; margin: 0; padding: 0;
  display: flex; flex-direction: column; gap: 8px;
}
.type-item { padding: 10px 12px; background: var(--ui-tint); border-radius: 5px; }
.type-head { display: flex; align-items: baseline; gap: 8px; flex-wrap: wrap; }
.type-slug { font-size: .78rem; font-weight: 600; }
.type-name { font-size: .85rem; font-weight: 600; }
/* 目标里可能带很长的域名或 query，允许在任意位置断行，避免撑破卡片 */
.type-target { font-size: .74rem; overflow-wrap: anywhere; }
/* 实例地址：在条目底色上再压一层更深的代码底，两者区分得开 */
.type-ex {
  display: flex; align-items: center; gap: 8px;
  margin-top: 8px; padding: 6px 10px;
  background: var(--ui-code-bg); border-radius: 4px;
}
.type-url { flex: 1 1 auto; min-width: 0; font-size: .76rem; overflow-wrap: anywhere; }
.type-desc { font-size: .74rem; line-height: 1.5; margin: 6px 0 0; }
</style>
