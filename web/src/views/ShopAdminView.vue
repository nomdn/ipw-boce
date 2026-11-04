<template>
  <div class="page wide">
    <div class="page-head">
      <div>
        <h1>商品与订单</h1>
        <div class="sub">上架商品，选择卡密自动发、发放接口（Webhook）或人工发货</div>
      </div>
      <div class="row">
        <div class="seg">
          <button :class="{ on: tab === 'products' }" @click="tab = 'products'">商品</button>
          <button :class="{ on: tab === 'orders' }" @click="tab = 'orders'">订单</button>
          <button :class="{ on: tab === 'doc' }" @click="tab = 'doc'">发放接口说明</button>
        </div>
        <button v-if="tab === 'products'" class="btn primary" @click="edit()">+ 上架商品</button>
      </div>
    </div>

    <!-- 商品 -->
    <div v-if="tab === 'products'" class="table-wrap">
      <table class="tbl">
        <thead><tr><th>商品</th><th>价格</th><th>发货方式</th><th>可兑</th><th>已售</th><th>上架</th><th /></tr></thead>
        <tbody>
          <tr v-for="p in products" :key="p.id">
            <td><b>{{ p.name }}</b><div class="faint">#{{ p.id }}</div></td>
            <td class="mono">{{ p.price }}</td>
            <td>{{ DELIVERY[p.delivery] }}</td>
            <td>{{ p.available === -1 ? '不限' : p.available }}</td>
            <td>{{ p.sold }}</td>
            <td><Switch :model-value="p.enabled" @change="(v) => quickToggle(p, v)" /></td>
            <td>
              <button class="btn sm" @click="edit(p)">编辑</button>
              <button v-if="p.delivery === 'cdk'" class="btn sm" @click="openCards(p)">卡密</button>
              <button v-if="p.delivery === 'webhook'" class="btn sm" @click="testHook(p)">测试接口</button>
              <button class="btn sm danger" @click="del(p)">删除</button>
            </td>
          </tr>
          <tr v-if="!products.length"><td colspan="7" class="empty">还没有商品</td></tr>
        </tbody>
      </table>
    </div>

    <!-- 订单 -->
    <template v-if="tab === 'orders'">
      <div class="row" style="margin-bottom: 10px">
        <select v-model="of.status" class="inp" style="width: 150px" @change="loadOrders">
          <option value="">全部状态</option>
          <option v-for="(v, k) in ORDER_STATUS" :key="k" :value="k">{{ v[0] }}</option>
        </select>
        <input v-model.trim="of.q" class="inp" placeholder="订单号 / 用户名" style="width: 220px" @keydown.enter="loadOrders" />
        <button class="btn" @click="loadOrders">查询</button>
        <button class="btn" @click="api.download(`/admin/shop/admin/orders/export${api.qs({ status: of.status })}`).catch(toastErr)">导出 CSV</button>
      </div>
      <div class="table-wrap">
        <table class="tbl">
          <thead><tr><th>订单号</th><th>用户</th><th>商品</th><th>积分</th><th>状态</th><th>时间</th><th /></tr></thead>
          <tbody>
            <tr v-for="o in orders" :key="o.id">
              <td class="mono">{{ o.orderNo }}</td>
              <td>{{ o.username }}</td>
              <td>{{ o.productName }} ×{{ o.quantity }}<div v-if="o.userRemark" class="faint">备注：{{ o.userRemark }}</div></td>
              <td class="mono">{{ o.totalPoints }}</td>
              <td><span class="pill" :class="ORDER_STATUS[o.status]?.[1]">{{ ORDER_STATUS[o.status]?.[0] }}</span>
                <div v-if="o.lastError" class="faint bad" style="white-space: normal; max-width: 220px">{{ o.lastError }}</div></td>
              <td class="faint">{{ fmtTime(o.createdAt) }}</td>
              <td>
                <template v-if="o.status === 'pending' || o.status === 'processing'">
                  <button class="btn sm accent" @click="openDeliver(o)">发货</button>
                  <button v-if="o.status === 'processing'" class="btn sm" @click="retry(o)">重推</button>
                  <button class="btn sm danger" @click="cancel(o)">取消退款</button>
                </template>
                <button v-else-if="o.content" class="btn sm" @click="(viewing = o), (viewOpen = true)">查看</button>
              </td>
            </tr>
            <tr v-if="!orders.length"><td colspan="7" class="empty">暂无订单</td></tr>
          </tbody>
        </table>
      </div>
    </template>

    <div v-if="tab === 'doc'" class="card">
      <h3>Webhook 发放接口</h3>
      <p class="muted">商品发货方式选「接口发放」后，用户每次兑换，中心会向你配置的地址发一个签名请求。你的系统收到后完成发货即可。</p>
      <pre class="json">{{ hookDoc }}</pre>
    </div>

    <!-- 商品编辑 -->
    <Modal v-model="editOpen" width="min(620px, 94vw)" :title="form?.id ? '编辑商品' : '上架商品'">
      <div v-if="form">
        <div class="field"><label>名称</label><input v-model="form.name" class="inp" /></div>
        <div class="field"><label>描述</label><textarea v-model="form.description" class="inp" style="font-family: inherit; min-height: 70px" /></div>
        <div class="grid c2">
          <div class="field"><label>价格（积分）</label><input v-model.number="form.price" type="number" min="0" class="inp" /></div>
          <div class="field"><label>图片 URL（选填）</label><input v-model="form.imageUrl" class="inp" /></div>
          <div class="field">
            <label>发货方式</label>
            <select v-model="form.delivery" class="inp" :disabled="!!form.id">
              <option value="cdk">卡密自动发（导入 CDK 库存）</option>
              <option value="webhook">接口发放（Webhook）</option>
              <option value="manual">人工发货</option>
            </select>
          </div>
          <div class="field" v-if="form.delivery !== 'cdk'"><label>库存（-1 = 不限）</label><input v-model.number="form.stock" type="number" min="-1" class="inp" /></div>
          <div class="field"><label>每人限兑（0 = 不限）</label><input v-model.number="form.perUserLimit" type="number" min="0" class="inp" /></div>
          <div class="field"><label>排序（小的在前）</label><input v-model.number="form.sortOrder" type="number" class="inp" /></div>
        </div>
        <template v-if="form.delivery === 'webhook'">
          <div class="field"><label>发放接口 URL</label><input v-model="form.deliveryUrl" class="inp" placeholder="https://your-system/boce/deliver" /></div>
          <div class="field">
            <label>签名密钥 {{ form.hasSecret ? '（已设置，留空不修改）' : '（留空自动生成）' }}</label>
            <div class="row">
              <input v-model="form.deliverySecret" class="inp" style="flex: 1" placeholder="whsec_..." />
              <button v-if="form.id" class="btn sm" @click="resetSecret">重新生成</button>
            </div>
          </div>
        </template>
        <div class="field" v-if="form.delivery === 'cdk' && !form.id">
          <label>卡密（每行一条，可稍后在「卡密」里继续导入）</label>
          <textarea v-model="form.cards" class="inp" />
        </div>
        <div class="row"><Switch v-model="form.enabled" /> 上架</div>
        <div class="row" style="justify-content: flex-end; margin-top: 16px">
          <button class="btn" @click="editOpen = false">取消</button>
          <button class="btn primary" @click="save">保存</button>
        </div>
      </div>
    </Modal>

    <!-- 卡密 -->
    <Modal v-model="cardsOpen" width="min(680px, 94vw)" :title="'卡密库存 · ' + (cardProduct?.name || '')">
      <div v-if="cardProduct">
        <div class="field"><label>批量导入（每行一条）</label><textarea v-model="newCards" class="inp" /></div>
        <button class="btn primary" :disabled="!newCards.trim()" @click="doImport">导入</button>
        <div class="seg" style="margin: 14px 0 8px">
          <button v-for="s in ['unused', 'used', '']" :key="s" :class="{ on: cardState === s }" @click="(cardState = s), loadCards()">
            {{ { unused: '未发出', used: '已发出', '': '全部' }[s] }}
          </button>
        </div>
        <div class="table-wrap">
          <table class="tbl">
            <thead><tr><th>ID</th><th>内容</th><th>订单</th><th /></tr></thead>
            <tbody>
              <tr v-for="c in cards" :key="c.id">
                <td>{{ c.id }}</td><td class="mono wrap">{{ c.content }}</td>
                <td>{{ c.orderId ? `#${c.orderId}` : '—' }}</td>
                <td><button v-if="!c.orderId" class="btn sm danger" @click="delCard(c)">删除</button></td>
              </tr>
              <tr v-if="!cards.length"><td colspan="4" class="empty">无</td></tr>
            </tbody>
          </table>
        </div>
      </div>
    </Modal>

    <!-- 发货 -->
    <Modal v-model="deliverOpen" width="min(520px, 94vw)" :title="'发货 · ' + (deliverTarget?.orderNo || '')">
      <div v-if="deliverTarget">
        <p class="muted">{{ deliverTarget.username }} 兑换 {{ deliverTarget.productName }} ×{{ deliverTarget.quantity }}</p>
        <p v-if="deliverTarget.userRemark" class="muted">用户备注：{{ deliverTarget.userRemark }}</p>
        <div class="field"><label>发货内容（用户可见，如卡密、快递单号）</label><textarea v-model="deliverContent" class="inp" /></div>
        <div class="row" style="justify-content: flex-end">
          <button class="btn" @click="deliverOpen = false">取消</button>
          <button class="btn primary" :disabled="!deliverContent.trim()" @click="doDeliver">确认发货</button>
        </div>
      </div>
    </Modal>

    <Modal v-model="viewOpen" width="min(520px, 94vw)" :title="'发货内容 · ' + (viewing?.orderNo || '')">
      <div v-if="viewing"><pre class="json">{{ viewing.content }}</pre></div>
    </Modal>

    <Modal v-model="secretOpen" width="min(520px, 94vw)" title="签名密钥">
      <div>
        <p class="muted">只显示这一次，请保存到你的发放系统里。</p>
        <pre class="json">{{ shownSecret }}</pre>
        <button class="btn accent" style="margin-top: 10px" @click="copy(shownSecret)">复制</button>
      </div>
    </Modal>
  </div>
</template>

<script setup>
import Modal from '../components/Modal.vue'
import Switch from '../components/Switch.vue'
import { onMounted, reactive, ref, watch } from 'vue'
import * as api from '../api/boce.js'
import { copyText, DELIVERY, fmtTime, ORDER_STATUS } from '../utils/format.js'
import { confirm, run, toastErr, toastOk } from '../utils/toast.js'

const tab = ref('products')
const products = ref([])
const orders = ref([])
const of = reactive({ status: '', q: '' })
const editOpen = ref(false)
const form = ref(null)
const cardsOpen = ref(false)
const cardProduct = ref(null)
const cards = ref([])
const cardState = ref('unused')
const newCards = ref('')
const deliverOpen = ref(false)
const deliverTarget = ref(null)
const deliverContent = ref('')
const viewOpen = ref(false)
const viewing = ref(null)
const secretOpen = ref(false)
const shownSecret = ref('')

const hookDoc = `POST <发放接口 URL>
Content-Type: application/json
X-Boce-Event: order.created
X-Boce-Timestamp: 1730000000
X-Boce-Signature: sha256=<hex(HMAC_SHA256(secret, timestamp + "." + rawBody))>

{
  "event": "order.created",
  "orderNo": "20241101120000A1B2C3D4",
  "productId": 3, "productName": "会员月卡", "quantity": 1, "totalPoints": 500,
  "userId": 12, "username": "alice", "email": "alice@example.com",
  "remark": "用户填写的备注", "createdAt": 1730000000,
  "callback": "/api/v1/shop/orders/20241101120000A1B2C3D4/deliver"
}

你的接口在 15 秒内返回 2xx，响应体三选一：
  {"content": "发给用户的内容"}            → 立即发货完成
  {"status": "pending"}                    → 稍后异步回调
  {"status": "failed", "message": "原因"}  → 失败并自动退还积分
非 2xx / 超时 / 网络错误同样视为失败并退款。

异步回调（管理员身份：admin-token 或管理员 ipt_ Token）：
  POST /api/v1/shop/orders/<orderNo>/deliver   {"content": "..."}   # 重复调用幂等
  POST /api/v1/shop/orders/<orderNo>/fail      {"reason": "..."}    # 自动退款
  GET  /api/v1/shop/orders/<orderNo>

校验签名（Node.js 示例）：
  const mac = crypto.createHmac('sha256', secret).update(ts + '.' + rawBody).digest('hex')
  const ok  = crypto.timingSafeEqual(Buffer.from('sha256=' + mac), Buffer.from(sigHeader))
  // 同时检查 |now - ts| < 300 防重放；按 orderNo 去重

「测试接口」按钮会发送 event=ping，不产生订单。`

async function loadProducts() {
  products.value = await api.fetchAdminProducts()
}
async function loadOrders() {
  orders.value = await api.fetchAdminOrders(of)
}
function edit(p) {
  form.value = p
    ? { ...p, deliverySecret: '' }
    : { name: '', description: '', price: 100, imageUrl: '', delivery: 'cdk', stock: -1, perUserLimit: 0, sortOrder: 0, enabled: true, deliveryUrl: '', deliverySecret: '', cards: '' }
  editOpen.value = true
}
async function save() {
  const f = { ...form.value }
  if (f.id && !f.deliverySecret) delete f.deliverySecret
  try {
    if (f.id) {
      await api.updateProduct(f.id, f)
      toastOk('已保存')
    } else {
      const r = await api.createProduct(f)
      toastOk(r.imported ? `已上架，导入卡密 ${r.imported} 条` : '已上架')
      if (f.delivery === 'webhook' && r.deliverySecret) {
        shownSecret.value = r.deliverySecret
        secretOpen.value = true
      }
    }
    editOpen.value = false
    loadProducts()
  } catch (e) {
    toastErr(e)
  }
}
async function resetSecret() {
  if (!(await confirm('重新生成后旧密钥立即失效，确定？'))) return
  const r = await run(() => api.resetProductSecret(form.value.id))
  shownSecret.value = r.deliverySecret
  secretOpen.value = true
  form.value.hasSecret = true
}
async function quickToggle(p, v) {
  await run(() => api.updateProduct(p.id, { enabled: v }))
  p.enabled = v
}
async function del(p) {
  if (!(await confirm(`删除商品「${p.name}」？已有订单的商品只会下架。`))) return
  const r = await run(() => api.deleteProduct(p.id))
  toastOk(r.deleted ? '已删除' : '已有订单，已改为下架')
  loadProducts()
}
async function testHook(p) {
  try {
    const r = await api.testProductWebhook(p.id)
    toastOk(`接口可达：${JSON.stringify(r.reply)}`)
  } catch (e) {
    toastErr(e)
  }
}
function openCards(p) {
  cardProduct.value = p
  newCards.value = ''
  cardState.value = 'unused'
  cardsOpen.value = true
  loadCards()
}
async function loadCards() {
  cards.value = await api.fetchCards(cardProduct.value.id, cardState.value)
}
async function doImport() {
  const r = await run(() => api.importCards(cardProduct.value.id, newCards.value))
  toastOk(`导入 ${r.imported} 条，可用 ${r.available}`)
  newCards.value = ''
  loadCards()
  loadProducts()
}
async function delCard(c) {
  await run(() => api.deleteCard(c.id), '已删除')
  loadCards()
  loadProducts()
}
function openDeliver(o) {
  deliverTarget.value = o
  deliverContent.value = ''
  deliverOpen.value = true
}
async function doDeliver() {
  await run(() => api.deliverOrder(deliverTarget.value.id, deliverContent.value), '已发货')
  deliverOpen.value = false
  loadOrders()
}
async function cancel(o) {
  if (!(await confirm(`取消订单 ${o.orderNo} 并退还 ${o.totalPoints} 积分？`))) return
  await run(() => api.cancelOrder(o.id, '管理员取消'), '已取消并退款')
  loadOrders()
}
async function retry(o) {
  const r = await run(() => api.retryOrder(o.id))
  toastOk(`当前状态：${ORDER_STATUS[r.status]?.[0]}`)
  loadOrders()
}
async function copy(t) {
  if (await copyText(t)) toastOk('已复制')
}
watch(tab, (t) => t === 'orders' && loadOrders())
onMounted(loadProducts)
</script>

