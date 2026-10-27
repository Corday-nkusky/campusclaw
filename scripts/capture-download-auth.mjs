#!/usr/bin/env node
/**
 * 「文件下载是否需要权限」取证脚本。
 *
 * ⚠️ **已随 add-token-auth 过期（2026-09-30）**：本脚本按 Cookie 会话方案取证
 * （credentials: 'include' / Cookie 脱敏），token 方案下已授权段的 fetch 会 401。
 * 它支撑的 0923 下载作业是在旧方案下交付的，证据本身仍然有效；
 * 如需在新方案下重取，须先把 fetch 改为带 `Authorization: Bearer` 头
 * （参照 e2e-acceptance.mjs 的 B11 写法）。
 *
 * 产出：docs/screenshots/ 下的真实浏览器截图 + docs/download-auth-report.json。
 *
 * 覆盖场景（前三个在**未登录**状态跑，后四个在登录后跑）：
 *   S1 未授权 · 直连文件接口 /api/materials/{id}/file        → 期望 401
 *   S2 未授权 · 猜静态路径 /uploads/{A班存储名}               → 期望 404
 *   S3 未授权 · 猜静态路径 /uploads/{B班存储名}               → 期望 404
 *   S4 登录（在 API 同源页面内真实 POST /api/login）
 *   S5 已授权 · 本班材料详情 /api/materials/1                 → 期望 200，含正文
 *   S6 已授权 · 本班文件可下载 /api/materials/1/file          → 期望 200 + 附件报头
 *   S7 已授权 · 跨班文件 /api/materials/2/file                → 期望 404
 *   S8 已授权 · 不存在文件 /api/materials/99999/file          → 期望 404（与 S7 同形）
 *
 * 用法：
 *   node scripts/capture-download-auth.mjs --user teacher_a --pass '<口令>'
 *
 * 说明：S1–S3 用**全新 user-data-dir** 的浏览器打开，保证「未登录」是真的未登录，
 *       而不是「我以为没登录」。这点在取证里是关键——否则拿到 200 会分不清是
 *       鉴权失效还是浏览器恰好带着旧会话。
 */

import { spawn } from 'node:child_process'
import { loadEnv } from './lib/env.mjs'
import { existsSync, mkdirSync, mkdtempSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'

// ── 参数 ──────────────────────────────────────────────────────
function parseArgs(argv) {
  const out = {}
  for (let i = 0; i < argv.length; i += 1) {
    const a = argv[i]
    if (a.startsWith('--')) out[a.slice(2)] = argv[i + 1]?.startsWith('--') ? true : argv[++i]
  }
  return out
}

// 口令默认取 .env 的种子口令：命令行参数仍然优先。
loadEnv()
const args = parseArgs(process.argv.slice(2))
const API = args.api ?? `http://127.0.0.1:${process.env.API_PORT || 18080}`
const OUT_DIR = path.resolve(args.out ?? 'docs/screenshots')
const JSON_OUT = path.resolve(args.json ?? 'docs/download-auth-report.json')
const USER = args.user ?? 'teacher_a'
const PASS = args.pass ?? process.env.SEED_TEACHER_PASSWORD
const PORT = Number(args.port ?? 9336)

const CHROME_CANDIDATES = [
  'C:/Program Files/Google/Chrome/Application/chrome.exe',
  'C:/Program Files (x86)/Google/Chrome/Application/chrome.exe',
  'C:/Users/TANGWENJIAN/AppData/Local/Google/Chrome/Application/chrome.exe',
  'C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe',
  'C:/Program Files/Microsoft/Edge/Application/msedge.exe',
]
const chromePath = CHROME_CANDIDATES.find((p) => existsSync(p))

if (!chromePath) {
  console.error('找不到 Chrome / Edge 可执行文件')
  process.exit(1)
}
if (!USER || !PASS) {
  console.error('用法：node scripts/capture-download-auth.mjs --user <账号> --pass <口令>')
  process.exit(1)
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

/** 会话值脱敏：报告可能被提交，绝不落完整会话串。 */
function redactCookie(v) {
  const m = String(v).match(/(cc_session=)([0-9a-f]+)/i)
  if (!m) return String(v)
  return String(v).replace(m[2], `${m[2].slice(0, 8)}…（共 ${m[2].length} 位，已截断）`)
}

// ── 最小 CDP 客户端 ───────────────────────────────────────────
class Cdp {
  constructor(ws) {
    this.ws = ws
    this.seq = 0
    this.pending = new Map()
    this.listeners = new Map()
    ws.addEventListener('message', (ev) => {
      const msg = JSON.parse(ev.data)
      if (msg.id !== undefined) {
        const slot = this.pending.get(msg.id)
        if (!slot) return
        this.pending.delete(msg.id)
        if (msg.error) slot.reject(new Error(`${msg.error.message}（code ${msg.error.code}）`))
        else slot.resolve(msg.result)
        return
      }
      const slots = this.listeners.get(msg.method)
      if (slots) for (const slot of [...slots]) slot.cb(msg.params)
    })
  }

  static async connect(wsUrl) {
    const ws = new WebSocket(wsUrl)
    await new Promise((resolve, reject) => {
      ws.addEventListener('open', resolve, { once: true })
      ws.addEventListener('error', () => reject(new Error(`WebSocket 连接失败：${wsUrl}`)), { once: true })
    })
    return new Cdp(ws)
  }

  send(method, params = {}) {
    const id = ++this.seq
    return new Promise((resolve, reject) => {
      this.pending.set(id, { resolve, reject })
      this.ws.send(JSON.stringify({ id, method, params }))
    })
  }

  on(method, cb) {
    if (!this.listeners.has(method)) this.listeners.set(method, [])
    this.listeners.get(method).push({ cb })
  }
}

async function launchChrome() {
  const profile = mkdtempSync(path.join(tmpdir(), 'cc-dlauth-'))
  const proc = spawn(
    chromePath,
    [
      '--headless=new',
      '--disable-gpu',
      '--no-first-run',
      '--no-default-browser-check',
      '--disable-extensions',
      '--disable-background-networking',
      '--hide-scrollbars',
      `--user-data-dir=${profile}`,
      `--remote-debugging-port=${PORT}`,
      'about:blank',
    ],
    { stdio: ['ignore', 'ignore', 'pipe'] },
  )

  const deadline = Date.now() + 25000
  while (Date.now() < deadline) {
    try {
      const res = await fetch(`http://127.0.0.1:${PORT}/json/version`)
      if (res.ok) {
        const list = await (await fetch(`http://127.0.0.1:${PORT}/json/list`)).json()
        const page = list.find((t) => t.type === 'page')
        if (page?.webSocketDebuggerUrl) return { proc, profile, wsUrl: page.webSocketDebuggerUrl }
      }
    } catch {
      /* 端点未就绪，继续等 */
    }
    await sleep(300)
  }
  proc.kill()
  throw new Error('Chrome CDP 端点未就绪')
}

async function evaluate(cdp, expression) {
  const r = await cdp.send('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true })
  if (r.exceptionDetails) {
    const d = r.exceptionDetails.exception?.description ?? r.exceptionDetails.text
    throw new Error(`页面内执行异常：${d}`)
  }
  return r.result.value
}

async function screenshot(cdp, file) {
  const r = await cdp.send('Page.captureScreenshot', { format: 'png' })
  writeFileSync(file, Buffer.from(r.data, 'base64'))
}

/**
 * 在页面上方叠加一条取证标注。
 *
 * 为什么需要：浏览器对一个 JSON 响应渲染出来只有一行 `{"error":"unauthorized"}`，
 * 截图里既没有 URL 也没有状态码，交出去没法自证「访问的是哪个地址、得到了什么状态」。
 * 标注条里的 URL 与状态码**取自 CDP 记录的真实请求/响应**，不是手写文案；
 * 条下面那一块仍是浏览器对响应的**原始渲染**。
 */
async function annotate(cdp, { title, url, status, note, ts }) {
  await evaluate(
    cdp,
    `(() => {
      const bar = document.createElement('div');
      bar.setAttribute('data-cc-annotation', '1');
      bar.style.cssText = [
        'position:fixed', 'top:0', 'left:0', 'right:0', 'z-index:2147483647',
        'background:#94070a', 'color:#fff', 'padding:11px 18px',
        'font:13px/1.55 -apple-system,"Segoe UI",system-ui,sans-serif',
        'box-shadow:0 2px 10px rgba(0,0,0,.28)',
      ].join(';');
      const line1 = document.createElement('div');
      line1.style.cssText = 'font-size:11px;letter-spacing:.1em;opacity:.88';
      line1.textContent = ${JSON.stringify(title)};
      const line2 = document.createElement('div');
      line2.style.cssText = 'font-family:ui-monospace,Consolas,monospace;font-size:12.5px;margin-top:3px;word-break:break-all';
      line2.textContent = 'GET ' + ${JSON.stringify(url)};
      const line3 = document.createElement('div');
      line3.style.cssText = 'font-size:12px;margin-top:3px';
      const b = document.createElement('b');
      b.textContent = ${JSON.stringify(String(status))};
      line3.textContent = 'HTTP 状态码：';
      line3.appendChild(b);
      line3.appendChild(document.createTextNode('　·　' + ${JSON.stringify(note)} + '　·　' + ${JSON.stringify(ts)}));
      bar.appendChild(line1);
      bar.appendChild(line2);
      bar.appendChild(line3);
      document.documentElement.appendChild(bar);
      document.body.style.paddingTop = '92px';
      return true;
    })()`,
  )
}

// ── 主流程 ────────────────────────────────────────────────────
let chromeProc
let cdp
let exitCode = 0

/** 每轮导航前清空，收集本轮的网络响应。 */
let netLog = []

try {
  mkdirSync(OUT_DIR, { recursive: true })

  const launched = await launchChrome()
  chromeProc = launched.proc
  cdp = await Cdp.connect(launched.wsUrl)

  await cdp.send('Page.enable')
  await cdp.send('Runtime.enable')
  await cdp.send('Network.enable')
  await cdp.send('Emulation.setDeviceMetricsOverride', {
    width: 1040,
    height: 620,
    deviceScaleFactor: 2,
    mobile: false,
  })

  cdp.on('Network.responseReceived', (p) => {
    netLog.push({ url: p.response.url, status: p.response.status, headers: p.response.headers })
  })

  /**
   * 导航到某 URL 并（可选）截图。
   * 状态码取自 CDP 网络事件，而不是页面 DOM —— 页面 DOM 里没有状态码这个东西。
   */
  async function visit({ label, url, shot, note }) {
    netLog = []
    await cdp.send('Page.navigate', { url })
    await sleep(900)
    const rec = netLog.find((r) => r.url.split('#')[0] === url) ?? netLog[netLog.length - 1]
    const status = rec?.status ?? null
    if (shot) {
      await annotate(cdp, { title: label, url, status, note: note ?? phaseNote, ts: stamp() })
      await sleep(250)
      await screenshot(cdp, path.join(OUT_DIR, shot))
    }
    const item = {
      label,
      url,
      status,
      headers: rec?.headers ?? {},
      screenshot: shot ?? null,
    }
    console.log(`  ${String(item.status).padEnd(4)} ${label}  ${url}${shot ? `  → ${shot}` : ''}`)
    return item
  }

  const stamp = () => `采集于 ${new Date().toLocaleString('zh-CN', { hour12: false })}`
  let phaseNote = ''

  const results = { api: API, capturedAt: new Date().toISOString(), scenarios: [] }

  // ═══ 阶段 1：未登录（全新 profile，保证真的没有会话） ═══
  console.log('\n【阶段 1】未登录 —— 应当拿不到任何材料内容')
  phaseNote = '浏览器为全新 profile，未持有任何会话 Cookie'

  results.scenarios.push(
    await visit({
      label: 'S1 未授权 · 直连文件接口',
      url: `${API}/api/materials/1/file`,
      shot: '07-未授权-直连文件接口-401.png',
    }),
  )
  results.scenarios.push(
    await visit({
      label: 'S2 未授权 · 猜静态路径（A 班存储名）',
      url: `${API}/uploads/seed-class-a-convention.md`,
      shot: '08-未授权-猜静态路径-404.png',
    }),
  )
  results.scenarios.push(
    await visit({
      label: 'S3 未授权 · 猜静态路径（B 班存储名）',
      url: `${API}/uploads/seed-class-b-convention.md`,
    }),
  )

  // 记录未登录时浏览器里到底有没有会话 Cookie
  const anonCookies = await cdp.send('Network.getCookies', { urls: [API] })
  results.anonymousCookies = anonCookies.cookies.map((c) => ({
    name: c.name,
    value: redactCookie(`${c.name}=${c.value}`),
    httpOnly: c.httpOnly,
    sameSite: c.sameSite,
  }))
  console.log(`  未登录时浏览器持有的 Cookie 数：${anonCookies.cookies.length}`)

  // ═══ 阶段 2：登录 ═══
  console.log('\n【阶段 2】登录')
  await cdp.send('Page.navigate', { url: `${API}/health` })
  await sleep(500)

  const login = await evaluate(
    cdp,
    `(async () => {
      const r = await fetch(${JSON.stringify(`${API}/api/login`)}, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        credentials: 'include',
        body: JSON.stringify({ username: ${JSON.stringify(USER)}, password: ${JSON.stringify(PASS)} }),
      });
      return { status: r.status, body: await r.text() };
    })()`,
  )
  console.log(`  ${login.status}  POST /api/login  body=${login.body}`)
  results.login = { status: login.status, body: login.body }

  const authCookies = await cdp.send('Network.getCookies', { urls: [API] })
  results.authenticatedCookies = authCookies.cookies.map((c) => ({
    name: c.name,
    value: redactCookie(`${c.name}=${c.value}`),
    httpOnly: c.httpOnly,
    sameSite: c.sameSite,
  }))
  console.log(`  登录后浏览器持有的 Cookie 数：${authCookies.cookies.length}`)

  // ═══ 阶段 3：已登录 ═══
  console.log('\n【阶段 3】已登录 —— 本班可见，跨班仍是 404')
  phaseNote = `已登录 ${USER} · 会话 Cookie 在表头由浏览器自动携带`

  results.scenarios.push(
    await visit({
      label: 'S5 已授权 · 本班材料详情',
      url: `${API}/api/materials/1`,
      shot: '09-已授权-本班材料正文-200.png',
    }),
  )
  results.scenarios.push(
    await visit({
      label: 'S7 已授权 · 跨班文件',
      url: `${API}/api/materials/2/file`,
      shot: '10-已登录-跨班文件-404.png',
    }),
  )
  results.scenarios.push(
    await visit({
      label: 'S8 已授权 · 不存在文件',
      url: `${API}/api/materials/99999/file`,
    }),
  )

  // ═══ 阶段 4：本班文件可下载（fetch 读报头后渲染成图） ═══
  // navigate 到 /file 会触发浏览器下载、页面保持空白，截不出东西；
  // 所以改成「在页面内 fetch 同一 URL，读状态码与响应头，渲染到页面上再截图」。
  console.log('\n【阶段 4】已授权 · 本班文件确实可下载（读响应头）')
  await cdp.send('Page.navigate', { url: `${API}/health` })
  await sleep(500)

  const download = await evaluate(
    cdp,
    `(async () => {
      const url = ${JSON.stringify(`${API}/api/materials/1/file`)};
      const r = await fetch(url, { credentials: 'include' });
      const text = await r.text();
      // 注意：text.length 是**字符数**不是字节数（中文 UTF-8 占 3 字节）。
      // 要跟 Content-Length 报头对得上，必须按编码后算。
      const bytes = new TextEncoder().encode(text).length;
      const pick = (n) => r.headers.get(n) ?? '（无）';
      const rows = [
        ['HTTP 状态码', r.status + ' ' + r.statusText],
        ['Content-Disposition', pick('content-disposition')],
        ['Content-Type', pick('content-type')],
        ['X-Content-Type-Options', pick('x-content-type-options')],
        ['Content-Length 报头', pick('content-length')],
        ['实际收到字节数', bytes + ' 字节（' + text.length + ' 字符）'],
      ];
      const esc = (s) => String(s).replace(/[&<>]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;' })[c]);
      document.title = '已授权下载取证';
      document.body.innerHTML = \`
        <div style="font:14px/1.6 -apple-system,'Segoe UI',system-ui,sans-serif;color:#1c1917;background:#fff;padding:28px 32px;min-height:100vh;box-sizing:border-box">
          <div style="font-size:12px;letter-spacing:.08em;color:#94070a;font-weight:700;margin-bottom:6px">CampusClaw · 文件下载鉴权取证</div>
          <h1 style="font-size:21px;margin:0 0 4px">已授权：本班文件可以被下载</h1>
          <p style="color:#57534e;margin:0 0 20px">对照上一张「未授权 → 401」。这张图里会话 Cookie 由浏览器自动携带，服务端据此放行并返回附件报头。</p>
          <div style="font-family:ui-monospace,Consolas,monospace;font-size:12px;color:#57534e;margin-bottom:14px">GET \${esc(url)}</div>
          <table style="border-collapse:collapse;width:100%;max-width:860px;font-size:13px">
            <tbody>
            \${rows.map(([k, v]) => \`<tr>
              <th style="text-align:left;padding:9px 14px;border:1px solid #e7e5e4;background:#faf9f8;width:210px;font-weight:600">\${esc(k)}</th>
              <td style="padding:9px 14px;border:1px solid #e7e5e4;font-family:ui-monospace,Consolas,monospace;word-break:break-all">\${esc(v)}</td>
            </tr>\`).join('')}
            </tbody>
          </table>
          <div style="margin-top:16px;font-size:12px;color:#57534e;max-width:860px">
            响应体前 60 字：<code style="background:#f5f3f2;padding:2px 6px;border-radius:3px">\${esc(text.slice(0, 60))}</code>
          </div>
        </div>\`;
      return { status: r.status, bytes, chars: text.length, disposition: pick('content-disposition') };
    })()`,
  )

  await sleep(400)
  await screenshot(cdp, path.join(OUT_DIR, '11-已授权-本班文件可下载-200.png'))
  console.log(
    `  ${download.status}  本班文件（fetch）${download.bytes} 字节 / ${download.chars} 字符  Content-Disposition=${download.disposition}`,
  )
  results.scenarios.push({
    label: 'S6 已授权 · 本班文件可下载',
    url: `${API}/api/materials/1/file`,
    status: download.status,
    bytes: download.bytes,
    chars: download.chars,
    contentDisposition: download.disposition,
    screenshot: '11-已授权-本班文件可下载-200.png',
  })

  // ═══ 判定 ═══
  console.log('\n【判定】')
  const byLabel = (kw) => results.scenarios.find((s) => s.label.includes(kw))
  const checks = [
    ['S1 未授权直连文件接口 → 401', byLabel('S1')?.status === 401],
    ['S2 猜静态路径(A) → 404', byLabel('S2')?.status === 404],
    ['S3 猜静态路径(B) → 404', byLabel('S3')?.status === 404],
    ['S5 本班详情 → 200', byLabel('S5')?.status === 200],
    ['S6 本班文件可下载 → 200', byLabel('S6')?.status === 200],
    ['S6 带附件报头 attachment', String(byLabel('S6')?.contentDisposition).startsWith('attachment')],
    ['S7 跨班文件 → 404', byLabel('S7')?.status === 404],
    ['S8 不存在文件 → 404', byLabel('S8')?.status === 404],
  ]
  for (const [name, ok] of checks) {
    console.log(`  ${ok ? '✅' : '❌'} ${name}`)
    if (!ok) exitCode = 1
  }

  results.checks = checks.map(([name, ok]) => ({ name, pass: ok }))
  results.summary = { total: checks.length, passed: checks.filter(([, ok]) => ok).length, failed: exitCode ? 1 : 0 }
  console.log(`\n  合计 ${results.summary.passed}/${results.summary.total}`)

  writeFileSync(JSON_OUT, `${JSON.stringify(results, null, 2)}\n`)
  console.log(`  报告已写入 ${JSON_OUT}`)
} catch (err) {
  console.error(`\n取证失败：${err.message}`)
  exitCode = 1
} finally {
  try {
    cdp?.close()
  } catch {
    /* 忽略 */
  }
  chromeProc?.kill()
}

process.exit(exitCode)
