<template>
  <div class="page">
    <div class="page-head">
      <div>
        <h1>积分中心</h1>
        <div class="sub">签到、兑换码、捐赠节点都能获得{{ cur }}</div>
      </div>
      <router-link to="/shop" class="btn primary"><Icon name="ShoppingBag" :size="15" />去商城</router-link>
    </div>

    <div v-if="me && !me.rules.enabled" class="card soft bad">积分功能暂未开启</div>

    <div class="balance anim-up">
      <div>
        <div class="muted">当前{{ cur }}</div>
        <div class="big"><AnimatedNumber :value="me?.balance ?? 0" /></div>
        <div class="faint">累计获得 {{ me?.totalEarned ?? 0 }} · 累计消耗 {{ me?.totalSpent ?? 0 }}</div>
      </div>
      <div v-if="me?.rules.checkinEnabled" class="checkin">
        <button class="ck-btn" :class="{ done: me.checkedInToday, pop: justChecked }" :disabled="me.checkedInToday || busy" @click="doCheckin">
          <Transition name="swap" mode="out-in">
            <span v-if="me.checkedInToday" key="d" class="row" style="gap: 6px"><Icon name="Check" :size="16" :stroke="2.5" />今日已签到</span>
            <span v-else key="c" class="row" style="gap: 6px"><Icon name="CalendarCheck" :size="16" />签到 +{{ me.nextCheckinPoints }}</span>
          </Transition>
        </button>
        <div class="streak">
          <span v-for="i in 7" :key="i" class="sd" :class="{ on: i <= Math.min(me.streak, 7) }" />
        </div>
        <div class="faint">已连续签到 {{ me.streak }} 天</div>
      </div>
    </div>

    <div class="card">
      <h3><Icon name="Ticket" :size="17" />兑换码</h3>
      <div class="row">
        <input v-model.trim="code" class="inp mono" placeholder="XXXX-XXXX-XXXX-XXXX" style="flex: 1; min-width: 200px" @keydown.enter="doRedeem" />
        <button class="btn primary" :disabled="!code" @click="doRedeem">兑换</button>
      </div>
    </div>

    <div v-if="me?.rules" class="card">
      <h3>获取方式</h3>
      <div class="ways">
        <div v-if="me.rules.checkinEnabled" class="way"><Icon name="CalendarCheck" /><div><b>每日签到</b><div class="faint">+{{ me.rules.checkinBase }}，连续每天再加 {{ me.rules.checkinStreakBonus }}（最多叠加 {{ me.rules.checkinStreakMax }} 天）</div></div></div>
        <div class="way"><Icon name="Ticket" /><div><b>兑换码</b><div class="faint">使用管理员发放的兑换码</div></div></div>
        <router-link to="/donate" class="way link"><Icon name="HandHeart" /><div><b>捐赠节点</b><div class="faint">通过审核有一次性奖励，之后每天在线还有奖励</div></div><Icon name="ChevronRight" :size="16" class="go" /></router-link>
        <div class="way"><Icon name="Gift" /><div><b>活动奖励</b><div class="faint">参与活动，由外部系统发放</div></div></div>
      </div>
      <p v-if="me.rules.probeCost > 0" class="faint" style="margin: 12px 0 0">普通用户每次一键拨测消耗 {{ me.rules.probeCost }}，拨测未执行会自动退还。</p>
    </div>

    <div class="card">
      <h3>{{ cur }}明细</h3>
      <div v-if="!ledger.length" class="empty">暂无记录</div>
      <TransitionGroup name="list" tag="div">
        <div v-for="l in ledger" :key="l.id" class="ledger-row">
          <div class="min0">
            <div class="ellipsis">{{ LEDGER_KIND[l.kind] || l.kind }}<span v-if="l.remark" class="muted"> · {{ l.remark }}</span></div>
            <div class="faint">{{ fmtTime(l.createdAt) }}</div>
          </div>
          <div class="right">
            <div class="mono delta" :class="{ bad: l.delta < 0 }">{{ l.delta > 0 ? '+' : '' }}{{ l.delta }}</div>
            <div class="faint">余额 {{ l.balanceAfter }}</div>
          </div>
        </div>
      </TransitionGroup>
      <div v-if="ledger.length < total" class="row" style="justify-content: center; margin-top: 10px">
        <button class="btn" @click="loadMore">加载更多</button>
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed, onMounted, ref } from 'vue'
import * as api from '../api/boce.js'
import { useAuthStore } from '../stores/auth.js'
import { fmtTime, LEDGER_KIND } from '../utils/format.js'
import { toastErr, toastOk } from '../utils/toast.js'
import Icon from '../components/Icon.vue'
import AnimatedNumber from '../components/AnimatedNumber.vue'

const auth = useAuthStore()
const me = ref(null)
const ledger = ref([])
const total = ref(0)
const code = ref('')
const busy = ref(false)
const justChecked = ref(false)
const cur = computed(() => me.value?.rules.currencyName || '积分')

async function load() {
  me.value = await auth.refreshPoints()
  const d = await api.fetchMyLedger(0)
  ledger.value = d.items
  total.value = d.total
}
async function loadMore() {
  const d = await api.fetchMyLedger(ledger.value.length)
  ledger.value.push(...d.items)
}
async function doCheckin() {
  busy.value = true
  try {
    const r = await api.checkin()
    justChecked.value = true
    setTimeout(() => (justChecked.value = false), 500)
    toastOk(`签到成功 +${r.points}`)
    await load()
  } catch (e) {
    toastErr(e)
  } finally {
    busy.value = false
  }
}
async function doRedeem() {
  if (!code.value) return
  try {
    const r = await api.redeemPointsCode(code.value)
    toastOk(`兑换成功 +${r.points}`)
    code.value = ''
    await load()
  } catch (e) {
    toastErr(e)
  }
}
onMounted(load)
</script>

<style scoped>
.balance {
  display: flex; justify-content: space-between; align-items: center; gap: 20px; flex-wrap: wrap;
  padding: 24px 26px; border-radius: 20px; background: var(--bg-soft); margin-bottom: 14px;
}
.big { font-size: 44px; font-weight: 600; line-height: 1.15; letter-spacing: -0.02em; font-variant-numeric: tabular-nums; margin: 4px 0; }
.checkin { text-align: center; display: flex; flex-direction: column; align-items: center; gap: 8px; }
.ck-btn {
  min-width: 156px; height: 42px; border-radius: 999px; border: none; background: var(--primary); color: var(--primary-fg);
  font: inherit; font-size: 14px; font-weight: 500; cursor: pointer; display: flex; align-items: center; justify-content: center;
  transition: opacity 0.15s, transform 0.12s var(--ease), background-color 0.25s var(--ease), color 0.25s var(--ease);
}
.ck-btn:hover:not(:disabled) { opacity: 0.88; }
.ck-btn:active:not(:disabled) { transform: scale(0.96); }
.ck-btn.done { background: var(--bg); color: var(--text-2); border: 1px solid var(--border); cursor: default; }
.ck-btn.pop { animation: popIn 0.35s var(--ease); }
.streak { display: flex; gap: 5px; }
.sd { width: 18px; height: 4px; border-radius: 2px; background: var(--border-strong); transition: background-color 0.3s var(--ease); }
.sd.on { background: var(--text); }
.ways { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 8px; }
@media (max-width: 640px) { .ways { grid-template-columns: 1fr; } }
.way { display: flex; gap: 12px; align-items: flex-start; padding: 12px 14px; border-radius: 14px; background: var(--bg-soft); color: var(--text); text-decoration: none; line-height: 1.45; }
.way > svg:first-child { color: var(--text-2); margin-top: 2px; }
.way.link { transition: background-color 0.15s; }
.way.link:hover { background: var(--bg-hover); }
.way .go { margin-left: auto; color: var(--text-3); align-self: center; }
.ledger-row { display: flex; justify-content: space-between; gap: 10px; padding: 11px 0; border-bottom: 1px solid var(--border); }
.ledger-row:last-child { border-bottom: none; }
.min0 { min-width: 0; }
.ellipsis { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.right { text-align: right; flex-shrink: 0; }
.delta { font-size: 16px; font-weight: 600; }
.swap-enter-active, .swap-leave-active { transition: opacity 0.15s, transform 0.15s; }
.swap-enter-from { opacity: 0; transform: translateY(4px); }
.swap-leave-to { opacity: 0; transform: translateY(-4px); }
</style>
