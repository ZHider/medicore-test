import { createApp } from 'vue'
import { createPinia } from 'pinia'
import ElementPlus from 'element-plus'
import zhCn from 'element-plus/es/locale/lang/zh-cn'
import 'element-plus/dist/index.css'

import App from './App.vue'
import router from './router'
import { useAuthStore } from './stores/auth'
import './style.css'

/**
 * 启动顺序很重要：
 * 先恢复登录态（会用 refresh token 换新令牌并拉一次 /auth/me），
 * 再按角色注册路由，最后才 mount —— 否则首屏会先渲染成 404 或登录页再跳走。
 */
async function bootstrap() {
  const app = createApp(App)

  app.use(createPinia())
  app.use(ElementPlus, { locale: zhCn })

  const auth = useAuthStore()
  await auth.bootstrap()

  app.use(router)
  await router.isReady()
  app.mount('#app')
}

void bootstrap()
