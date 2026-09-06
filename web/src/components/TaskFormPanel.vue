<template>
  <section class="panel" style="margin-bottom:16px">
    <h2 class="panel-title">{{ form.id ? '编辑任务' : '新建任务' }} <span class="hl">/ probe task</span></h2>
    <div class="form-row" style="margin-bottom:10px">
      <div class="form-field">
        <label>任务名</label>
        <input class="ak-input" v-model.trim="form.name" placeholder="如 ssl-zakoflare" style="width:180px" />
      </div>
      <div class="form-field">
        <label>拨测类型</label>
        <select class="ak-select" v-model="form.apiType" style="width:160px">
          <option v-for="t in apiOptions" :key="t.value" :value="t.value">{{ t.label }}</option>
        </select>
      </div>
      <div class="form-field" style="flex:1;min-width:220px">
        <label>拨测目标</label>
        <input class="ak-input" v-model.trim="form.target" :placeholder="targetHint" />
      </div>
      <div class="form-field" v-if="form.apiType === 'speed'">
        <label>栈</label>
        <!-- 宽度 130 让"默认 v4 / v4 / v6"完整显示 -->
        <select class="ak-select" v-model="form.stack" style="width:130px">
          <option value="">默认 v4</option>
          <option value="v4">v4</option>
          <option value="v6">v6</option>
        </select>
      </div>
      <div class="form-field">
        <label>间隔 (秒)</label>
        <input class="ak-input" type="number" v-model.number="form.intervalSec" style="width:90px" />
      </div>
      <div class="form-field">
        <label>慢阈值 (ms)</label>
        <input class="ak-input" type="number" v-model.number="form.slowMs" placeholder="0=不判慢" style="width:110px" />
      </div>
    </div>

    <div class="form-row">
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
      <div class="form-field" v-if="form.nodeScope === 'custom'" style="flex:1;min-width:200px">
        <label>节点 id（逗号分隔）</label>
        <input class="ak-input" v-model.trim="form.nodeIds" placeholder="test-node-1,test-http" />
      </div>
      <label class="chk" v-if="form.apiType === 'detail'">
        <input type="checkbox" v-model="form.bothProtocols" /> http 与 https 都命中才算成功
      </label>
      <label class="chk" v-if="form.apiType === 'detail' || form.apiType === 'ssl'">
        <input type="checkbox" v-model="form.requireAllStacks" /> 双栈全通才算可用
      </label>
      <label class="chk" v-if="form.apiType === 'ssl'">
        <input type="checkbox" v-model="form.certExpiredDown" /> 证书过期视为不可用
      </label>
      <span style="flex:1"></span>
      <button class="ak-button ak-button--outline" @click="emit('cancel')">取消</button>
      <button class="ak-button ak-button--action" @click="emit('submit')" :disabled="saving">保存</button>
    </div>
    <div v-if="msg" :class="err ? 'err' : 'ok-200'" style="margin-top:8px;font-size:.8rem">{{ msg }}</div>
  </section>
</template>

<script setup>
import { apiOptions } from '../utils/probeMeta.js'

defineProps({
  // 父组件持有的响应式 form 对象；子组件直接改字段，省去逐字段 emit
  form: { type: Object, required: true },
  saving: { type: Boolean, default: false },
  msg: { type: String, default: '' },
  err: { type: Boolean, default: false },
  targetHint: { type: String, default: '' },
})
const emit = defineEmits(['submit', 'cancel'])
</script>