#!/usr/bin/env node
/**
 * 极简 TCP 转发器 —— 只为 §8.4 的验收测试服务。
 *
 * 用途：把后端实例的 DB_PORT 指到本转发器，本转发器再转到真实的 MySQL。
 * 这样**杀掉转发器**就等于「DB 在运行中途掉线」：后端到 DB 的既有连接被断、
 * 新建连接被拒；**重新拉起转发器**就等于 DB 恢复。
 *
 * 为什么绕这一圈：本机 `net stop MySQL80` 需要管理员权限（实测报「系统错误 5
 * 拒绝访问」），而「运行中 DB 掉线再恢复」这条转换路径恰恰是 R8.4 要验的核心。
 * 用转发器可以在不提权的前提下制造出**对被测进程而言完全等价**的连通性中断，
 * 而且比停服务更严格：连池子里的长连接也会被真正掐断。
 *
 * 用法：
 *   node scripts/tcp-proxy.mjs <监听端口> <目标端口> [目标地址]
 *
 * 退出码：0 = 正常；1 = 参数缺失或监听失败。
 */

import net from 'node:net'

const listenPort = Number(process.argv[2])
const targetPort = Number(process.argv[3])
const targetHost = process.argv[4] ?? '127.0.0.1'

if (!listenPort || !targetPort) {
  console.error('用法：node scripts/tcp-proxy.mjs <监听端口> <目标端口> [目标地址]')
  process.exit(1)
}

const server = net.createServer((client) => {
  const upstream = net.connect(targetPort, targetHost)
  client.pipe(upstream)
  upstream.pipe(client)

  const teardown = () => {
    client.destroy()
    upstream.destroy()
  }
  client.on('error', teardown)
  upstream.on('error', teardown)
  client.on('close', () => upstream.destroy())
  upstream.on('close', () => client.destroy())
})

server.on('error', (e) => {
  console.error(`转发器启动失败：${e.message}`)
  process.exit(1)
})

server.listen(listenPort, '127.0.0.1', () => {
  console.log(`tcp-proxy: 127.0.0.1:${listenPort} → ${targetHost}:${targetPort}`)
})
