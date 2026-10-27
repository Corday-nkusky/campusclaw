/**
 * 清理端到端脚本造出来的演示数据。
 *
 * 为什么需要：`e2e-platform.mjs` 每跑一次就在配置页里建一份模板、一个技能、
 * 一个智能体、一台 MCP 服务器（名字都带时间戳后缀以保证唯一）。脚本原来只在
 * finally 里关掉浏览器，这些行就永久留在库里——跑几次之后教师配置页
 * 满屏「生物助教-953636」，正常内容根本看不见了。
 *
 * 匹配规则：名字形如 `<前缀>-<6 位上下数字>`，且前缀在白名单里。
 * 刻意不用「按创建时间早于某个点全删」之类的规则：那会误伤教师真建的条目。
 * `demo-tools` 这种不带数字后缀的真配置也不会被匹配到。
 *
 * 用法（独立跑一遍清历史残留）：
 *   node scripts/lib/cleanup-demo.mjs            # 只打印将要删的，不动
 *   node scripts/lib/cleanup-demo.mjs --apply    # 真删
 *
 * 被 `e2e-platform.mjs` 在 finally 里调用时直接 apply。
 */

import { fileURLToPath } from 'node:url'
import path from 'node:path'
import { loadEnv } from './env.mjs'

const DEMO_NAME_RE = /^(?:生物助教|上传技能|模板助教|demo)-\d{4,8}$/

export function isDemoName(name) {
  return DEMO_NAME_RE.test(String(name ?? '').trim())
}

/** 登录拿令牌。失败时返回 null，由调用方决定是静默跳过还是报错。 */
async function login(base, user, pass) {
  const res = await fetch(`${base}/api/login`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username: user, password: pass }),
  })
  if (!res.ok) return null
  const body = await res.json()
  return body.access_token ?? null
}

async function req(base, token, method, url) {
  const res = await fetch(`${base}${url}`, {
    method,
    headers: { Authorization: `Bearer ${token}` },
  })
  if (res.status === 204) return { ok: true }
  let body = null
  try {
    body = await res.json()
  } catch {
    body = null
  }
  return { ok: res.ok, status: res.status, body }
}

/**
 * 删除所有符合演示命名的资源。
 *
 * @returns {Promise<{deleted: Array<{kind: string, id: number, name: string}>,
 *                    skipped: Array<{kind: string, id: number, name: string, reason: string}>}>}
 */
export async function cleanupDemoData({
  base = `http://127.0.0.1:${process.env.API_PORT || 18080}`,
  user = 'teacher_a',
  pass = process.env.SEED_TEACHER_PASSWORD,
  apply = true,
} = {}) {
  const deleted = []
  const skipped = []
  if (!pass) throw new Error('缺少口令：SEED_TEACHER_PASSWORD 未设置')

  const token = await login(base, user, pass)
  if (!token) throw new Error(`登录失败：${base} /api/login 拒绝了 ${user}`)

  /**
   * 顺序有讲究：**先删智能体，再删模板与技能**。
   * 智能体引用了模板和技能；反过来的话模板/技能先没，智能体就留下悬空引用，
   * 配置页里会显示「模板已被删除」这种半死状态。
   */
  const targets = [
    { kind: 'agent', list: '/api/agents', key: 'agents', del: (id) => `/api/agents/${id}` },
    { kind: 'template', list: '/api/prompt-templates', key: 'templates', del: (id) => `/api/prompt-templates/${id}` },
    { kind: 'skill', list: '/api/skills', key: 'skills', del: (id) => `/api/skills/${id}` },
    { kind: 'mcp_server', list: '/api/mcp/servers', key: 'servers', del: (id) => `/api/mcp/servers/${id}` },
  ]

  for (const t of targets) {
    const listed = await req(base, token, 'GET', t.list)
    if (!listed.ok || !listed.body) continue
    const rows = listed.body[t.key] ?? listed.body.items ?? []
    for (const row of rows) {
      if (!isDemoName(row.name)) continue
      if (!apply) {
        skipped.push({ kind: t.kind, id: row.id, name: row.name, reason: 'dry-run' })
        continue
      }
      const r = await req(base, token, 'DELETE', t.del(row.id))
      if (r.ok) deleted.push({ kind: t.kind, id: row.id, name: row.name })
      else skipped.push({ kind: t.kind, id: row.id, name: row.name, reason: `${r.status} ${r.body?.error ?? ''}`.trim() })
    }
  }
  return { deleted, skipped }
}

// ── 独立执行 ──────────────────────────────────────────────────

const isMain = Boolean(process.argv[1]) && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)
if (isMain) {
  loadEnv()
  const apply = process.argv.includes('--apply')
  try {
    const { deleted, skipped } = await cleanupDemoData({ apply })
    if (!apply) {
      console.log(`[dry-run] 将删除 ${skipped.length} 条演示数据（加 --apply 才真删）`)
      for (const s of skipped) console.log(`  - ${s.kind} #${s.id} ${s.name}`)
    } else {
      console.log(`已删除 ${deleted.length} 条演示数据`)
      for (const d of deleted) console.log(`  ✓ ${d.kind} #${d.id} ${d.name}`)
      for (const s of skipped) console.log(`  ⚠ 未删 ${s.kind} #${s.id} ${s.name} —— ${s.reason}`)
    }
  } catch (e) {
    console.error(`清理失败：${e.message}`)
    process.exit(1)
  }
}
