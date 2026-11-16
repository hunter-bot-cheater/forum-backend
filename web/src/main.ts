import { createApp } from 'vue'
import { createPinia } from 'pinia'
import ElementPlus from 'element-plus'
import 'element-plus/dist/index.css'
// 对话组件的样式（Conversations / BubbleList / XSender）。
// 这些组件是 vue-element-plus-x 提供的，样式是它自己的一份，不引就只剩裸 DOM。
import 'vue-element-plus-x/styles/index.css'
import * as ElementPlusIconsVue from '@element-plus/icons-vue'

import App from './App.vue'
import router from './router'
import './styles/index.css'

const app = createApp(App)

// vue3-element-admin 的做法：把全部图标全局注册，页面里直接用 <Document/> 等组件名。
for (const [key, component] of Object.entries(ElementPlusIconsVue)) {
  app.component(key, component)
}

app.use(createPinia())
app.use(router)
app.use(ElementPlus)
app.mount('#app')
