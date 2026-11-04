<template>
  <div class="page wide">
    <div class="page-head">
      <div>
        <h1>积分商城</h1>
        <div class="sub">当前{{ auth.currencyName }} <b>{{ auth.points }}</b></div>
      </div>
      <div class="seg">
        <button :class="{ on: tab === 'goods' }" @click="tab = 'goods'">商品</button>
        <button :class="{ on: tab === 'orders' }" @click="(tab = 'orders'), loadOrders()">我的订单</button>
      </div>
    </div>

    <Transition name="route" mode="out-in">
      <div v-if="tab === 'goods'" key="goods">
        <div v-if="loading" class="goods">
          <div v-for="i in 4" :key="i" class="good skel-card"><div class="cover skeleton" /><div class="g-body"><div class="skeleton" style="height: 16px; width: 60%" /><div class="skeleton" style="height: 12px; width: 90%; margin-top: 10px" /></div></div>
        </div>
        <div v-else-if="!products.length" class="empty"><Icon name="ShoppingBag" :size="28" />暂无上架商品</div>
        <TransitionGroup v-else name="list" tag="div" class="goods">
          <button v-for="(p, i) in products" :key="p.id" class="good" :style="{ animationDelay: i * 40 + 'ms' }" @click="open(p)">
            <div class="cover" :style="p.imageUrl ? { backgroundImage: `url(${p.imageUrl})` } : {}">
              <Icon v-if="!p.imageUrl" name="Gift" :size="34" :stroke="1.4" />
            </div>
            <div class="g-body">
              <div class="g-name ellipsis">{{ p.name }}</div>
              <div class="faint g-desc ellipsis">{{ p.description || DELIVERY[p.delivery] }}</div>
              <div class="row between">
                <span class="price">{{ p.price }}<small> {{ auth.currencyName }}</small></span>
                <span class="faint">{{ p.available === -1 ? '不限量' : p.available > 0 ? `剩 ${p.available}` : '已兑完' }}</span>
              </div>
            </div>
          </button>
        </TransitionGroup>
      </div>

      <div v-else key="orders">
        <div v-if="!orders.length" class="empty"><Icon name="Package" :size="28" />还没有订单</div>
        <TransitionGroup name="list" tag="div">
          <div v-for="o in orders" :key="o.id" class="card">
            <div class="row between">
              <b>{{ o.productName }} × {{ o.quantity }}</b>
              <span class="pill" :class="ORDER_STATUS[o.status]?.[1]">{{ ORDER_STATUS[o.status]?.[0] || o.status }}</span>
            </div>
            <div class="faint">订单号 {{ o.orderNo }} · {{ fmtTime(o.createdAt) }} · {{ o.totalPoints }} {{ auth.currencyName }}</div>
            <div v-if="o.content" class="content">
              <div class="row between"><span class="muted">发货内容</span><button class="btn sm" @click="copy(o.content)"><Icon name="Copy" :size="13" />复制</button></div>
              <pre class="json">{{ o.content }}</pre>
            </div>
            <div v-if="o.lastError && (o.status === 'failed' || o.status === 'canceled')" class="bad" style="font-size: 13px; margin-top: 6px">{{ o.lastError }}</div>
          </div>
        </TransitionGroup>
      </div>
    </Transition>

    <Modal v-model="sheet" :title="cur?.name" width="min(480px, 94vw)">
      <div v-if="cur">
        <p class="muted" style="white-space: pre-wrap; margin-top: 0">{{ cur.description || '暂无描述' }}</p>
        <div class="row between">
          <span class="faint">{{ DELIVERY[cur.delivery] }}<template v-if="cur.perUserLimit"> · 每人限兑 {{ cur.perUserLimit }}</template></span>
          <div class="stepper">
            <button :disabled="qty <= 1" aria-label="减少" @click="qty--">−</button>
            <span>{{ qty }}</span>
            <button :disabled="qty >= maxQty" aria-label="增加" @click="qty++">+</button>
          </div>
        </div>
        <div v-if="cur.delivery !== 'cdk'" class="field" style="margin-top: 14px">
          <label>备注（选填，如收货信息、游戏 ID）</label>
          <input v-model="remark" class="inp" maxlength="200" />
        </div>
      </div>
      <template #footer>
        <div v-if="cur" class="row between" style="width: 100%">
          <div>合计 <span class="price">{{ cur.price * qty }}</span> {{ auth.currencyName }}</div>
          <button class="btn primary" :disabled="busy || cur.available === 0 || auth.points < cur.price * qty" @click="buy">
            <Icon v-if="busy" name="RefreshCw" :size="14" class="spin" />
            {{ cur.available === 0 ? '已兑完' : auth.points < cur.price * qty ? `${auth.currencyName}不足` : '立即兑换' }}
          </button>
        </div>
      </template>
    </Modal>

    <Modal v-model="resultOpen" :title="result?.status === 'delivered' ? '兑换成功' : '已提交'" width="min(440px, 92vw)">
      <div v-if="result">
        <div class="success-mark"><Icon name="Check" :size="22" :stroke="2.5" /></div>
        <p v-if="result.status === 'pending'" class="muted">订单已提交，等待管理员发货，可在「我的订单」查看。</p>
        <p v-if="result.status === 'processing'" class="muted">订单已交给发放系统处理，稍后在「我的订单」查看结果。</p>
        <template v-if="result.content">
          <pre class="json">{{ result.content }}</pre>
          <button class="btn primary" style="margin-top: 12px" @click="copy(result.content)"><Icon name="Copy" :size="14" />复制内容</button>
        </template>
      </div>
    </Modal>
  </div>
</template>

<script setup>
import { computed, onMounted, ref } from 'vue'
import * as api from '../api/boce.js'
import { useAuthStore } from '../stores/auth.js'
import { copyText, DELIVERY, fmtTime, ORDER_STATUS } from '../utils/format.js'
import { toastErr, toastOk } from '../utils/toast.js'
import Icon from '../components/Icon.vue'
import Modal from '../components/Modal.vue'

const auth = useAuthStore()
const tab = ref('goods')
const products = ref([])
const loading = ref(true)
const orders = ref([])
const sheet = ref(false)
const cur = ref(null)
const qty = ref(1)
const remark = ref('')
const busy = ref(false)
const result = ref(null)
const resultOpen = ref(false)

const maxQty = computed(() => {
  if (!cur.value) return 1
  let m = 20
  if (cur.value.available >= 0) m = Math.min(m, cur.value.available)
  if (cur.value.perUserLimit) m = Math.min(m, cur.value.perUserLimit)
  return Math.max(1, m)
})

async function loadProducts() {
  try {
    products.value = await api.fetchProducts()
  } finally {
    loading.value = false
  }
}
async function loadOrders() {
  orders.value = await api.fetchMyOrders()
}
function open(p) {
  cur.value = p
  qty.value = 1
  remark.value = ''
  sheet.value = true
}
async function buy() {
  busy.value = true
  try {
    result.value = await api.placeOrder(cur.value.id, qty.value, remark.value)
    sheet.value = false
    setTimeout(() => (resultOpen.value = true), 180)
    auth.refreshPoints()
    loadProducts()
  } catch (e) {
    toastErr(e)
  } finally {
    busy.value = false
  }
}
async function copy(t) {
  if (await copyText(t)) toastOk('已复制')
}
onMounted(() => {
  loadProducts()
  auth.refreshPoints()
})
</script>

<style scoped>
.goods { display: grid; grid-template-columns: repeat(auto-fill, minmax(220px, 1fr)); gap: 14px; }
.good {
  padding: 0; overflow: hidden; cursor: pointer; border: 1px solid var(--border); border-radius: 18px; background: var(--bg);
  text-align: left; font: inherit; color: inherit; animation: fadeUp 0.35s var(--ease) both;
  transition: border-color 0.2s var(--ease), box-shadow 0.2s var(--ease), transform 0.2s var(--ease);
}
.good:hover { border-color: var(--border-strong); box-shadow: var(--shadow); transform: translateY(-2px); }
.skel-card { cursor: default; animation: none; }
.skel-card:hover { transform: none; box-shadow: none; }
.cover { height: 140px; background: var(--bg-soft) center/cover no-repeat; display: flex; align-items: center; justify-content: center; color: var(--text-3); }
.g-body { padding: 12px 16px 14px; }
.g-name { font-weight: 600; }
.g-desc { min-height: 18px; margin: 2px 0 10px; }
.price { font-weight: 600; font-size: 17px; font-variant-numeric: tabular-nums; }
.price small { font-size: 12px; font-weight: 400; color: var(--text-2); }
.ellipsis { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.content { margin-top: 12px; }
.stepper { display: inline-flex; align-items: center; border: 1px solid var(--border-strong); border-radius: 999px; overflow: hidden; }
.stepper button { width: 34px; height: 30px; border: none; background: transparent; color: var(--text); font-size: 16px; cursor: pointer; transition: background-color 0.15s; }
.stepper button:hover:not(:disabled) { background: var(--bg-hover); }
.stepper button:disabled { color: var(--text-3); cursor: default; }
.stepper span { min-width: 32px; text-align: center; font-variant-numeric: tabular-nums; }
.success-mark { width: 44px; height: 44px; border-radius: 50%; background: var(--primary); color: var(--primary-fg); display: flex; align-items: center; justify-content: center; margin-bottom: 12px; animation: popIn 0.3s var(--ease) both; }
</style>
