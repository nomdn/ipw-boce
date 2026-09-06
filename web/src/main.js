import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import router from './router/index.js'
import { installAuthGuard } from './stores/auth.js'

// ak-ui 全量组件样式（含 tokens + 组件类）
import '@yunyoujun/ak-ui/style.css'
// 项目级补充样式（布局 / 表格 / echarts 适配 / 品牌微调）
import './styles/app.css'

const app = createApp(App)
app.use(createPinia())
app.use(router)

installAuthGuard(router)

app.mount('#app')
