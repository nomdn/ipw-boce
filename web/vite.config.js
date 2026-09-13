import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// ipw-boce 数据可视化控制台
// 不走 dev proxy：前端经 http.js 直接向后端 origin 跨域请求（VITE_API_BASE 指向后端，默认 127.0.0.1:8091）。
export default defineConfig({
  plugins: [vue()],
  server: {
    port: 5173,
    host: '127.0.0.1',
    // 预览面板 / 沙箱环境不支持 WebSocket 升级时，用 VITE_NO_HMR=1 关掉 HMR。
    // 否则客户端会反复重连失败，模块图错乱后报 "injection Symbol(router) not found" 这类假错。
    hmr: process.env.VITE_NO_HMR ? false : undefined,
  },
})
