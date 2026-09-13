import { fileURLToPath, URL } from 'node:url'

import vue from '@vitejs/plugin-vue'
// 用 vitest/config 的 defineConfig 而不是 vite 的：它接受多出来的 `test` 字段。
// 这样测试配置与构建配置共用同一份 alias，不必再维护一个 vitest.config.ts。
import { defineConfig } from 'vitest/config'

// 后端没有任何 CORS 中间件，浏览器直连 127.0.0.1:8080 会被同源策略全部拦掉。
// 这里用 Vite 开发代理把请求变成同源，前端代码里 baseURL 一律写相对路径（/api/v1），
// 该写法在生产环境（nginx 同时托管静态资源并反代 /api）同样成立。
const backend = 'http://127.0.0.1:8080'

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
      '/api': { target: backend, changeOrigin: true },
      // 探针与 Prometheus 指标不在 /api 下，管理看板要用
      '/metrics': { target: backend, changeOrigin: true },
      '/healthz': { target: backend, changeOrigin: true },
      '/readyz': { target: backend, changeOrigin: true },
    },
  },
  test: {
    // 只测纯逻辑（解析器、格式化、错误封装）。
    // 组件测试需要 jsdom + @vue/test-utils，那是另外两个依赖，暂不引入。
    environment: 'node',
    include: ['src/**/*.spec.ts'],
  },
})
