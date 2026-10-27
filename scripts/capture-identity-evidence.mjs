#!/usr/bin/env node
/**
 * 采集「用户登录成功后，取班级材料时服务端如何识别用户标识」的真实报文证据。
 *
 * 产出（供 0923 作业第 ④ 题直接引用；add-token-auth 后识别方式为 Bearer JWT）：
 *   1. docs/screenshots/06-材料页-本班列表.png —— 教师登录后看到的材料页
 *   2. docs/identity-evidence.json —— 真实浏览器抓到的：
 *        · POST /api/login 的请求头 / 请求体 / 响应头与响应体（含签发的 JWT）
 *        · GET  /api/materials 的**请求头（含前端手工加的 Authorization: Bearer）** / 响应体
 *        · 页面内带 token fetch 别班材料 → 404 的结果
 *
 * 与 browser-verify.mjs 的关系：共用同一套极简 CDP 客户端与 Chrome 启动方式
 * （本机已装 Chrome，不下 Chromium）。那个脚本做的是**断言**，本脚本做的是**取证**。
 *
 * 用法：
 *   node scripts/capture-identity-evidence.mjs --user teacher_a --pass 'xxx'
 * 可选：
 *   --base    前端地址（默认 http://127.0.0.1:5173）
 *   --out     截图目录（默认 docs/screenshots）
 *   --json    证据输出路径（默认 docs/identity-evidence.json）
 *   --port    CDP 调试端口（默认 9334）
 *
 * 退出码：0 = 三类报文都采到；1 = 有环节缺失。
 */

import { spawn } from 'node:child_process'
import { loadEnv } from './lib/env.mjs'
import { existsSync, mkdirSync, mkdtempSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'

/**
 * 脱敏（add-token-auth 版）。
 *
 * 报告要能当证据留档，所以**保留结构**（三段式 JWT、`Authorization: Bearer`、
 * localStorage 的键名——这些恰恰是要分析的对象），但 token 本身是凭证，不进仓库。
 * 只留前 12 位与总长度 —— 足以证明「这里确实是一个签名凭证」。
 */
function redact(text) {
  return String(text)
    .replace(
      /(Bearer\s+)([A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+)/g,
      (_, prefix, value) => `${prefix}${value.slice(0, 12)}…（JWT，共 ${value.length} 字符，已脱敏）`,
    )
    .replace(
      // ⚠️ 要同时吃 `"token":"x"` 与落盘 JSON 转义后的 `\"token\":\"x\"` 两种形态
      // —— 只匹配未转义形态会让完整 JWT 漏进仓库（实测踩过）。
      /(\\?"(?:[A-Za-z]*_)?token\\?"\s*:\s*\\?")([A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+)(\\?")/g,
      (_, pre, value, post) => `${pre}${value.slice(0, 12)}…（JWT，共 ${value.length} 字符，已脱敏）${post}`,
    )
    .replace(
      /password(\\?"\s*:\s*\\?")([^"\\]*)/gi,
      (_, sep) => `password${sep}***（已脱敏）`,
    )
    // 兜底：不管键叫什么，三段式 JWT 一律脱敏。
    // localStorageDump 里的键名是 `cc_access`（不带 token 字样），按 `*_token` 匹配
    // 的规则看不见它 —— 2026-10-27 这份证据文件就把完整 JWT 落进了仓库。
    .replace(
      /\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b/g,
      (value) => `${value.slice(0, 12)}…（JWT，${value.length} 字符，已脱敏）`,
    )
    // refresh token 是 **opaque 串**（不是三段式 JWT），按 JWT 形态的规则看不见它。
    // localStorageDump 里键叫 cc_refresh —— 它同样能换 access token，必须一起脱敏。
    .replace(
      /(\\?"[A-Za-z_]*(?:token|refresh|access|secret)[A-Za-z_]*\\?"\s*:\s*\\?")([A-Za-z0-9_-]{16,})(\\?")/gi,
      (_, pre, value, post) => `${pre}${value.slice(0, 6)}…（${value.length} 字符，已脱敏）${post}`,
    )
}

function parseArgs(argv) {
  const out = {}
  for (let i = 0; i < argv.length; i += 1) {
    const a = argv[i]
    if (!a.startsWith('--')) continue
    const key = a.slice(2)
    const val = argv[i + 1] && !argv[i + 1].startsWith('--') ? (i += 1, argv[i]) : 'true'
    out[key] = val
  }
  return out
}

// 口令默认取 .env 的种子口令：命令行参数仍然优先。
loadEnv()
const args = parseArgs(process.argv.slice(2))
const BASE = args.base ?? 'http://127.0.0.1:5173'
const OUT_DIR = path.resolve(args.out ?? 'docs/screenshots')
const JSON_OUT = path.resolve(args.json ?? 'docs/identity-evidence.json')
const USER = args.user ?? 'teacher_a'
const PASS = args.pass ?? process.env.SEED_TEACHER_PASSWORD
const PORT = Number(args.port ?? 9334)

const CHROME_CANDIDATES = [
  args.chrome,
  'C:/Program Files/Google/Chrome/Application/chrome.exe',
  'C:/Program Files (x86)/Google/Chrome/Application/chrome.exe',
  'C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe',
].filter(Boolean)

const chromePath = CHROME_CANDIDATES.find((p) => existsSync(p))
if (!chromePath) {
  console.error('找不到 Chrome / Edge，可用 --chrome 指定路径')
  process.exit(1)
}
if (!USER || !PASS) {
  console.error('必须提供 --user 与 --pass')
  process.exit(1)
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

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
      if (slots) {
        for (const slot of [...slots]) {
          slot.cb(msg.params)
          if (slot.once) slots.splice(slots.indexOf(slot), 1)
        }
      }
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

  on(method, cb, once = false) {
    if (!this.listeners.has(method)) this.listeners.set(method, [])
    this.listeners.get(method).push({ cb, once })
  }

  close() {
    try {
      this.ws.close()
    } catch {
      /* 关不掉也不影响退出 */
    }
  }
}

async function launchChrome() {
  const profile = mkdtempSync(path.join(tmpdir(), 'cc-evidence-'))
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
      /* CDP 端点还没起来，继续等 */
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

async function waitFor(cdp, expr, label, timeoutMs = 20000) {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    if (await evaluate(cdp, expr)) return
    await sleep(150)
  }
  throw new Error(`等待超时：${label}`)
}

async function screenshot(cdp, file, fullPage = false) {
  const r = await cdp.send('Page.captureScreenshot', { format: 'png', captureBeyondViewport: fullPage })
  writeFileSync(file, Buffer.from(r.data, 'base64'))
  return file
}

/** React 受控输入：必须走原型链上的原生 setter，再派发 input 事件。 */
function setInputValue(selector, value) {
  return `(() => {
  const el = document.querySelector(${JSON.stringify(selector)});
  if (!el) return 'NO_ELEMENT';
  const proto = el instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
  const setter = Object.getOwnPropertyDescriptor(proto, 'value').set;
  setter.call(el, ${JSON.stringify(value)});
  el.dispatchEvent(new Event('input', { bubbles: true }));
  return el.value;
})()`
}

const headerPairs = (headers) => {
  if (!headers) return []
  if (Array.isArray(headers)) return headers.map((h) => [String(h.name), String(h.value)])
  return Object.entries(headers).map(([k, v]) => [String(k), String(v)])
}

let chromeProc
let cdp
let exitCode = 0

try {
  mkdirSync(OUT_DIR, { recursive: true })

  const launched = await launchChrome()
  chromeProc = launched.proc
  cdp = await Cdp.connect(launched.wsUrl)

  const records = new Map() // requestId → 记录
  const findRec = (requestId) => records.get(requestId)
  // 两个事件到达顺序不保证，谁先到谁建记录，另一个补字段。
  const ensureRec = (requestId) => {
    let rec = records.get(requestId)
    if (!rec) {
      rec = {}
      records.set(requestId, rec)
    }
    return rec
  }

  cdp.on('Network.requestWillBeSent', (p) => {
    const rec = ensureRec(p.requestId)
    rec.url = p.request.url
    rec.method = p.request.method
    rec.requestHeaders = p.request.headers
    rec.requestBody = p.request.postData
    rec.resourceType = p.type
  })

  // ⚠️ 关键：`requestWillBeSent` 的 `request.headers` **不含 Cookie** ——
  // Chrome 在这个事件里刻意剥离了它（Authorization 等凭证类头同理）。
  // 带 Cookie 的完整请求头只在 `requestWillBeSentExtraInfo` 里给到，
  // 而「materials 请求头里到底带了什么」恰恰是本脚本要取证的核心。
  // 少了这一个监听，抓出来的证据会恰好缺掉最关键的那一行。
  cdp.on('Network.requestWillBeSentExtraInfo', (p) => {
    const rec = ensureRec(p.requestId)
    rec.requestHeadersExtra = p.headers
    rec.associatedCookies = p.associatedCookies
  })

  cdp.on('Network.responseReceived', async (p) => {
    const rec = findRec(p.requestId)
    if (!rec) return
    rec.status = p.response.status
    rec.mimeType = p.response.mimeType
    rec.responseHeaders = p.response.headers
    try {
      const body = await cdp.send('Network.getResponseBody', { requestId: p.requestId })
      rec.responseBody = body.base64Encoded ? '（base64 编码，本脚本未解码）' : body.body
    } catch (e) {
      rec.responseBodyError = String(e.message ?? e)
    }
  })

  // Set-Cookie 只在原始响应头里，responseReceived 的 headers 不含它（踩过）
  cdp.on('Network.responseReceivedExtraInfo', (p) => {
    const rec = findRec(p.requestId)
    if (rec) rec.rawResponseHeaders = p.headers
  })

  await cdp.send('Page.enable')
  await cdp.send('Runtime.enable')
  await cdp.send('Network.enable')
  await cdp.send('Emulation.setDeviceMetricsOverride', {
    width: 1280,
    height: 900,
    deviceScaleFactor: 1,
    mobile: false,
  })

  console.log('== 1. 打开登录页')
  await cdp.send('Page.navigate', { url: BASE })
  await waitFor(cdp, `!!document.querySelector('#username')`, '登录页渲染出账号输入框')
  console.log(`   已加载：${BASE}`)

  console.log('== 2. 提交登录（这一步会在响应体里签发 JWT）')
  await evaluate(cdp, setInputValue('#username', USER))
  await evaluate(cdp, setInputValue('#password', PASS))
  await evaluate(cdp, `document.querySelector('button[type="submit"]').click(); true`)
  // 登录后落地在「工作台」页。材料列表自 §6 起搬到独立的「材料」页，
  // 所以必须先点导航里的「材料」页签，否则永远等不到 .material-list。
  await waitFor(cdp, `!!document.querySelector('.app-nav')`, '已登录并渲染主导航')
  await evaluate(
    cdp,
    `(() => {
      const tab = Array.from(document.querySelectorAll('.app-nav .nav-tab')).find((b) => b.textContent.trim() === '材料');
      if (!tab) return 'NO_TAB';
      tab.click();
      return 'clicked';
    })()`,
  )
  await waitFor(cdp, `!!document.querySelector('.material-list') || !!document.querySelector('.placeholder')`, '材料区渲染完成')
  console.log('   登录成功，材料页已渲染')

  console.log('== 3. 截图')
  const shot = await screenshot(cdp, path.join(OUT_DIR, '06-材料页-本班列表.png'), true)
  console.log(`   ${shot}`)

  // 页面侧读 Cookie：token 方案下本就不应存在任何会话 Cookie
  const cookieVisible = await evaluate(cdp, `document.cookie`)
  const localStorageDump = await evaluate(
    cdp,
    `JSON.stringify(Object.fromEntries(Object.entries(localStorage)))`,
  )
  const renderedItems = await evaluate(
    cdp,
    `Array.from(document.querySelectorAll('.material-title')).map((e) => e.textContent)`,
  )
  const scopeText = await evaluate(cdp, `document.body.innerText.match(/命中 \\d+ 条/)?.[0] ?? ''`)

  console.log('== 4. 在页面内直接请求别班材料（跨班访问的浏览器侧证据）')
  // add-token-auth：裸 fetch 不带凭证只会得到 401（未认证），
  // 取证目标是**授权**语义（404），所以必须带上本账号的 token。
  const crossClass = await evaluate(
    cdp,
    `(async () => {
      const r = await fetch('/api/materials/2', {
        headers: { Authorization: 'Bearer ' + localStorage.getItem('cc_access') },
      });
      return { status: r.status, body: await r.text() };
    })()`,
  )
  console.log(`   GET /api/materials/2 → ${crossClass.status} ${crossClass.body}`)

  // 会话 Cookie 应当不存在（token 方案不再下发）
  const cookies = await cdp.send('Network.getCookies', { urls: [BASE] })
  const sessionCookie = (cookies.cookies ?? []).find((c) => c.name === 'cc_session')

  // 记录里可能只有 ExtraInfo 一侧的字段（url 由 requestWillBeSent 填），故用 ?. 过滤
  const loginRec = [...records.values()].find((r) => r.url?.includes('/api/login'))
  const materialRecs = [...records.values()].filter((r) => r.url?.includes('/api/materials'))

  const evidence = {
    generatedAt: new Date().toISOString(),
    frontendBase: BASE,
    account: USER,
    note:
      '本文件由 scripts/capture-identity-evidence.mjs 用真实 Chrome（CDP）抓取。' +
      'JWT 与口令已脱敏；Authorization: Bearer 头与 localStorage 的结构刻意保留。',
    login: loginRec
      ? {
          method: loginRec.method,
          url: loginRec.url,
          requestHeaders: loginRec.requestHeaders,
          requestBody: loginRec.requestBody,
          status: loginRec.status,
          responseHeaders: loginRec.responseHeaders,
          setCookie: headerPairs(loginRec.rawResponseHeaders ?? loginRec.responseHeaders)
            .filter(([n]) => n.toLowerCase() === 'set-cookie')
            .map(([, v]) => v),
          responseBody: loginRec.responseBody,
        }
      : null,
    materialsRequests: materialRecs.map((r) => ({
      method: r.method,
      url: r.url,
      requestHeaders: r.requestHeaders,
      requestHeadersExtra: r.requestHeadersExtra,
      associatedCookies: r.associatedCookies,
      status: r.status,
      responseBody: r.responseBody,
    })),
    browserSideCredentialCheck: {
      documentCookie: cookieVisible,
      localStorage: localStorageDump,
      sessionCookieSeenByCdp: sessionCookie
        ? { name: sessionCookie.name, httpOnly: sessionCookie.httpOnly, sameSite: sessionCookie.sameSite, session: sessionCookie.session, domain: sessionCookie.domain, path: sessionCookie.path }
        : null,
    },
    renderedMaterials: renderedItems,
    renderedScope: scopeText,
    crossClassProbe: { url: '/api/materials/2', ...crossClass },
  }

  writeFileSync(JSON_OUT, JSON.stringify(evidence, null, 2))
  const redacted = redact(JSON.stringify(evidence, null, 2))
  if (redacted !== JSON.stringify(evidence, null, 2)) {
    // 若脱敏改动了内容，落盘的应是脱敏版本
    writeFileSync(JSON_OUT, redacted)
  }
  console.log(`== 5. 证据 JSON → ${JSON_OUT}（已脱敏）`)

  // ── 人类可读摘要（这段就是作业第 ④ 题的答案骨架）────────────
  console.log('\n─── 摘要：取材料时，服务端凭什么识别身份 ───')
  const m = materialRecs.find((r) => r.method === 'GET')
  if (m) {
    // Authorization 是页面 JS 手工设置的头，requestWillBeSent 侧就能看到；
    // ExtraInfo 一侧是完整请求头，两者都取出来对照。
    const authHeader =
      headerPairs(m.requestHeadersExtra).find(([k]) => k.toLowerCase() === 'authorization') ??
      headerPairs(m.requestHeaders).find(([k]) => k.toLowerCase() === 'authorization')
    const hasAuthPlain = headerPairs(m.requestHeaders).some(([k]) => k.toLowerCase() === 'authorization')
    console.log(`请求行   : ${m.method} ${m.url}`)
    console.log(`请求头   : Authorization: ${authHeader ? redact(authHeader[1]) : '<未抓到>'}`)
    console.log(
      `           （该头由前端 JS 读取 localStorage 的 token 后手工添加，` +
        `requestWillBeSent 侧${hasAuthPlain ? '含' : '不含'}它）`,
    )
    console.log(`响应状态 : ${m.status}`)
    console.log(`响应体   : ${String(m.responseBody ?? '').slice(0, 300)}`)
  }
  console.log(`document.cookie          → ${JSON.stringify(cookieVisible)}（token 方案下应为空）`)
  console.log(`localStorage             → ${redact(localStorageDump)}`)
  console.log(
    `CDP 看到的会话 Cookie     → ${sessionCookie ? `仍存在（异常）：${sessionCookie.name}` : '不存在（符合 token 方案预期）'}`,
  )
  console.log(`跨班探测 /api/materials/2 → ${crossClass.status} ${crossClass.body}`)

  if (!loginRec || materialRecs.length === 0) {
    console.error('\n❌ 未抓齐 login 与 materials 两类报文')
    exitCode = 1
  }
} catch (e) {
  console.error(`\n❌ 采集失败：${e.message ?? e}`)
  exitCode = 1
} finally {
  try {
    cdp?.close()
  } catch {
    /* ignore */
  }
  try {
    chromeProc?.kill()
  } catch {
    /* ignore */
  }
}

process.exit(exitCode)
