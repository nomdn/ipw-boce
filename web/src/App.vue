<template>
  <div class="app-root">
    <router-view v-slot="{ Component, route }">
      <Transition name="route" mode="out-in">
        <component :is="Component" :key="route.matched[0]?.path" />
      </Transition>
    </router-view>
    <UiLayer />
    <PopupAd v-if="popup" :popup="popup" />
  </div>
</template>

<script setup>
import { onMounted, ref } from 'vue'
import UiLayer from './components/UiLayer.vue'
import PopupAd from './components/PopupAd.vue'
import * as api from './api/boce.js'

// 弹窗公告：后台「系统设置」里开启并勾选「在 boce 显示」才会有；拉取失败静默
const popup = ref(null)
onMounted(async () => {
  try {
    popup.value = (await api.fetchPublicSite())?.popup || null
  } catch {
    /* 没有弹窗就算了 */
  }
})
</script>
