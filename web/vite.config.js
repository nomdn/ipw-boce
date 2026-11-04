import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// 开发时把接口代理到 Go 后端（默认 8080）；生产构建产物由 Go 直接托管（web-dir）
const target = process.env.BOCE_API || 'http://127.0.0.1:8080'

export default defineConfig({
  plugins: [vue()],
  server: {
    port: 5173,
    proxy: {
      '/admin': target,
      '/api': target,
      '/remote-config': target,
      '/console': { target, ws: true },
    },
  },
  build: {
    chunkSizeWarningLimit: 1500,
  },
})
