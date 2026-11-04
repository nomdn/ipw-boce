import { createApp } from 'vue'
import { createPinia } from 'pinia'
import NutUI from '@nutui/nutui'
import '@nutui/nutui/dist/style.css'
import './styles/theme.css'
import App from './App.vue'
import router from './router/index.js'
import { installAuthGuard } from './stores/auth.js'
import { initTheme } from './composables/useTheme.js'

initTheme()
const app = createApp(App)
app.use(createPinia())
app.use(router)
app.use(NutUI)
installAuthGuard(router)
app.mount('#app')
