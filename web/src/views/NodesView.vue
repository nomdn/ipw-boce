<template>
  <div>
    <div class="toolbar">
      <button class="ak-button ak-button--outline" @click="load">刷新节点</button>
      <span v-if="error" class="err">{{ error }}</span>
      <span class="dim" style="font-size:.85rem">在线 {{ onlineCount }} / {{ nodes.length }}</span>
    </div>
    <div v-if="loading" class="loading-center"><span class="ak-loading"></span></div>

    <div class="node-wall">
      <div v-for="n in nodes" :key="n.nodeId" class="node-card" :class="{ online: n.online }">
        <div class="node-head" style="display:flex;justify-content:space-between;align-items:center">
          <span class="node-id">
            <span class="dot" :class="n.online ? 'online' : 'offline'"></span>{{ n.nodeId }}
          </span>
          <span class="ak-tag ch" :class="n.online ? 'ak-tag--advanced' : 'ak-tag--neutral'">
            {{ n.online ? '在线' : '离线' }}
          </span>
        </div>
        <div class="node-label">{{ n.label || '（未命名）' }}</div>
        <div class="node-meta">
          <span>远端 {{ n.remoteAddr || '—' }}</span>
        </div>
        <div class="node-meta">
          <span>最后活跃 {{ timeAgo(n.lastSeenAt) }}</span>
          <span>首见 {{ timeAgo(n.firstSeenAt) }}</span>
        </div>
        <div class="node-actions">
          <button class="ak-button ak-button--outline" @click="openEvents(n)">事件历史</button>
        </div>
      </div>
      <div v-if="!nodes.length && !loading" class="panel" style="grid-column:1/-1">
        <h2 class="panel-title">暂无节点</h2>
        <p class="dim" style="margin:0">节点经 WS 通道注册或上报时才会出现在列表。</p>
      </div>
    </div>

    <!-- 事件抽屉 -->
    <div v-if="drawer" class="ev-mask" @click.self="drawer = null">
      <div class="ev-panel">
        <header class="ev-head">
          <span class="mono">{{ drawer.nodeId }}</span>
          <span class="dim">在线/离线事件</span>
          <button class="ak-button ak-button--outline" style="margin-left:auto;padding:3px 10px" @click="drawer=null">关闭</button>
        </header>
        <div class="ak-table-wrap" style="max-height:60vh;overflow:auto">
          <table class="ak-table">
            <thead><tr><th>时间</th><th>事件</th><th>原因</th></tr></thead>
            <tbody>
              <tr v-for="e in events" :key="e.id">
                <td class="mono nowrap">{{ fmtTime(e.createdAt) }}</td>
                <td><span class="dot" :class="e.event === 'online' ? 'online' : 'offline'"></span>
                  <span :class="e.event === 'online' ? 'ok-200' : 'err'">{{ e.event }}</span></td>
                <td class="dim" style="word-break:break-all">{{ e.reason || '—' }}</td>
              </tr>
              <tr v-if="!events.length"><td colspan="3" class="dim">暂无事件</td></tr>
            </tbody>
          </table>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, onMounted } from 'vue'
import { fetchNodes, fetchNodeEvents } from '../api/boce.js'
import { timeAgo, fmtTime } from '../utils/format.js'

const nodes = ref([])
const events = ref([])
const drawer = ref(null)
const loading = ref(false)
const error = ref('')

const onlineCount = computed(() => nodes.value.filter((n) => n.online).length)

onMounted(load)
async function load() {
  loading.value = true
  error.value = ''
  try {
    nodes.value = await fetchNodes()
  } catch (e) {
    error.value = e?.message || 'load failed'
  } finally {
    loading.value = false
  }
}

async function openEvents(n) {
  drawer.value = n
  events.value = []
  try {
    events.value = await fetchNodeEvents(n.nodeId, 100)
  } catch (e) {
    events.value = []
  }
}
</script>

<style scoped>
.ev-mask {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.55);
  z-index: 50;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 20px;
}
.ev-panel {
  width: min(640px, 100%);
  background: var(--ak-surface-inverse);
  border: var(--ak-line-hairline) solid rgba(255, 255, 255, 0.12);
  border-top: 3px solid var(--ak-signal-info);
  padding: 16px 18px;
  max-height: 80vh;
  display: flex;
  flex-direction: column;
}
.ev-head { display: flex; align-items: center; gap: 12px; padding-bottom: 10px; border-bottom: 1px solid rgba(255,255,255,.08); }
</style>
