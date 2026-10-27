import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// 开发期：前端 5173，后端跟随 .env 的 API_PORT（默认 18080）。
// 为什么不是 8080：8080 是很多服务的默认端口，本机容易被别的程序先占上；
// 一旦前端代理打到不是本仓后端的进程，表现就是「页面能开、一登录就报账号或口令不正确」。
const API_PORT = process.env.API_PORT || '18080'
//
// 走代理而**不是** CORS：会话 Cookie 是 HttpOnly + SameSite=Lax，
// 让浏览器全程只看到单一来源（5173），跨域与凭证特例就都不存在了，
// 这与生产形态（Nginx 同时托管静态资源并反代 /api、/health）一致。
//
// 注意：不要给后端加 CORS 头来「顺手解决」本地联调 —— 那会把一个
// 开发期的便利固化成生产配置，而生产是同源的（design.md 交付链路）。
export default defineConfig({
  plugins: [react()],
  server: {
    // 显式绑 IPv4 回环。
    // Vite 默认只绑 [::1]（IPv6 回环），那样 `http://127.0.0.1:5173` 直接连不上（实测 000），
    // 而浏览器访问 `localhost` 会在 IPv6 失败后回退到 IPv4 —— 绑 127.0.0.1 时两个地址都能用，
    // 绑 [::1] 时只有 localhost 能用。绑回环地址不是绑 0.0.0.0，不对外暴露。
    host: '127.0.0.1',
    port: 5173,
    strictPort: true,
    proxy: {
      '/api': { target: `http://127.0.0.1:${API_PORT}`, changeOrigin: false },
      '/health': { target: `http://127.0.0.1:${API_PORT}`, changeOrigin: false },
    },
  },
})
