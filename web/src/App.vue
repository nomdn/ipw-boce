<template>
  <DialogProvider>
    <router-view />
  </DialogProvider>
</template>

<script setup>
// 根组件：DialogProvider 提供全局对话框（confirm / 内容弹窗），路由出口在其内
import DialogProvider from './components/DialogProvider.vue'
</script>
