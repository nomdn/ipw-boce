// ==================== 全局配置：指定后端接口地址 ====================
// 前端不依赖 dev proxy，直接向后端 origin 发起跨域请求（后端已开 CORS，Allow-Headers 含 Authorization）。
// 改后端地址只需改这里 API_BASE（也可用环境变量 VITE_API_BASE 覆盖，例如打包时注入 https://collector.example.com）。
// path 一律写后端真实路由（/admin/...、/report/...），无 /api 前缀。
export const API_BASE = (import.meta.env.VITE_API_BASE || 'http://127.0.0.1:8091').replace(/\/+$/, '')

// ==================== WebSocket 推送基址（与 API_BASE 同源派生） ====================
// 浏览器控制台经 /console/sla 实时接收 SLA 推送（后端在 HTTP 同端口升级）。
// 路径避开 /ws 前缀（节点数据面预留），保证将来节点通道合并进主服务时不冲突。http→ws / https→wss。
export const WS_BASE = API_BASE.replace(/^http/, 'ws')
