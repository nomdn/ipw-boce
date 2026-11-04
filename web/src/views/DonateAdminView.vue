<template>
  <div class="page wide">
    <div class="page-head">
      <div>
        <h1>捐赠节点审核</h1>
        <div class="sub">通过即加入节点池并发放奖励；奖励额度在「积分管理 → 规则」里按机房 / 家宽分别设置</div>
      </div>
      <div class="row">
        <input v-model="settleDate" type="date" class="inp" style="width: 160px" />
        <button class="btn" @click="settle">结算该日在线奖励</button>
      </div>
    </div>
    <div class="row" style="margin-bottom: 10px">
      <div class="seg">
        <button v-for="s in ['pending', 'approved', 'rejected', 'disabled', '']" :key="s" :class="{ on: f.status === s }" @click="(f.status = s), load()">
          {{ s ? DONATE_STATUS[s][0] : '全部' }}
        </button>
      </div>
      <select v-model="f.netType" class="inp" style="width: 130px" @change="load">
        <option value="">全部类型</option><option value="idc">机房</option><option value="home">家庭宽带</option>
      </select>
    </div>
    <div class="table-wrap">
      <table class="tbl">
        <thead><tr><th>节点</th><th>捐赠者</th><th>类型</th><th>地区 / 运营商</th><th>接入</th><th>在线</th><th>奖励</th><th>状态</th><th>提交时间</th><th /></tr></thead>
        <tbody>
          <tr v-for="d in list" :key="d.id">
            <td><b>{{ d.label }}</b><div class="faint mono">{{ d.nodeId }}</div></td>
            <td>{{ d.username }}<div v-if="d.contact" class="faint">{{ d.contact }}</div></td>
            <td><span class="pill">{{ NET_TYPE[d.netType] }}</span><div class="faint">{{ d.stack }} {{ d.bandwidth }}</div></td>
            <td>{{ d.region }}<div class="faint">{{ d.isp }}</div></td>
            <td>{{ d.channel.toUpperCase() }}<div v-if="d.url" class="faint">{{ d.url }}</div></td>
            <td><span :class="d.online ? 'ok' : 'faint'">{{ d.online ? '在线' : '离线' }}</span><div v-if="d.version" class="faint">v{{ d.version }}</div></td>
            <td class="mono">{{ d.bonusPaid }} + {{ d.dailyEarned }}<div class="faint">{{ d.rewardDays }} 天</div></td>
            <td><span class="pill" :class="DONATE_STATUS[d.status]?.[1]">{{ DONATE_STATUS[d.status]?.[0] }}</span>
              <div v-if="d.reviewNote" class="faint" style="white-space: normal; max-width: 180px">{{ d.reviewNote }}</div></td>
            <td class="faint">{{ fmtTime(d.createdAt) }}</td>
            <td>
              <template v-if="d.status === 'pending' || d.status === 'rejected' || d.status === 'disabled'">
                <button class="btn sm accent" @click="review(d, 'approve')">通过</button>
              </template>
              <button v-if="d.status === 'pending'" class="btn sm danger" @click="review(d, 'reject')">拒绝</button>
              <button v-if="d.status === 'approved'" class="btn sm danger" @click="review(d, 'disable')">停用</button>
            </td>
          </tr>
          <tr v-if="!list.length"><td colspan="10" class="empty">没有记录</td></tr>
        </tbody>
      </table>
    </div>
    <p class="faint" style="margin-top: 10px">提示：待审核节点用分配的 Key 已经可以连上来，「在线」列能看到它是否真的跑起来了，建议确认在线后再通过。</p>

    <Modal v-model="noteOpen" width="min(420px, 92vw)" :title="pending ? ({ approve: '通过', reject: '拒绝', disable: '停用' }[pending.action] + ' · ' + pending.d.label) : ''">
      <div v-if="pending">
        <div class="field"><label>备注 / 原因（用户可见）</label><input v-model="note" class="inp" /></div>
        <div class="row" style="justify-content: flex-end">
          <button class="btn" @click="noteOpen = false">取消</button><button class="btn primary" @click="doReview">确定</button>
        </div>
      </div>
    </Modal>
  </div>
</template>

<script setup>
import Modal from '../components/Modal.vue'
import { onMounted, reactive, ref } from 'vue'
import * as api from '../api/boce.js'
import { DONATE_STATUS, fmtTime, NET_TYPE } from '../utils/format.js'
import { run, toastOk } from '../utils/toast.js'

const f = reactive({ status: 'pending', netType: '' })
const list = ref([])
const noteOpen = ref(false)
const pending = ref(null)
const note = ref('')
const y = new Date(Date.now() - 86400000)
const settleDate = ref(`${y.getFullYear()}-${String(y.getMonth() + 1).padStart(2, '0')}-${String(y.getDate()).padStart(2, '0')}`)

const load = async () => (list.value = await api.fetchAdminDonations(f))
function review(d, action) {
  pending.value = { d, action }
  note.value = ''
  noteOpen.value = true
}
async function doReview() {
  await run(() => api.reviewDonation(pending.value.d.id, pending.value.action, note.value), '已处理')
  noteOpen.value = false
  load()
}
async function settle() {
  const r = await run(() => api.settleDonations(settleDate.value))
  toastOk(`${r.date}：新发放 ${r.rewarded} 个节点（已发过的不会重复发）`)
  load()
}
onMounted(load)
</script>

