import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// ipw-boce 数据可视化控制台
// 不走 dev proxy：前端经 http.js 直接向后端 origin 跨域请求（VITE_API_BASE 指向后端，默认 127.0.0.1:8091）。
export default defineConfig({
  plugins: [vue()],
  server: {
    port: 5173,
  },
})
