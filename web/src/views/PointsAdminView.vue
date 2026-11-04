<template>
  <div class="page wide">
    <div class="page-head">
      <div>
        <h1>积分管理</h1>
        <div class="sub">规则配置、手动增减、兑换码与全站流水</div>
      </div>
      <div class="seg">
        <button v-for="t in TABS" :key="t.v" :class="{ on: tab === t.v }" @click="tab = t.v">{{ t.label }}</button>
      </div>
    </div>

    <div class="grid c4" style="margin-bottom: 14px" v-if="ov">
      <div class="stat"><div class="k">流通中</div><div class="v">{{ ov.circulating }}</div></div>
      <div class="stat"><div class="k">累计发放</div><div class="v">{{ ov.issued }}</div></div>
      <div class="stat"><div class="k">累计消耗</div><div class="v">{{ ov.spent }}</div></div>
      <div class="stat"><div class="k">今日签到 / 待处理订单</div><div class="v">{{ ov.checkinsToday }} / {{ ov.pendingOrders + ov.processingOrders }}</div></div>
    </div>

    <!-- 规则 -->
    <div v-if="tab === 'rules' && cfg" class="card">
      <h3>积分规则</h3>
      <div class="grid c2">
        <div class="field"><label>总开关</label><Switch v-model="cfg.enabled" /></div>
        <div class="field"><label>货币名称</label><input v-model="cfg.currencyName" class="inp" /></div>
        <div class="field"><label>注册奖励（0 = 不发）</label><input v-model.number="cfg.signupBonus" type="number" min="0" class="inp" /></div>
        <div class="field"><label>普通用户一键拨测消耗（0 = 免费）</label><input v-model.number="cfg.probeCost" type="number" min="0" class="inp" /></div>
      </div>

      <h3 style="margin-top: 18px" class="row">每日签到 <Switch v-model="cfg.checkinEnabled" /> <span class="faint">{{ cfg.checkinEnabled ? '已开启' : '已关闭，用户端隐藏签到入口' }}</span></h3>
      <div class="grid c3" v-if="cfg.checkinEnabled">
        <div class="field"><label>签到基础积分</label><input v-model.number="cfg.checkinBase" type="number" min="0" class="inp" /></div>
        <div class="field"><label>连续签到每天加成</label><input v-model.number="cfg.checkinStreakBonus" type="number" min="0" class="inp" /></div>
        <div class="field"><label>加成封顶天数</label><input v-model.number="cfg.checkinStreakMax" type="number" min="0" class="inp" /></div>
      </div>

      <h3 style="margin-top: 18px" class="row">捐赠节点 <Switch v-model="cfg.donateEnabled" /> <span class="faint">{{ cfg.donateEnabled ? '开放用户捐赠' : '不开放' }}</span></h3>
      <template v-if="cfg.donateEnabled">
        <div class="table-wrap" style="margin-bottom: 12px">
          <table class="tbl">
            <thead><tr><th /><th>审核通过一次性奖励</th><th>每在线一天奖励</th></tr></thead>
            <tbody>
              <tr><td>机房</td><td><input v-model.number="cfg.donateBonusIdc" type="number" min="0" class="inp" /></td><td><input v-model.number="cfg.donateDailyIdc" type="number" min="0" class="inp" /></td></tr>
              <tr><td>家庭宽带</td><td><input v-model.number="cfg.donateBonusHome" type="number" min="0" class="inp" /></td><td><input v-model.number="cfg.donateDailyHome" type="number" min="0" class="inp" /></td></tr>
            </tbody>
          </table>
        </div>
        <div class="grid c2">
          <div class="field"><label>当天在线满多少小时算"在线一天"（1~24）</label><input v-model.number="cfg.donateMinOnlineHours" type="number" min="1" max="24" step="0.5" class="inp" /></div>
          <div class="field"><label>每人最多捐赠节点数（0 = 不限）</label><input v-model.number="cfg.donateMaxPerUser" type="number" min="0" class="inp" /></div>
        </div>
        <p class="faint">每天 00:05 自动结算前一天；维护窗口内的离线按在线计算。</p>
      </template>
      <button class="btn primary" @click="saveCfg">保存规则</button>
    </div>

    <!-- 手动增减 -->
    <div v-if="tab === 'adjust'" class="card">
      <h3>手动增减</h3>
      <div class="grid c3">
        <div class="field"><label>用户名</label><input v-model.trim="adj.username" class="inp" list="acc-list" @input="searchAcc" /></div>
        <div class="field"><label>变动（正数加、负数扣）</label><input v-model.number="adj.delta" type="number" class="inp" /></div>
        <div class="field"><label>备注</label><input v-model="adj.remark" class="inp" /></div>
      </div>
      <datalist id="acc-list"><option v-for="a in accounts" :key="a.userId" :value="a.username">{{ a.balance }}</option></datalist>
      <button class="btn primary" :disabled="!adj.username || !adj.delta" @click="doAdjust">提交</button>

      <h3 style="margin-top: 22px">账户余额</h3>
      <div class="table-wrap">
        <table class="tbl">
          <thead><tr><th>ID</th><th>用户</th><th>邮箱</th><th>角色</th><th>余额</th><th /></tr></thead>
          <tbody>
            <tr v-for="a in accounts" :key="a.userId">
              <td>{{ a.userId }}</td><td>{{ a.username }}</td><td>{{ a.email || '—' }}</td><td>{{ a.role }}</td>
              <td class="mono"><b>{{ a.balance }}</b></td>
              <td><button class="btn sm" @click="(adj.username = a.username), (tab = 'adjust')">调整</button>
                <button class="btn sm" @click="viewLedger(a.userId)">流水</button></td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <!-- 兑换码 -->
    <template v-if="tab === 'codes'">
      <div class="card">
        <h3>生成积分兑换码</h3>
        <div class="grid c3">
          <div class="field"><label>每码积分</label><input v-model.number="gen.points" type="number" min="1" class="inp" /></div>
          <div class="field"><label>生成数量（≤1000）</label><input v-model.number="gen.count" type="number" min="1" max="1000" class="inp" :disabled="!!gen.code" /></div>
          <div class="field"><label>每码可用次数（不同用户）</label><input v-model.number="gen.maxUses" type="number" min="1" class="inp" /></div>
          <div class="field"><label>前缀（选填）</label><input v-model.trim="gen.prefix" class="inp" maxlength="16" /></div>
          <div class="field"><label>自定义码（填写则只生成这一个）</label><input v-model.trim="gen.code" class="inp" /></div>
          <div class="field"><label>过期时间（选填）</label><input v-model="gen.expiresAt" type="datetime-local" class="inp" /></div>
        </div>
        <div class="field"><label>备注</label><input v-model="gen.remark" class="inp" /></div>
        <button class="btn primary" :disabled="!gen.points" @click="doGen">生成</button>
        <div v-if="lastCodes.length" style="margin-top: 14px">
          <div class="row between"><span class="muted">本批 {{ lastCodes.length }} 个</span><button class="btn sm" @click="copyCodes">复制全部</button></div>
          <pre class="json">{{ lastCodes.join('\n') }}</pre>
        </div>
      </div>
      <div class="table-wrap">
        <table class="tbl">
          <thead><tr><th>兑换码</th><th>积分</th><th>已用/次数</th><th>批次</th><th>过期</th><th>状态</th><th /></tr></thead>
          <tbody>
            <tr v-for="c in codes" :key="c.id">
              <td class="mono">{{ c.code }}</td><td>{{ c.points }}</td><td>{{ c.usedCount }}/{{ c.maxUses }}</td>
              <td class="faint">{{ c.batch }}</td><td class="faint">{{ c.expiresAt ? fmtTime(c.expiresAt) : '永久' }}</td>
              <td><Switch :model-value="c.enabled" @change="(v) => toggle(c, v)" /></td>
              <td><button class="btn sm danger" @click="delCode(c)">删除</button></td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>

    <!-- 流水 -->
    <template v-if="tab === 'ledger'">
      <div class="row" style="margin-bottom: 10px">
        <input v-model="lf.userId" class="inp" placeholder="用户 ID" style="width: 120px" />
        <select v-model="lf.kind" class="inp" style="width: 160px">
          <option value="">全部类型</option>
          <option v-for="(v, k) in LEDGER_KIND" :key="k" :value="k">{{ v }}</option>
        </select>
        <button class="btn" @click="loadLedger">查询</button>
        <button class="btn" @click="api.download(`/admin/points/admin/ledger/export${api.qs(lf)}`).catch(toastErr)">导出 CSV</button>
      </div>
      <div class="table-wrap">
        <table class="tbl">
          <thead><tr><th>时间</th><th>用户 ID</th><th>类型</th><th>变动</th><th>余额</th><th>备注</th><th>操作人</th></tr></thead>
          <tbody>
            <tr v-for="l in ledger" :key="l.id">
              <td class="faint">{{ fmtTime(l.createdAt) }}</td><td>{{ l.userId }}</td><td>{{ LEDGER_KIND[l.kind] || l.kind }}</td>
              <td class="mono" :class="l.delta > 0 ? 'ok' : 'bad'">{{ l.delta > 0 ? '+' : '' }}{{ l.delta }}</td>
              <td class="mono">{{ l.balanceAfter }}</td><td class="wrap">{{ l.remark }}</td><td class="faint">{{ l.operator }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>

    <!-- 开放接口 -->
    <div v-if="tab === 'api'" class="card">
      <h3>积分发放接口</h3>
      <p class="muted">外部系统使用 <b>管理员身份</b> 调用：静态 <code>admin-token</code>，或管理员账号在「个人资料」生成的 <code>ipt_</code> 个人 Token。</p>
      <pre class="json">{{ apiDoc }}</pre>
    </div>
  </div>
</template>

<script setup>
import Switch from '../components/Switch.vue'
import { onMounted, reactive, ref, watch } from 'vue'
import * as api from '../api/boce.js'
import { copyText, fmtTime, LEDGER_KIND } from '../utils/format.js'
import { confirm, run, toastErr, toastOk } from '../utils/toast.js'

const TABS = [
  { v: 'rules', label: '规则' },
  { v: 'adjust', label: '账户' },
  { v: 'codes', label: '兑换码' },
  { v: 'ledger', label: '流水' },
  { v: 'api', label: '发放接口' },
]
const tab = ref('rules')
const ov = ref(null)
const cfg = ref(null)
const accounts = ref([])
const codes = ref([])
const ledger = ref([])
const lastCodes = ref([])
const adj = reactive({ username: '', delta: null, remark: '' })
const gen = reactive({ points: 100, count: 10, maxUses: 1, prefix: '', code: '', expiresAt: '', remark: '' })
const lf = reactive({ userId: '', kind: '' })

const origin = location.origin
const apiDoc = `# 发放积分（Idempotency-Key 可选：同一个键重复调用只入账一次，返回 replayed=true）
curl -X POST ${origin}/api/v1/points/grant \\
  -H "Authorization: Bearer <admin-token 或 ipt_...>" \\
  -H "Content-Type: application/json" \\
  -H "Idempotency-Key: activity-2024-0001" \\
  -d '{"username":"alice","points":100,"remark":"活动奖励"}'
# 用户可用 userId / username / email 任一字段定位

# 扣减积分（余额不足返回 402 {"error":{"code":"insufficient_points"}}）
POST /api/v1/points/deduct   {"userId":1,"points":20,"remark":"..."}

# 查询余额 / 流水
GET  /api/v1/points/balance?username=alice
GET  /api/v1/points/ledger?userId=1&kind=api_grant&limit=50&offset=0

# 批量生成积分兑换码（CDK）
POST /api/v1/points/codes    {"count":100,"points":50,"maxUses":1,"prefix":"VIP","expiresAt":"2025-12-31T23:59:59+08:00"}
→ {"batch":"...","codes":["VIP-XXXX-XXXX-XXXX-XXXX", ...]}

# 商城 webhook 订单回调（见「商品与订单 → 发放接口说明」）
POST /api/v1/shop/orders/<orderNo>/deliver  {"content":"卡密或发货说明"}
POST /api/v1/shop/orders/<orderNo>/fail     {"reason":"..."}   # 自动退款`

async function loadOv() {
  ov.value = await api.fetchPointsOverview()
}
async function loadAccounts(q = '') {
  accounts.value = await api.fetchAccounts(q)
}
let st
function searchAcc() {
  clearTimeout(st)
  st = setTimeout(() => loadAccounts(adj.username), 250)
}
async function loadLedger() {
  const f = {}
  if (lf.userId) f.userId = lf.userId
  if (lf.kind) f.kind = lf.kind
  ledger.value = (await api.fetchAllLedger(f)).items
}
function viewLedger(uid) {
  lf.userId = String(uid)
  tab.value = 'ledger'
}
async function saveCfg() {
  cfg.value = await run(() => api.savePointsSettings(cfg.value), '已保存')
}
async function doAdjust() {
  await run(() => api.adjustPoints({ username: adj.username, delta: adj.delta, remark: adj.remark }), '已调整')
  adj.delta = null
  adj.remark = ''
  loadAccounts()
  loadOv()
}
async function doGen() {
  const p = { ...gen }
  p.expiresAt = gen.expiresAt ? new Date(gen.expiresAt).toISOString() : ''
  const r = await run(() => api.createCodes(p), '已生成')
  lastCodes.value = r.codes.map((c) => c.code)
  gen.code = ''
  codes.value = await api.fetchCodes()
}
async function copyCodes() {
  if (await copyText(lastCodes.value.join('\n'))) toastOk('已复制')
}
async function toggle(c, v) {
  await run(() => api.toggleCode(c.id, v))
  c.enabled = v
}
async function delCode(c) {
  if (!(await confirm(`删除兑换码 ${c.code}？`))) return
  await run(() => api.deleteCode(c.id), '已删除')
  codes.value = codes.value.filter((x) => x.id !== c.id)
}

watch(tab, async (t) => {
  if (t === 'adjust') loadAccounts()
  if (t === 'codes') codes.value = await api.fetchCodes()
  if (t === 'ledger') loadLedger()
})
onMounted(async () => {
  loadOv()
  cfg.value = await api.fetchPointsSettings()
})
</script>
