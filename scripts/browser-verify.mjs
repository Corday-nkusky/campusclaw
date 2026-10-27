#!/usr/bin/env node
/**
 * 用**本机已装的 Chrome**（CDP，不下载 Chromium）跑一遍登录流程，并留下三类证据：
 *
 *   1. 截图（登录页 / 口令错误 / 登录后落地页 / 401 回登录页）
 *   2. 断言（页面文本、Cookie 属性、localStorage 是否为空、控制台有无报错）
 *   3. 真实浏览器看到的 HTTP 报文（/api/login 的请求头、请求体、响应状态与响应头）
 *
 * 为什么不用 agent-browser / Playwright：它们要另下约 500MB 的 Chromium，
 * 而本机 Google Chrome 已就绪。CDP 是 Chrome 自己的调试协议，Node 22 自带
 * WebSocket 与 fetch，够用且不引依赖。
 *
 * 用法：
 *   node scripts/browser-verify.mjs --user teacher_a --pass 'xxx'
 * 可选：
 *   --base   前端地址（默认 http://localhost:5173）
 *   --out    截图目录（默认 docs/screenshots）
 *   --port   CDP 调试端口（默认 9333）
 *   --chrome Chrome 可执行文件路径
 *
 * 退出码：0 = 全部断言通过；1 = 有断言失败或流程异常。
 */

import { spawn } from 'node:child_process'
import { loadEnv } from './lib/env.mjs'
import { existsSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'

/**
 * 凭证脱敏（add-token-auth 版）。
 *
 * 报告要能当证据留档（所以保留 token 的**结构**：三段式 JWT、`Authorization: Bearer`），
 * 但真实 token 属于凭证，不该随报告进仓库 —— 只留前缀与长度。
 */
function redact(text) {
  return String(text)
    .replace(
      /(Bearer\s+)([A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+)/g,
      (_, prefix, value) => `${prefix}${value.slice(0, 12)}…（JWT，${value.length} 字符，已脱敏）`,
    )
    .replace(
      // 键名要吃任意前缀：add-refresh-token 之后是 access_token / refresh_token，
      // 旧正则只认 token / cc_token，字段改名后 JWT 明文就漏进报告（2026-10-27 踩过）。
      /(\\?"(?:[A-Za-z]+_)?token\\?"\s*:\s*\\?")([A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+)(\\?")/g,
      (_, pre, value, post) => `${pre}${value.slice(0, 12)}…（JWT，${value.length} 字符，已脱敏）${post}`,
    )
    // 注意：写进 JSON 报告时引号会被转义成 \"，所以要同时吃
    // `"password":"x"` 与 `\"password\":\"x\"` 两种形态 ——
    // 只匹配未转义形态会让明文口令漏进仓库（踩过）。
    .replace(
      /password(\\?"\s*:\s*\\?")([^"\\]*)/gi,
      (_, sep) => `password${sep}***（已脱敏）`,
    )
    // 兜底：不管键叫什么，三段式 JWT 一律脱敏。
    // 加这条是因为 localStorageDump 里的键名是 `cc_access`（不带 token 字样），
    // 上面按 `*_token` 匹配的规则根本看不见它 —— 2026-10-27 因此把完整 JWT 落进了证据文件。
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

// ── 参数 ────────────────────────────────────────────────────────

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
const BASE = args.base ?? 'http://localhost:5173'
const OUT_DIR = path.resolve(args.out ?? 'docs/screenshots')
const USER = args.user ?? 'teacher_a'
const PASS = args.pass ?? process.env.SEED_TEACHER_PASSWORD
const PORT = Number(args.port ?? 9333)

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

// ── 极简 CDP 客户端 ─────────────────────────────────────────────

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

  waitEvent(method, timeoutMs = 20000) {
    return new Promise((resolve, reject) => {
      const timer = setTimeout(
        () => reject(new Error(`等待 ${method} 超时（${timeoutMs}ms）`)),
        timeoutMs,
      )
      this.on(method, (params) => {
        clearTimeout(timer)
        resolve(params)
      }, true)
    })
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
  const profile = mkdtempSync(path.join(tmpdir(), 'cc-chrome-'))
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

// ── 页面操作封装 ────────────────────────────────────────────────

async function evaluate(cdp, expression) {
  const r = await cdp.send('Runtime.evaluate', {
    expression,
    returnByValue: true,
    awaitPromise: true,
  })
  if (r.exceptionDetails) {
    const d = r.exceptionDetails.exception?.description ?? r.exceptionDetails.text
    throw new Error(`页面内执行异常：${d}`)
  }
  return r.result.value
}

async function waitFor(cdp, expr, label, timeoutMs = 15000) {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    if (await evaluate(cdp, expr)) return
    await sleep(150)
  }
  throw new Error(`等待超时：${label}`)
}

async function screenshot(cdp, file, fullPage = false) {
  const r = await cdp.send('Page.captureScreenshot', {
    format: 'png',
    captureBeyondViewport: fullPage,
  })
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

// ── 主流程 ─────────────────────────────────────────────────────

const results = []
const screenshots = []
function check(id, desc, ok, detail) {
  results.push({ id, desc, ok, detail })
  console.log(`  ${ok ? '✅' : '❌'} ${id}  ${desc}${detail ? ` —— ${detail}` : ''}`)
}

let chromeProc
let cdp

try {
  mkdirSync(OUT_DIR, { recursive: true })
  const launched = await launchChrome()
  chromeProc = launched.proc
  cdp = await Cdp.connect(launched.wsUrl)
  launched.profileForCleanup = launched.profile

  // 收集控制台报错与网络事件
  const consoleErrors = []
  const loginTraffic = new Map() // requestId → 记录
  let logoutStatus = null // B16：真实登出请求的 HTTP 状态

  cdp.on('Runtime.exceptionThrown', (p) => {
    consoleErrors.push(p.exceptionDetails?.exception?.description ?? p.exceptionDetails?.text ?? '未知 JS 异常')
  })
  cdp.on('Runtime.consoleAPICalled', (p) => {
    if (p.type === 'error') consoleErrors.push(p.args.map((a) => a.value ?? a.description).join(' '))
  })
  cdp.on('Log.entryAdded', (p) => {
    // source === 'network' 的是浏览器给 4xx/5xx 打的资源加载日志。
    // 本脚本**故意**要打出一串 401/501 来验证鉴权，不能把它们算成页面报错。
    if (p.entry.level === 'error' && p.entry.source !== 'network') consoleErrors.push(p.entry.text)
  })

  cdp.on('Network.requestWillBeSent', (p) => {
    if (p.request.url.includes('/api/login')) {
      loginTraffic.set(p.requestId, {
        requestId: p.requestId,
        method: p.request.method,
        url: p.request.url,
        requestHeaders: p.request.headers,
        requestBody: p.request.postData,
      })
    }
  })
  cdp.on('Network.responseReceived', (p) => {
    // B16 要显示真实登出请求的状态码，与登录报文分开记（登出可能触发多次，取最后一次）
    if (p.response?.url?.includes('/api/logout')) logoutStatus = p.response.status
    const rec = loginTraffic.get(p.requestId)
    if (!rec) return
    rec.status = p.response.status
    rec.statusText = p.response.statusText
    rec.mimeType = p.response.mimeType
    rec.responseHeaders = p.response.headers
    // 响应体必须**当场**取：登录发生在脚本开头，等 §9 再取早已被 Chrome 丢弃（踩过）
    cdp
      .send('Network.getResponseBody', { requestId: p.requestId })
      .then((b) => {
        rec.responseBody = b.base64Encoded ? '（base64）' : b.body
      })
      .catch(() => {
        rec.responseBody = '<读不到>'
      })
  })
  // 注意：`Network.responseReceived` 里的 response.headers **不含 Set-Cookie**。
  // 原始响应头（含 Set-Cookie）只有 responseReceivedExtraInfo 给得到。
  cdp.on('Network.responseReceivedExtraInfo', (p) => {
    const rec = loginTraffic.get(p.requestId)
    if (!rec) return
    rec.rawResponseHeaders = p.headers
    rec.statusCode = p.statusCode
  })

  /**
   * 把响应头统一成 `[name, value][]`。
   *
   * 两种形态都要吃：`responseReceived` 给的是对象，`responseReceivedExtraInfo`
   * 给的也是对象，但 CDP 文档里 header 数组形态同样存在（fetch/XHR 场景）。
   * 踩过的坑：ExtraInfo 的 headers 是对象，早期版本只判了 Array.isArray，
   * 结果 Set-Cookie 明明抓到了却读不出来，断言假失败。
   */
  const headerPairs = (headers) => {
    if (!headers) return []
    if (Array.isArray(headers)) return headers.map((h) => [String(h.name), String(h.value)])
    return Object.entries(headers).map(([k, v]) => [k, String(v)])
  }

  /** 取 Set-Cookie，优先用 ExtraInfo 的**原始**响应头。 */
  const setCookieOf = (rec) =>
    headerPairs(rec.rawResponseHeaders ?? rec.responseHeaders)
      .filter(([name]) => name.toLowerCase() === 'set-cookie')
      .map(([, value]) => value)

  await cdp.send('Page.enable')
  await cdp.send('Runtime.enable')
  await cdp.send('Network.enable')
  await cdp.send('Log.enable')
  await cdp.send('Emulation.setDeviceMetricsOverride', {
    width: 1280,
    height: 900,
    deviceScaleFactor: 1,
    mobile: false,
  })

  // ── 1. 打开首页，未登录应看到登录页 ──────────────────────────
  console.log('\n[1] 打开首页（未登录）')
  let base = BASE
  const loadPage = async (url) => {
    const loaded = cdp.waitEvent('Page.loadEventFired')
    await cdp.send('Page.navigate', { url })
    await loaded
    await sleep(500)
  }
  await loadPage(base + '/')

  let hasForm = await evaluate(cdp, `!!document.querySelector('#username') && !!document.querySelector('#password')`)
  if (!hasForm) {
    // Vite 默认只绑 IPv6 回环，localhost 解析失败时退到 [::1]
    const alt = base.replace('localhost', '[::1]')
    console.log(`   localhost 打不开，改试 ${alt}`)
    base = alt
    await loadPage(base + '/')
    hasForm = await evaluate(cdp, `!!document.querySelector('#username') && !!document.querySelector('#password')`)
  }
  check('B1', '未登录时渲染登录页（账号/口令输入框存在）', hasForm, base)

  const titleText = await evaluate(cdp, `document.querySelector('.card h1')?.textContent ?? ''`)
  check('B2', '页面标题为「登录」', titleText.trim() === '登录', JSON.stringify(titleText))
  screenshots.push(await screenshot(cdp, path.join(OUT_DIR, '01-登录页.png')))

  // ── 2. 登录前的本地状态探测 ─────────────────────────────────
  console.log('\n[2] 登录前：本地存储（add-token-auth：凭证在 localStorage，不在 Cookie）')
  const preToken = await evaluate(
    cdp,
    `({ has: !!localStorage.getItem('cc_access'), keys: Object.keys(localStorage).join(',') })`,
  )
  check('B3', '登录前 localStorage 无 cc_access（未登录不应有凭证）', !preToken.has, JSON.stringify(preToken))

  // ── 3. 错误口令 → 统一失败文案 ──────────────────────────────
  console.log('\n[3] 错误口令')
  // ⚠️ 这里刻意用一个**不存在的账号**而不是 --user 那个真实账号。
  // 原因：限流键是「用户名 + IP」，每跑一次本脚本就会给真实账号记一次失败；
  // 连跑 LOGIN_FAIL_THRESHOLD 次（默认 5）以内、且都在 LOGIN_LOCK_MINUTES
  // （默认 15 分钟）窗口内，就会把 teacher_a 锁掉，之后所有脚本一起假红。
  // 界面分支（统一文案「账号或口令不正确」）与账号是否存在无关，
  // 而「已存在账号 + 口令错误」这一条由 verify-auth.sh R1.2 覆盖
  // ——它跑在一台限流器为空的**一次性实例**上，不会污染任何状态。
  const WRONG_USER = 'no_such_user_browser_probe'
  await evaluate(cdp, setInputValue('#username', WRONG_USER))
  await evaluate(cdp, setInputValue('#password', 'definitely-wrong-password'))
  await evaluate(cdp, `document.querySelector('form').requestSubmit()`)
  await waitFor(cdp, `!!document.querySelector('.alert-danger')`, '错误提示出现')
  const errText = await evaluate(cdp, `document.querySelector('.alert-danger').textContent`)
  check('B4', '口令错误时给出统一文案，不区分账号是否存在', errText.includes('账号或口令不正确'), JSON.stringify(errText))
  screenshots.push(await screenshot(cdp, path.join(OUT_DIR, '02-登录失败.png')))

  // ── 4. 正确口令 → 落地页 ────────────────────────────────────
  console.log('\n[4] 正确口令（教师账号）')
  // 上一节把用户名填成了不存在的探测账号，这里必须**两个字段都重填**
  // ——只补口令的话会拿「不存在的账号 + 正确口令」去登录，必然超时（踩过）。
  await evaluate(cdp, setInputValue('#username', USER))
  await evaluate(cdp, setInputValue('#password', PASS))
  await evaluate(cdp, `document.querySelector('form').requestSubmit()`)
  await waitFor(cdp, `!!document.querySelector('.badge')`, '落地页出现', 20000)
  screenshots.push(await screenshot(cdp, path.join(OUT_DIR, '03-教师已登录.png')))

  const landing = await evaluate(
    cdp,
    `(() => ({
      badge: document.querySelector('.badge')?.textContent ?? '',
      who: document.querySelector('.who .name')?.textContent ?? '',
      classText: document.querySelector('.who .muted')?.textContent ?? '',
      heading: document.querySelector('.page h2')?.textContent ?? '',
      raw: document.querySelector('pre.raw')?.textContent ?? '',
    }))()`,
  )
  check('B5', '落地页显示角色为「教师」', landing.badge.trim() === '教师', landing.badge)
  check('B6', '落地页显示账号名', landing.who.trim() === USER, landing.who)
  check('B7', '落地页显示班级（来自服务端 class_name）', landing.classText.trim().length > 0, landing.classText)

  let meJson = null
  try {
    meJson = JSON.parse(landing.raw)
  } catch {
    /* 下面按 null 处理 */
  }
  check(
    'B8',
    '/api/me 原样返回 role 与 class_id',
    !!meJson && meJson.role === 'teacher' && typeof meJson.class_id === 'number',
    meJson ? JSON.stringify(meJson) : '解析失败',
  )

  // ── 5. 登录后：token 落位与 Cookie 消失 ────────────────────
  console.log('\n[5] 登录后：token 存储形态（add-token-auth R1.1 / design D4）')
  const tokenState = await evaluate(
    cdp,
    `(() => {
      const t = localStorage.getItem('cc_access');
      const keys = Object.keys(localStorage);
      // 身份字段黑名单：这些键一旦出现，就等于把「我是谁/我在哪个班」交给前端，
      // 而权限判定必须只看服务端（design D3）。
      const identityKeys = keys.filter(
        (k) => !k.startsWith('cc_') && /role|identity|class|user|role|name|token|exp/i.test(k),
      );
      const ccKeys = keys.filter((k) => k.startsWith('cc_')).sort().join(',');
      const otherKeys = keys
        .filter((k) => !k.startsWith('cc_') && !identityKeys.includes(k))
        .sort()
        .join(',');
      return {
        has: !!t,
        len: t ? t.length : 0,
        segs: t ? t.split('.').length : 0,
        ss: Object.keys(sessionStorage).length,
        ccKeys,
        identityKeys,
        otherKeys,
      };
    })()`,
  )
  check('B9', '登录后 localStorage 存在 cc_access', tokenState.has, `长度 ${tokenState.len}`)
  check('B10', 'token 为三段式 JWT', tokenState.segs === 3, `段数 ${tokenState.segs}`)
  const cookiesAfter = await cdp.send('Network.getAllCookies')
  const sessLeft = cookiesAfter.cookies.filter((c) => c.name === 'cc_session')
  check('B11', '不再下发会话 Cookie（cc_session 数量 0）', sessLeft.length === 0, `数量 ${sessLeft.length}`)
  check(
    'B14',
    'cc_* 只含三个凭证键（cc_access / cc_access_exp / cc_refresh），无身份字段，sessionStorage 为空',
    // 断言对象是「凭证命名空间 + 有没有存身份」。非 cc_ 前缀的界面偏好
    // （如登录页「记住账号口令」的 login_pref）既不参与鉴权也不泄露身份，
    // 用「键串完全相等」去卡它只会让每次加偏好都得改脚本。
    tokenState.ccKeys === 'cc_access,cc_access_exp,cc_refresh' &&
      tokenState.identityKeys.length === 0 &&
      tokenState.ss === 0,
    `cc=[${tokenState.ccKeys}] 身份字段=[${tokenState.identityKeys}] 其它=[${tokenState.otherKeys}] ss=${tokenState.ss}`,
  )

  // ── 6. 绕过界面直接访问受保护接口 ──────────────────────────
  console.log('\n[6] 绕过界面直接请求受保护接口（手工带 Bearer 头）')
  // ⚠️ 页面自己的 fetch 走 api.ts 的 authHeaders()；这里的裸 fetch 必须手工带 token，
  // 否则 401 是「没带凭证」，证明不了任何问题。
  const guarded = await evaluate(
    cdp,
    `fetch('/api/materials', { headers: { Authorization: 'Bearer ' + localStorage.getItem('cc_access') } })
       .then(async r => ({ status: r.status, body: await r.text() }))`,
  )
  // 步 5 之后列表已实现：这里断言「已登录的教师直接请求得到 200 且含本班材料标题」。
  // 若仍是 501，说明上传/列表未落地 —— 那是失败，不是通过。
  // 注意：不要截断 body 再 JSON.parse —— 材料多了以后必然截在 JSON 中间，
  // 断言就会因为「解析失败」假红（踩过：400 字符正好切在第 2 条材料中间）。
  let guardedJson = null
  try {
    guardedJson = JSON.parse(guarded.body)
  } catch {
    /* 下面按 null 处理 */
  }
  check(
    'B15',
    '受保护接口经浏览器直接请求返回 200，且响应体是本班材料',
    guarded.status === 200 && Array.isArray(guardedJson?.materials) && guardedJson.materials.length > 0,
    `HTTP ${guarded.status} class_id=${guardedJson?.class_id} count=${guardedJson?.count} 首条=${JSON.stringify(guardedJson?.materials?.[0]?.title)}`,
  )
  // 负对照：响应体里不能出现别班材料的标题（B 班种子为「B班-班级公约」）
  check(
    'B15b',
    '该响应体不含别班材料标题（隔离不靠前端过滤）',
    !guarded.body.includes('B班-'),
    guarded.body.includes('B班-') ? '出现了 B 班标题' : '未见 B 班标题',
  )

  // ── 7. 篡改 localStorage 后刷新（R9.1 的正面测试） ─────────
  console.log('\n[7] 篡改 localStorage 的角色后刷新')
  await evaluate(
    cdp,
    `(() => { localStorage.setItem('role','student'); localStorage.setItem('class_id','999'); return 'ok'; })()`,
  )
  await loadPage(base + '/')
  await waitFor(cdp, `!!document.querySelector('.badge')`, '刷新后回到落地页', 20000)
  const afterTamper = await evaluate(
    cdp,
    `(() => ({
      badge: document.querySelector('.badge')?.textContent ?? '',
      cls: document.querySelector('.who .muted')?.textContent ?? '',
      ls: Object.keys(localStorage).length,
    }))()`,
  )
  check(
    'B19',
    '篡改 localStorage 的 role=student 后刷新，界面仍显示服务端的「教师」',
    afterTamper.badge.trim() === '教师' && afterTamper.cls.trim() === 'A班',
    `badge=${afterTamper.badge} 班级=${afterTamper.cls} localStorage 键数=${afterTamper.ls}`,
  )
  // 只清掉伪造的键，**不能 clear()** —— cc_access 是合法凭证，后面还要用它
  await evaluate(cdp, `localStorage.removeItem('role'); localStorage.removeItem('class_id')`)

  // ── 8. 真实登出 → 服务端吊销 refresh → 回登录页（add-refresh-token R1.4 / R9.3）──
  //
  // ⚠️ 这一段 2026-10-27 修过一次，原来的写法是错的：
  //   旧：`fetch('/api/logout', {method:'POST'})` → 手工 `removeItem('cc_access')` → 点刷新
  //   错在两点：① add-refresh-token 之后 /api/logout 必须带 refresh_token 才吊销得了；
  //   ② 只删 cc_access 会留下 cc_refresh，前端 401 后**静默刷新**把用户救了回来，
  //     于是永远回不到登录页（B16 假红）。
  //   现在走页面上的真实「退出登录」按钮：前端带 refresh 调 /api/logout，
  //   服务端吊销，前端清三个键 —— 验的是产品行为，不是脚本自己模拟。
  console.log('\n[8] 登出后重新校验')
  const oldRefresh = await evaluate(cdp, `localStorage.getItem('cc_refresh')`)
  await evaluate(
    cdp,
    `(() => {
      const btn = Array.from(document.querySelectorAll('button')).find(
        (b) => b.textContent.trim() === '退出登录',
      );
      if (!btn) throw new Error('页面上找不到「退出登录」按钮');
      btn.click();
    })()`,
  )
  let backToLogin = false
  try {
    await waitFor(cdp, `!!document.querySelector('#username')`, '回到登录页', 15000)
    backToLogin = true
  } catch {
    backToLogin = false
  }
  // 三个 cc_* 键都必须清掉（access / refresh / access_exp），少一个就还能续期
  const leftKeys = await evaluate(
    cdp,
    `Object.keys(localStorage).filter((k) => k.startsWith('cc_'))`,
  )
  // 服务端吊销要真生效：拿登出前的 refresh 再换一次令牌，必须被拒
  const refreshAfterLogout = await evaluate(
    cdp,
    `fetch('/api/refresh', {
       method: 'POST',
       headers: { 'Content-Type': 'application/json' },
       body: JSON.stringify({ refresh_token: ${JSON.stringify(oldRefresh)} }),
     }).then((r) => r.status).catch(() => -1)`,
  )
  check(
    'B16',
    `登出（HTTP ${logoutStatus ?? '未捕获'}）后凭证清空、服务端 refresh 已吊销、回到登录页`,
    backToLogin && leftKeys.length === 0 && refreshAfterLogout === 401,
    `回登录页=${backToLogin} 剩余键=${JSON.stringify(leftKeys)} 旧 refresh 再换=${refreshAfterLogout}`,
  )
  screenshots.push(await screenshot(cdp, path.join(OUT_DIR, '04-会话失效回登录页.png')))

  // ── 8.5 深浅色（R10.1 的体验项之一，必须实测而不是看代码） ──
  console.log('\n[8.5] 深色模式')
  await cdp.send('Emulation.setEmulatedMedia', {
    features: [{ name: 'prefers-color-scheme', value: 'dark' }],
  })
  await sleep(400)
  const darkBg = await evaluate(cdp, `getComputedStyle(document.body).backgroundColor`)
  const lightBgBefore = 'rgb(244, 244, 246)'
  check('B20', '切到 prefers-color-scheme: dark 后页面底色确实变化', darkBg === 'rgb(22, 22, 26)', `${lightBgBefore} → ${darkBg}`)
  screenshots.push(await screenshot(cdp, path.join(OUT_DIR, '05-深色模式.png')))
  await cdp.send('Emulation.setEmulatedMedia', { features: [] })

  // ── 9. 浏览器侧看到的登录报文 ──────────────────────────────
  console.log('\n[9] 真实浏览器侧观测到的 /api/login 报文')
  const traffic = [...loginTraffic.values()]
  const success = traffic.find((t) => t.status === 200)
  if (success) {
    const setCookies = setCookieOf(success)
    const bodyText = success.responseBody ?? ''
    console.log(`   请求：${success.method} ${success.url}`)
    console.log(`   请求头 Content-Type：${success.requestHeaders['Content-Type'] ?? '-'}`)
    console.log(`   请求体：${redact(success.requestBody)}`)
    console.log(`   响应：HTTP ${success.status} ${success.statusText ?? ''}`)
    console.log(`   响应体（token 已脱敏）：${redact(bodyText).slice(0, 200)}`)
    check(
      'B17',
      '登录响应含 JWT 且不再下发 Set-Cookie（add-token-auth R1.1）',
      setCookies.length === 0 && /"access_token":"[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+"/.test(bodyText),
      setCookies.length > 0
        ? `意外出现 Set-Cookie：${redact(setCookies[0])}`
        : bodyText.includes('"access_token"')
          ? '响应体含三段式 access_token，无 Set-Cookie'
          : `响应体不可用：${redact(bodyText)}`,
    )
  } else {
    check('B17', '登录报文已被完整捕获（含 token 响应体）', false, '未捕获到 200 的 /api/login')
  }

  check('B18', '页面无 JS 异常与控制台报错（网络层 4xx/5xx 不计）', consoleErrors.length === 0, consoleErrors.join(' | ') || '无')

  // ── 落盘 ───────────────────────────────────────────────────
  const report = {
    generatedAt: new Date().toISOString(),
    base,
    chrome: chromePath,
    user: USER,
    screenshots,
    assertions: results,
    loginTraffic: traffic,
    consoleErrors,
  }
  writeFileSync(
    path.join(OUT_DIR, 'browser-login-report.json'),
    redact(JSON.stringify(report, null, 2)),
    'utf8',
  )

  const failed = results.filter((r) => !r.ok)
  console.log(`\n${results.length - failed.length}/${results.length} 项断言通过`)
  console.log(`截图目录：${OUT_DIR}`)
  process.exitCode = failed.length === 0 ? 0 : 1
} catch (err) {
  console.error(`\n❌ 流程异常：${err.message}`)
  process.exitCode = 1
} finally {
  cdp?.close()
  chromeProc?.kill()
}
