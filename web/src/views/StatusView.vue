<template>
  <div class="status-page">
    <div class="page">
      <div class="page-head">
        <div><h1>服务状态</h1><div class="sub">最近 {{ hours < 24 ? `${hours} 小时` : `${hours / 24} 天` }} · 每分钟自动刷新</div></div>
        <div class="seg">
          <button v-for="h in [24, 72, 168]" :key="h" :class="{ on: hours === h }" @click="hours = h">{{ h / 24 }}d</button>
        </div>
      </div>
      <div v-if="err" class="empty">{{ err }}</div>
      <div v-for="t in data?.tasks || []" :key="t.taskId" class="card">
        <div class="row between">
          <div><b>{{ t.name }}</b> <span class="pill mono">{{ t.apiType }}</span> <span v-if="t.target" class="muted mono">{{ t.target }}</span></div>
          <b :class="t.availability >= 99 ? 'ok' : 'bad'" style="font-size: 18px">{{ t.up + t.down ? fmtPct(t.availability) : '—' }}</b>
        </div>
        <div class="bars">
          <span v-for="(p, i) in bars(t.series)" :key="i" class="bar" :class="p.cls" :title="p.title" />
        </div>
        <div class="row between faint"><span>平均 {{ fmtMs(t.avgMs) }} · P95 {{ fmtMs(t.p95Ms) }}</span><span>{{ t.byNode.length }} 个节点</span></div>
      </div>
      <div class="faint" style="text-align: center; margin-top: 24px">Powered by 1IPW-BOCE</div>
    </div>
  </div>
</template>

<script setup>
import { onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import * as api from '../api/boce.js'
import { fmtMs, fmtPct, fmtTime } from '../utils/format.js'

const route = useRoute()
const hours = ref(24)
const data = ref(null)
const err = ref('')

// 把曲线点压成 60 根状态条
function bars(series = []) {
  const n = 60
  if (!series.length) return Array.from({ length: n }, () => ({ cls: 'none', title: '无数据' }))
  const per = Math.ceil(series.length / n)
  const out = []
  for (let i = 0; i < series.length; i += per) {
    const g = series.slice(i, i + per)
    const up = g.reduce((s, p) => s + p.up, 0)
    const down = g.reduce((s, p) => s + p.down, 0)
    const cls = up + down === 0 ? 'none' : down === 0 ? 'up' : up === 0 ? 'down' : 'part'
    out.push({ cls, title: `${fmtTime(g[0].time)}  up ${up} / down ${down}` })
  }
  return out
}

async function load() {
  try {
    data.value = await api.fetchPublicStatus(route.params.token, hours.value)
    err.value = ''
  } catch (e) {
    err.value = e.status === 404 ? '分享链接无效或已关闭' : e.message
  }
}
let timer
watch(hours, load)
onMounted(() => {
  load()
  timer = setInterval(load, 60000)
})
onUnmounted(() => clearInterval(timer))
</script>

<style scoped>
.status-page { min-height: 100%; overflow-y: auto; }
.bars { display: flex; gap: 2px; margin: 12px 0 8px; height: 30px; }
.bar { flex: 1; border-radius: 3px; background: var(--bg-hover); }
.bar.up { background: var(--text); }
.bar.down { background: var(--danger); }
.bar.part { background: var(--warn); }
</style>
