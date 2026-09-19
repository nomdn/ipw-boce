import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import router from './router/index.js'
import { installAuthGuard } from './stores/auth.js'
import { syncFromDocument } from './composables/useTheme.js'

// ak-ui 全量组件样式（含 tokens + 组件类）
import '@yunyoujun/ak-ui/style.css'
// 主题层（亮/暗）：暗色基线 + html[data-theme="light"] 覆盖，必须在 ak-ui 之后
import './styles/theme.css'
// 项目级补充样式（布局 / 表格 / echarts 适配 / 品牌微调）
import './styles/app.css'

// 与 index.html 内联脚本对齐（内联脚本负责首屏，这里兜住被禁用 JS 存储的边界）
syncFromDocument()

const app = createApp(App)
app.use(createPinia())
app.use(router)

installAuthGuard(router)

app.mount('#app')
