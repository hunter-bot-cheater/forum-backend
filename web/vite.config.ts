import { defineConfig, loadEnv } from 'vite'
import vue from '@vitejs/plugin-vue'
import { fileURLToPath, URL } from 'node:url'

// allowedHosts 只管 Vite 自己的 dev / preview 服务。
// 用 Nginx 托管 dist 时不经过 Vite，域名由 Nginx 的 server_name 处理。
export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), '')
  const allowedHosts: string[] = []
  const parts = (env.ALLOWED_HOSTS || '').split(',')
  for (const part of parts) {
    const host = part.trim()
    if (host) {
      allowedHosts.push(host)
    }
  }

  return {
    plugins: [vue()],
    resolve: {
      alias: {
        '@': fileURLToPath(new URL('./src', import.meta.url)),
      },
    },
    server: {
      port: 5173,
      // 默认只绑 ::1，穿透客户端连 127.0.0.1 会失败，所以监听全部地址。
      host: true,
      allowedHosts,
      // WebSocket 握手也走 /api 前缀，代理必须开 ws。
      proxy: {
        '/api': {
          target: 'http://127.0.0.1:8080',
          changeOrigin: true,
          ws: true,
        },
        // 上传的图片存在后端进程的 uploads/ 目录里，由后端直接吐。
        // 不代理这一条，开发时头像会 404：Vite 只认 src 下的资源，
        // 服务端文件得转给后端。生产由 Nginx 读同一个目录，不走这里。
        '/uploads': {
          target: 'http://127.0.0.1:8080',
          changeOrigin: true,
        },
      },
    },
    // preview 会继承 server 的 proxy 与 allowedHosts，端口不继承。
    preview: {
      port: 5173,
    },
  }
})
