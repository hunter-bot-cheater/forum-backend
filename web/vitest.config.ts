// 单元测试配置。单独一份而不是塞进 vite.config.ts：
// 那份配置里带 dev server 端口与代理，测试完全用不到，
// 混在一起会让人以为改了端口会影响测试。
import { defineConfig } from 'vitest/config'
import vue from '@vitejs/plugin-vue'
import { fileURLToPath, URL } from 'node:url'

export default defineConfig({
  plugins: [vue()],
  resolve: {
    // 与 vite.config.ts 保持一致，否则测试里 import '@/...' 解析不到。
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  test: {
    // jsdom 提供 Date、localStorage 这些浏览器 API，
    // 纯函数测试其实不需要它，但组件测试早晚要有，先统一成一套环境。
    environment: 'jsdom',
    include: ['src/**/*.spec.ts'],
    // 用进程池而不是默认的 worker_threads 池：本机跑默认池时
    // 子线程会被外部环境直接杀掉，报「Worker exited unexpectedly」，
    // 与测试代码无关。fork 出来的子进程同样隔离，速度差异在本项目的用例量下看不出来。
    pool: 'forks',
    // 只开一个子进程：并行 fork 多个同样会被杀掉。
    // 用例总量只有十几个，串行跑不到一秒，没有并行的必要。
    poolOptions: {
      forks: {
        singleFork: true,
      },
    },
  },
})
