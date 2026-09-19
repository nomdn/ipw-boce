<template>
  <section class="panel" style="margin-bottom:16px">
    <h2 class="panel-title">{{ form.id ? '编辑任务' : '新建任务' }}</h2>
    <!-- align-items:flex-start：类型字段的说明行会增加高度，居中对齐会把相邻字段挤下沉 -->
    <div class="form-row" style="margin-bottom:22px;align-items:flex-start">
      <div class="form-field">
        <label>任务名</label>
        <input class="ak-input" v-model.trim="form.name" placeholder="如 ssl-zakoflare" style="width:180px" />
      </div>
      <div class="form-field">
        <label>拨测类型</label>
        <select class="ak-select" v-model="form.apiType" style="width:160px">
          <option v-for="t in apiOptions" :key="t.value" :value="t.value">{{ t.label }}</option>
        </select>
        <!-- 原生下拉弹出层按最长选项撑宽、短选项右侧留白（无法用 CSS 控制），故选项用短名，说明放这里 -->
        <div class="dim type-desc">{{ typeDesc }}</div>
      </div>
      <div class="form-field" style="flex:1;min-width:220px">
        <label>拨测目标</label>
        <input class="ak-input" v-model.trim="form.target" :placeholder="targetHint" />
      </div>
      <div class="form-field" v-if="form.apiType === 'speed'">
        <label>协议栈</label>
        <!-- 宽度 130 让"默认 v4 / v4 / v6"完整显示 -->
        <select class="ak-select" v-model="form.stack" style="width:130px">
          <option value="">默认 v4</option>
          <option value="v4">v4</option>
          <option value="v6">v6</option>
        </select>
      </div>
      <div class="form-field" v-if="form.apiType === 'dns'">
        <label>记录类型</label>
        <select class="ak-select" v-model="form.recordType" style="width:110px">
          <option v-for="r in dnsRecordTypes" :key="r.value" :value="r.value">{{ r.label }}</option>
        </select>
      </div>
      <div class="form-field">
        <label>间隔（秒）</label>
        <input class="ak-input" type="number" v-model.number="form.intervalSec" style="width:90px" />
      </div>
      <div class="form-field">
        <label>慢阈值（毫秒）</label>
        <input class="ak-input" type="number" v-model.number="form.slowMs" placeholder="0 = 不判定" style="width:110px" />
      </div>
    </div>

    <!-- 第二行：同类控件各自成组 —— 判定/范围（输入+下拉+勾选）｜通知与标签（输入）｜开关项（勾选）｜操作（按钮） -->
    <div class="form-row">
      <div class="tb-group">
        <div class="form-field">
          <label>期望状态码</label>
          <input class="ak-input" v-model.trim="form.expectStatus" placeholder="2xx 或 200,301" style="width:130px" />
        </div>
        <!-- 节点范围：宽度 150 让"全部节点 / 指定节点"四字完整显示 -->
        <div class="form-field">
          <label>节点范围</label>
          <select class="ak-select" v-model="form.nodeScope" style="width:150px">
            <option value="all">全部节点</option>
            <option value="custom">指定节点</option>
          </select>
        </div>
        <div class="form-field" v-if="form.nodeScope === 'custom'" style="flex:1;min-width:260px">
          <label>节点（勾选）</label>
          <div class="node-picks">
            <label v-for="n in nodes" :key="n.nodeId" class="node-pick" :title="n.version ? '版本 ' + n.version : '未上报版本'">
              <input type="checkbox" :value="n.nodeId" v-model="picked" />
              <span class="dot" :class="n.online ? 'online' : 'offline'"></span>{{ n.nodeId }}
              <span v-if="n.label" class="dim">{{ n.label }}</span>
            </label>
            <span v-if="!nodes || !nodes.length" class="dim">暂无可用节点</span>
          </div>
        </div>
        <!-- 全选/清空属于上面的节点勾选，跟着它走，不跟「保存」混在一起 -->
        <div class="form-field" v-if="form.nodeScope === 'custom' && nodes && nodes.length" style="min-width:120px">
          <label>&nbsp;</label>
          <div class="node-picks">
            <button type="button" class="ak-button ak-button--outline" style="font-size:.72rem;padding:3px 8px"
              @click="picked = nodes.map(n => n.nodeId)">全选</button>
            <button type="button" class="ak-button ak-button--outline" style="font-size:.72rem;padding:3px 8px"
              @click="picked = []">清空</button>
          </div>
        </div>
        <div class="form-field">
          <label>免打扰时段</label>
          <input class="ak-input" v-model.trim="form.quietHours" placeholder="23:00-07:00，留空不静默" style="width:170px" />
        </div>
        <div class="form-field">
          <label>标签</label>
          <input class="ak-input" v-model.trim="form.tags" placeholder="逗号分隔，如 生产,核心" style="width:180px" />
        </div>
      </div>

      <div class="tb-group tb-group--checks">
        <label class="chk" v-if="form.apiType === 'detail'">
          <input type="checkbox" v-model="form.bothProtocols" /> HTTP 与 HTTPS 均需命中才算成功
        </label>
        <label class="chk" v-if="form.apiType === 'detail' || form.apiType === 'ssl'">
          <input type="checkbox" v-model="form.requireAllStacks" /> IPv4 与 IPv6 均可用才算通过
        </label>
        <label class="chk" v-if="form.apiType === 'ssl'">
          <input type="checkbox" v-model="form.certExpiredDown" /> 证书过期视为不可用
        </label>
        <label class="chk">
          <input type="checkbox" v-model="form.notifyRecover" /> 恢复时也通知
        </label>
        <label class="chk" title="勾选后，公开分享页不显示该任务的拨测目标">
          <input type="checkbox" v-model="form.hideTarget" /> 分享页隐藏拨测目标
        </label>
      </div>

      <span style="flex:1"></span>
      <div class="tb-group tb-group--actions">
        <button class="ak-button ak-button--outline" @click="emit('cancel')">取消</button>
        <button class="ak-button ak-button--action" @click="emit('submit')" :disabled="saving">保存</button>
      </div>
    </div>
    <div v-if="msg" :class="err ? 'err' : 'ok-200'" style="margin-top:8px;font-size:.8rem">{{ msg }}</div>
  </section>
</template>

<script setup>
import { computed } from 'vue'
import { apiOptions, dnsRecordTypes } from '../utils/probeMeta.js'

const props = defineProps({
  // 父组件持有的响应式 form 对象；子组件直接改字段，省去逐字段 emit
  form: { type: Object, required: true },
  saving: { type: Boolean, default: false },
  msg: { type: String, default: '' },
  err: { type: Boolean, default: false },
  targetHint: { type: String, default: '' },
  // 节点简表（父组件拉取）：nodeId/label/online/version，"指定节点"勾选数据源
  nodes: { type: Array, default: () => [] },
})
const emit = defineEmits(['submit', 'cancel'])

// 勾选集与 form.nodeIds（逗号分隔串）互相同步
const picked = computed({
  get: () => String(props.form.nodeIds || '').split(',').map((s) => s.trim()).filter(Boolean),
  set: (arr) => { props.form.nodeIds = [...new Set(arr)].join(',') },
})

// 选中类型的说明文案（原下拉里" · 副标题"的替代，见模板注释）
const typeDesc = computed(() => apiOptions.find((t) => t.value === props.form.apiType)?.desc || '')
</script>

<style scoped>
.type-desc { font-size: .7rem; margin-top: 3px; min-height: 1em; }
/* 操作按钮组：即使换行到下一行也贴着右缘，保存/取消位置稳定不漂 */
.tb-group--actions { margin-left: auto; }
.node-picks {
  display: flex; flex-wrap: wrap; gap: 6px 12px; align-items: center;
  max-width: 560px;
}
.node-pick {
  display: inline-flex; align-items: center; gap: 5px;
  font-size: .8rem; cursor: pointer; white-space: nowrap;
}
.node-pick .dot { width: 7px; height: 7px; border-radius: 50%; display: inline-block; }
.node-pick .dot.online { background: var(--ak-signal-success); }
.node-pick .dot.offline { background: var(--ak-signal-danger); opacity: .55; }
.node-pick .dim { color: var(--ak-text-secondary); font-size: .72rem; }
</style>
