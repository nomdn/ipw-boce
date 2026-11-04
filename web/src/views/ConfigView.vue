<template>
  <div class="page wide">
    <div class="page-head">
      <div>
        <h1>配置托管</h1>
        <div class="sub">节点把 remote-config-url 指向 <code>{{ origin }}/remote-config/&lt;nodeId&gt;</code>，返回 global 与节点配置逐键合并的结果</div>
      </div>
      <button class="btn primary" @click="open({ nodeId: '', isNew: true })">+ 新建配置</button>
    </div>
    <div class="table-wrap">
      <table class="tbl">
        <thead><tr><th>节点</th><th>大小</th><th>更新时间</th><th /></tr></thead>
        <tbody>
          <tr v-for="c in list" :key="c.nodeId">
            <td class="mono">{{ c.nodeId }}<span v-if="c.nodeId === 'global'" class="pill">全局缺省</span></td>
            <td>{{ c.sizeBytes }} B</td><td class="faint">{{ fmtTime(c.updatedAt) }}</td>
            <td><button class="btn sm" @click="open(c)">编辑</button><button class="btn sm danger" @click="del(c)">删除</button></td>
          </tr>
          <tr v-if="!list.length"><td colspan="4" class="empty">暂无托管配置</td></tr>
        </tbody>
      </table>
    </div>
    <Modal v-model="editOpen" width="min(720px, 94vw)" :title="cur?.isNew ? '新建配置' : '编辑 ' + (cur?.nodeId || '')">
      <div v-if="cur">
        <div class="field" v-if="cur.isNew"><label>节点 ID（global = 全局缺省）</label><input v-model.trim="cur.nodeId" class="inp" /></div>
        <div class="field"><label>JSON（setting.json 同构）</label><textarea v-model="text" class="inp" style="min-height: 320px" /></div>
        <div class="row" style="justify-content: flex-end">
          <button class="btn" @click="editOpen = false">取消</button><button class="btn primary" @click="save">保存</button>
        </div>
      </div>
    </Modal>
  </div>
</template>

<script setup>
import Modal from '../components/Modal.vue'
import { onMounted, ref } from 'vue'
import * as api from '../api/boce.js'
import { fmtTime } from '../utils/format.js'
import { confirm, run, toastErr } from '../utils/toast.js'

const origin = location.origin
const list = ref([])
const editOpen = ref(false)
const cur = ref(null)
const text = ref('')

const load = async () => (list.value = await api.fetchNodeConfigs())
async function open(c) {
  cur.value = { ...c }
  text.value = c.isNew ? '{\n  \n}' : JSON.stringify(await api.fetchNodeConfig(c.nodeId), null, 2)
  editOpen.value = true
}
async function save() {
  let obj
  try {
    obj = JSON.parse(text.value)
  } catch (e) {
    return toastErr(new Error('JSON 格式错误：' + e.message))
  }
  if (!cur.value.nodeId) return toastErr(new Error('请填写节点 ID'))
  await run(() => api.putNodeConfig(cur.value.nodeId, obj), '已保存')
  editOpen.value = false
  load()
}
async function del(c) {
  if (!(await confirm(`删除 ${c.nodeId} 的托管配置？`))) return
  await run(() => api.deleteNodeConfig(c.nodeId), '已删除')
  load()
}
onMounted(load)
</script>

