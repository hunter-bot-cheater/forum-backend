import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import { fileURLToPath, URL } from 'node:url'

// 前端工程放在 forum-backend/web 下，与 cmd/ internal/ 平级。
// 后端默认监听 127.0.0.1:8080，开发时用 Vite 代理把 /api 转发过去，
// 这样既不用后端开 CORS，也避开了浏览器跨域限制。
// 注意：WebSocket 走 /api/ws/... ，所以 /api 代理要打开 ws: true。
export default defineConfig({
  plugins: [vue()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  server: {
    port: 5173,
    proxy: {
      '/api': {
        target: 'http://127.0.0.1:8080',
        changeOrigin: true,
        ws: true,
      },
    },
  },
})
