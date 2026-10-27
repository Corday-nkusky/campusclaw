#!/usr/bin/env node
/**
 * §8 验收取证：用**本机已装的真实 Chrome**（CDP）把 tasks.md §8.2 / §8.3 / R9.4
 * 要求的场景各跑一遍，产出截图 + 机器可判定的断言 + 脱敏 JSON。
 *
 * 覆盖：
 *   §8.2 主路径四条 —— 登录、上传、列表可见、学生只读
 *   §8.3 失败路径三条 —— 未登录、学生上传、跨班访问
 *   R9.3 401 → 回登录页；403 / 404 按服务端语义提示
 *   R9.4 材料正文里的脚本不执行（markdown-it 的 html:false）
 *   R10.1 列表/网格、⌘K 命令面板、上传进度、toast、GFM
 *
 * 为什么不用 Playwright：它要另下约 500MB 的 Chromium，本机 Google Chrome 已就绪。
 * 与 browser-verify.mjs / capture-identity-evidence.mjs 共用同一套极简 CDP 客户端。
 *
 * 用法：
 *   node scripts/e2e-acceptance.mjs --teacher-pass 'xxx' --student-pass 'yyy'
 * 可选：
 *   --base            前端地址（默认 http://127.0.0.1:5173）
 *   --out             截图目录（默认 docs/screenshots）
 *   --json            证据输出（默认 docs/acceptance-evidence.json）
 *   --port            CDP 调试端口（默认 9335）
 *   --teacher-user    默认 teacher_a
 *   --student-user    默认 student_a1
 *
 * 退出码：0 = 全部断言通过；1 = 有断言失败或流程异常。
 *
 * 前置：后端（.env 的 API_PORT，默认 18080）与前端 5173 都在跑；库处于种子状态
 *       （先 `bash scripts/reset-demo-data.sh --yes`，材料编号才与文档一致）。
 * 产物：截图写入 --out，JSON 写入 --json；**不会**自动清理上传的材料，
 *       重跑前再执行一次 reset-demo-data.sh 即可。
 */

import { spawn } from 'node:child_process'
import { loadEnv } from './lib/env.mjs'
import { existsSync, mkdirSync, mkdtempSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'

/** 凭证与口令脱敏（add-token-auth 版）。token 的**结构**刻意保留（它本身就是要验的对象）。 */
function redact(text) {
  return String(text)
    .replace(
      /(Bearer\s+)([A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+)/g,
      (_, prefix, value) => `${prefix}${value.slice(0, 12)}…（JWT，${value.length} 字符，已脱敏）`,
    )
    .replace(
      /(\\?"(?:[A-Za-z]*_)?token\\?"\s*:\s*\\?")([A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+)(\\?")/g,
      (_, pre, value, post) => `${pre}${value.slice(0, 12)}…（JWT，${value.length} 字符，已脱敏）${post}`,
    )
    .replace(/password(\\?"\s*:\s*\\?")([^"\\]*)/gi, (_, sep) => `password${sep}***（已脱敏）`)
    // 兜底：不管键叫什么，三段式 JWT 一律脱敏（localStorageDump 里的键是 cc_access，
    // 不带 token 字样，按 *_token 匹配的规则看不见它 —— 2026-10-27 漏过一次）。
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
    const val = argv[i + 1] && !argv[i + 1].startsWith('--') ? ((i += 1), argv[i]) : 'true'
    out[key] = val
  }
  return out
}

// 口令默认取 .env 的种子口令：命令行参数仍然优先。
loadEnv()
const args = parseArgs(process.argv.slice(2))
const BASE = args.base ?? 'http://127.0.0.1:5173'
const OUT_DIR = path.resolve(args.out ?? 'docs/screenshots')
const JSON_OUT = path.resolve(args.json ?? 'docs/acceptance-evidence.json')
const PORT = Number(args.port ?? 9335)
const T_USER = args['teacher-user'] ?? 'teacher_a'
const S_USER = args['student-user'] ?? 'student_a1'
const T_PASS = args['teacher-pass'] ?? process.env.SEED_TEACHER_PASSWORD
const S_PASS = args['student-pass'] ?? process.env.SEED_STUDENT_PASSWORD

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
if (!T_PASS || !S_PASS) {
  console.error('必须提供 --teacher-pass 与 --student-pass')
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
      ws.addEventListener('error', () => reject(new Error(`WebSocket 连接失败：${wsUrl}`)), {
        once: true,
      })
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
      this.on(
        method,
        (params) => {
          clearTimeout(timer)
          resolve(params)
        },
        true,
      )
    })
  }

  close() {
    try {
      this.ws.close()
    } catch {
      /* 关不掉不影响退出 */
    }
  }
}

async function launchChrome() {
  const profile = mkdtempSync(path.join(tmpdir(), 'cc-accept-'))
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
      /* CDP 端点还没起来 */
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

async function waitFor(cdp, expr, label, timeoutMs = 20000) {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    if (await evaluate(cdp, expr)) return true
    await sleep(120)
  }
  throw new Error(`等待超时：${label}`)
}

/** 轮询直到 expr 为真；超时返回 false（用于「可能来不及看到」的观测，不抛错）。 */
async function waitForSoft(cdp, expr, timeoutMs = 6000) {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    if (await evaluate(cdp, expr)) return true
    await sleep(60)
  }
  return false
}

async function screenshot(cdp, name, fullPage = true) {
  const r = await cdp.send('Page.captureScreenshot', {
    format: 'png',
    captureBeyondViewport: fullPage,
  })
  const file = path.join(OUT_DIR, name)
  writeFileSync(file, Buffer.from(r.data, 'base64'))
  screenshots.push(file)
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

/** 按可见文本找按钮并点击。 */
function clickByText(selector, text) {
  return `(() => {
  const el = Array.from(document.querySelectorAll(${JSON.stringify(selector)}))
    .find((b) => b.textContent.trim() === ${JSON.stringify(text)});
  if (!el) return 'NO_ELEMENT';
  el.click();
  return 'clicked';
})()`
}

/** 给隐藏的 file input 塞文件：必须走 CDP 的 DOM.setFileInputFiles，页面侧无法伪造。 */
async function setFileInput(cdp, selector, filePath) {
  const doc = await cdp.send('DOM.getDocument', { depth: -1 })
  const node = await cdp.send('DOM.querySelector', { nodeId: doc.root.nodeId, selector })
  if (!node.nodeId) throw new Error(`找不到文件输入框：${selector}`)
  await cdp.send('DOM.setFileInputFiles', { files: [filePath], nodeId: node.nodeId })
}

// ── 主流程 ─────────────────────────────────────────────────────

const results = []
const screenshots = []
const traffic = [] // 关键请求的报文记录

function check(id, desc, ok, detail) {
  results.push({ id, desc, ok: Boolean(ok), detail })
  console.log(`  ${ok ? '✅' : '❌'} ${id}  ${desc}${detail ? ` —— ${detail}` : ''}`)
}

function section(name) {
  console.log(`\n=== ${name}`)
}

let chromeProc
let cdp
let exitCode = 0

/** 上传用的材料正文：同时作为 GFM 与 XSS 的载荷。 */
const PAYLOAD_NAME = '2026-09-23-验收材料.md'
const PAYLOAD_BODY = [
  '# 验收材料：上传链路与正文渲染',
  '',
  '这是**教师**上传的 Markdown，用来验证 GFM 渲染与脚本不执行。',
  '',
  '- 列表项一',
  '- 列表项二',
  '',
  '| 字段 | 值 |',
  '| --- | --- |',
  '| class_id | 1 |',
  '| 来源 | e2e-acceptance.mjs |',
  '',
  '```go',
  'func main() { fmt.Println("hello") }',
  '```',
  '',
  '下面两行是 XSS 载荷。它们必须以**文本**出现在页面上，而不是变成 DOM：',
  '',
  '<script>window.__xss_script = 1</script>',
  '',
  '<img src="x" onerror="window.__xss_img = 1">',
  '',
  '链接：[OpenSpec](https://github.com/Fission-AI/OpenSpec)',
  '',
].join('\n')

try {
  mkdirSync(OUT_DIR, { recursive: true })
  const launched = await launchChrome()
  chromeProc = launched.proc
  cdp = await Cdp.connect(launched.wsUrl)

  const consoleErrors = []
  cdp.on('Runtime.exceptionThrown', (p) => {
    consoleErrors.push(
      p.exceptionDetails?.exception?.description ?? p.exceptionDetails?.text ?? '未知 JS 异常',
    )
  })
  cdp.on('Runtime.consoleAPICalled', (p) => {
    if (p.type === 'error') consoleErrors.push(p.args.map((a) => a.value ?? a.description).join(' '))
  })
  cdp.on('Log.entryAdded', (p) => {
    // 本脚本**故意**打出 401/403/404；浏览器给它们记的 network 日志不算页面报错
    if (p.entry.level === 'error' && p.entry.source !== 'network') consoleErrors.push(p.entry.text)
  })

  // 记录关键请求的报文（证明状态码来自服务端，而不是界面的说法）
  const reqIndex = new Map()
  const trackRe = /\/(api\/materials|api\/me|api\/login|api\/logout)/
  cdp.on('Network.requestWillBeSent', (p) => {
    if (!trackRe.test(p.request.url)) return
    reqIndex.set(p.requestId, {
      method: p.request.method,
      url: p.request.url,
      resourceType: p.type,
      requestHeaders: p.request.headers,
    })
  })
  cdp.on('Network.requestWillBeSentExtraInfo', (p) => {
    const rec = reqIndex.get(p.requestId)
    if (rec) rec.requestHeadersWithCookie = p.headers
  })
  cdp.on('Network.responseReceived', (p) => {
    const rec = reqIndex.get(p.requestId)
    if (!rec) return
    rec.status = p.response.status
    rec.mimeType = p.response.mimeType
    // 正文只记前 300 字符，够断言用，不至于把整份列表塞进证据文件
    cdp
      .send('Network.getResponseBody', { requestId: p.requestId })
      .then((b) => {
        rec.responseBody = b.base64Encoded ? '（base64）' : String(b.body).slice(0, 300)
      })
      .catch(() => {
        rec.responseBody = '<读不到>'
      })
  })
  cdp.on('Network.responseReceivedExtraInfo', (p) => {
    const rec = reqIndex.get(p.requestId)
    if (rec) rec.rawResponseHeaders = p.headers
  })

  await cdp.send('Page.enable')
  await cdp.send('Runtime.enable')
  await cdp.send('Network.enable')
  await cdp.send('DOM.enable')
  await cdp.send('Log.enable')
  await cdp.send('Emulation.setDeviceMetricsOverride', {
    width: 1360,
    height: 960,
    deviceScaleFactor: 1,
    mobile: false,
  })

  const loadPage = async (url) => {
    const loaded = cdp.waitEvent('Page.loadEventFired')
    await cdp.send('Page.navigate', { url })
    await loaded
    await sleep(400)
  }

  const clearCookies = async () => {
    await cdp.send('Network.clearBrowserCookies')
    await cdp.send('Network.clearBrowserCache')
    // add-token-auth：凭证在 localStorage，清 Cookie 不等于登出。
    // about:blank 下 localStorage 不可访问，用 try 兜住「页面还没加载过」的情况。
    try {
      await evaluate(cdp, `localStorage.clear()`)
    } catch {
      /* 尚未加载过站点 */
    }
  }

  const loginAs = async (user, pass) => {
    await clearCookies()
    await loadPage(BASE + '/')
    // 再清一次并重载：loadPage 之后清，才能保证渲染出来的应用是**未登录**态
    // ——带着上一个账号的 token 加载，应用会直接进已登录界面，等不到登录框。
    await evaluate(cdp, `localStorage.clear()`)
    await loadPage(BASE + '/')
    await waitFor(cdp, `!!document.querySelector('#username')`, '登录页')
    await evaluate(cdp, setInputValue('#username', user))
    await evaluate(cdp, setInputValue('#password', pass))
    await evaluate(cdp, `document.querySelector('button[type="submit"]').click(); true`)
    await waitFor(cdp, `!!document.querySelector('.app-nav')`, `登录 ${user} 后渲染主导航`)
  }

  const gotoMaterials = async () => {
    await evaluate(cdp, clickByText('.app-nav .nav-tab', '材料'))
    await waitFor(
      cdp,
      `!!document.querySelector('.material-list') || !!document.querySelector('.placeholder')`,
      '材料页渲染完成',
    )
  }

  // ═══════════════════════════════════════════════════════════
  // A. 失败路径之一：未登录
  // ═══════════════════════════════════════════════════════════
  section('A. 未登录（§8.3-1 / R9.2）')
  await clearCookies()
  await loadPage(BASE + '/')
  const anonymous = await evaluate(
    cdp,
    `(async () => {
      const list = await fetch('/api/materials').then(async r => ({ s: r.status, b: await r.text() }));
      const me   = await fetch('/api/me').then(async r => ({ s: r.status, b: await r.text() }));
      const up   = await fetch('/api/materials', { method: 'POST' }).then(async r => ({ s: r.status, b: await r.text() }));
      return {
        hasLoginForm: !!document.querySelector('#username'),
        list, me, up,
        bodyText: document.body.innerText.slice(0, 400),
      };
    })()`,
  )
  check('A1', '未登录时渲染登录页', anonymous.hasLoginForm)
  check(
    'A2',
    'GET /api/materials 未登录 → 401',
    anonymous.list.s === 401,
    `${anonymous.list.s} ${anonymous.list.b}`,
  )
  check('A3', 'GET /api/me 未登录 → 401', anonymous.me.s === 401, `${anonymous.me.s} ${anonymous.me.b}`)
  check(
    'A4',
    'POST /api/materials 未登录 → 401（先于角色判定）',
    anonymous.up.s === 401,
    `${anonymous.up.s} ${anonymous.up.b}`,
  )
  check(
    'A5',
    '401 响应体不含任何材料标题或磁盘路径',
    !anonymous.list.b.includes('班级公约') &&
      !anonymous.list.b.includes('uploads') &&
      !anonymous.list.b.includes('stored_name'),
    anonymous.list.b,
  )
  await screenshot(cdp, '12-未登录-直连受保护接口-401.png')

  // ═══════════════════════════════════════════════════════════
  // B. 主路径（§8.2）：登录 → 列表 → 上传 → 列表可见 → 详情/下载
  // ═══════════════════════════════════════════════════════════
  section('B. 教师主路径（§8.2）')

  await loginAs(T_USER, T_PASS)
  const landing = await evaluate(
    cdp,
    `(() => ({
      badge: document.querySelector('.badge')?.textContent ?? '',
      who: document.querySelector('.who .name')?.textContent ?? '',
      heading: document.querySelector('.page h2')?.textContent ?? '',
    }))()`,
  )
  check(
    'B1',
    '教师登录成功，工作台显示角色与账号',
    landing.badge.trim() === '教师' && landing.who.trim() === T_USER,
    `${landing.who} / ${landing.badge} / ${landing.heading}`,
  )
  await screenshot(cdp, '13-教师-工作台.png')

  await gotoMaterials()
  const listBefore = await evaluate(
    cdp,
    `(() => ({
      titles: Array.from(document.querySelectorAll('.material-title')).map(e => e.textContent),
      countText: document.querySelector('.panel-head .muted')?.textContent ?? '',
      hasUploadBtn: Array.from(document.querySelectorAll('button')).some(b => b.textContent.trim() === '上传材料'),
    }))()`,
  )
  check(
    'B2',
    '材料列表可见，且全是本班材料（不含 B 班种子）',
    listBefore.titles.length > 0 && !listBefore.titles.some((t) => t.includes('B班-')),
    `${listBefore.countText}，标题：${JSON.stringify(listBefore.titles)}`,
  )
  check('B3', '教师看到「上传材料」入口（依据 /api/me 的 role）', listBefore.hasUploadBtn)
  await screenshot(cdp, '14-教师-材料页-列表视图.png')

  // ── 上传：把载荷写成真实文件，交给 Chrome 的 file input ──
  //
  // 本机回环传输是毫秒级，不节流的话进度条根本来不及被看到 —— 所以下面会把
  // 上行限到 30KB/s（CDP 的**网络层**限速，不是界面作弊），并把正文填充到约
  // 300KB，让「上传中… N%」有约 10 秒的观察窗口。
  // 注意：填充内容追加在 XSS 载荷**之后**，前面 PAYLOAD_BODY 的 GFM/XSS 断言不受影响。
  const bigBody = PAYLOAD_BODY + '\n' + '填充段落，用于观察上传进度。\n'.repeat(3000)
  const uploadPath = path.join(tmpdir(), PAYLOAD_NAME)
  writeFileSync(uploadPath, bigBody, 'utf8')

  await evaluate(cdp, clickByText('button', '上传材料'))
  await waitFor(cdp, `!!document.querySelector('.dialog')`, '上传对话框出现')
  await setFileInput(cdp, '.dropzone input[type="file"]', uploadPath)
  await waitFor(
    cdp,
    `document.querySelector('.dropzone strong')?.textContent === ${JSON.stringify(PAYLOAD_NAME)}`,
    '对话框显示已选文件名',
  )

  await cdp.send('Network.emulateNetworkConditions', {
    offline: false,
    latency: 20,
    downloadThroughput: -1,
    uploadThroughput: 30 * 1024, // 30 KB/s → 约 300KB 走 10 秒
  })

  await evaluate(cdp, clickByText('.dialog-foot button', '上传'))
  // 等一个**非零**百分比再截图 —— 只抓到「上传中… 0%」说明进度条刚挂上去还没走，
  // 那样证不了「进度在动」。30KB/s / 约 300KB 给了约 10 秒窗口，足够抓到中间值。
  const sawMovingProgress = await waitForSoft(
    cdp,
    `(() => {
      const t = document.querySelector('.progress-label')?.textContent ?? '';
      const m = t.match(/(\\d+)%/);
      return !!m && Number(m[1]) > 0;
    })()`,
    9000,
  )
  const progressState = await evaluate(
    cdp,
    `(() => ({
      label: document.querySelector('.progress-label')?.textContent ?? '',
      barWidth: document.querySelector('.progress-bar')?.style.width ?? '',
      busyText: Array.from(document.querySelectorAll('.dialog-foot button')).map(b => b.textContent.trim()),
    }))()`,
  )
  await screenshot(cdp, '15-教师-上传进度.png', false)
  await cdp.send('Network.emulateNetworkConditions', {
    offline: false,
    latency: 0,
    downloadThroughput: -1,
    uploadThroughput: -1,
  })
  check(
    'B4',
    '上传进度条真实推进（截到非零百分比），按钮进入「上传中…」（R10.1）',
    sawMovingProgress && progressState.busyText.includes('上传中…'),
    `label=${JSON.stringify(progressState.label)} 条宽=${progressState.barWidth} 按钮=${JSON.stringify(progressState.busyText)}`,
  )

  await waitFor(cdp, `!!document.querySelector('.toast')`, '上传成功 toast', 30000)
  const toastText = await evaluate(cdp, `document.querySelector('.toast')?.textContent ?? ''`)
  check('B5', '上传成功后有 toast 提示（R10.1）', toastText.includes('已上传'), JSON.stringify(toastText))
  await screenshot(cdp, '16-教师-上传成功-toast.png', false)

  await waitFor(
    cdp,
    `Array.from(document.querySelectorAll('.material-title')).some(e => e.textContent.includes('验收材料'))`,
    '列表里出现新上传的材料',
    20000,
  )
  const listAfter = await evaluate(
    cdp,
    `(() => ({
      titles: Array.from(document.querySelectorAll('.material-title')).map(e => e.textContent),
      countText: document.querySelector('.panel-head .muted')?.textContent ?? '',
    }))()`,
  )
  check(
    'B6',
    '上传后本班列表可见该材料（§8.2 第 3 条）',
    listAfter.titles.some((t) => t.includes('验收材料')),
    `${listBefore.countText} → ${listAfter.countText}`,
  )
  check(
    'B7',
    '列表仍不含别班材料（隔离由服务端保证）',
    !listAfter.titles.some((t) => t.includes('B班-')),
    JSON.stringify(listAfter.titles),
  )

  // ── 详情 + Markdown 渲染 + XSS 断言 ──
  await evaluate(
    cdp,
    `(() => {
      const el = Array.from(document.querySelectorAll('.material-title')).find(e => e.textContent.includes('验收材料'));
      if (!el) return 'NO_ELEMENT';
      el.click();
      return 'clicked';
    })()`,
  )
  await waitFor(cdp, `!!document.querySelector('.markdown-body')`, '详情正文渲染', 20000)
  const md = await evaluate(
    cdp,
    `(() => {
      const box = document.querySelector('.markdown-body');
      return {
        html: box.innerHTML,
        hasScriptEl: box.querySelectorAll('script').length,
        hasImgEl: box.querySelectorAll('img').length,
        hasStrong: !!box.querySelector('strong'),
        hasTable: !!box.querySelector('table'),
        hasCode: !!box.querySelector('pre code'),
        hasLink: !!box.querySelector('a'),
        text: box.innerText.slice(0, 400),
        xssScript: typeof window.__xss_script,
        xssImg: typeof window.__xss_img,
        detailHead: document.querySelector('.detail-head')?.innerText?.replace(/\\n/g, ' | ') ?? '',
      };
    })()`,
  )
  check(
    'B8',
    'R9.4：材料正文里的 <script> 没有变成 DOM，也没有执行',
    md.hasScriptEl === 0 && md.xssScript === 'undefined' && md.html.includes('&lt;script&gt;'),
    `script 元素=${md.hasScriptEl} window.__xss_script=${md.xssScript} 正文含转义后的 &lt;script&gt;=${md.html.includes('&lt;script&gt;')}`,
  )
  check(
    'B9',
    'R9.4：onerror 载荷同样未生效（无 img 元素、无副作用）',
    md.hasImgEl === 0 && md.xssImg === 'undefined',
    `img 元素=${md.hasImgEl} window.__xss_img=${md.xssImg}`,
  )
  check(
    'B10',
    'R10.1：GFM 渲染齐备（粗体 / 表格 / 代码块 / 链接）',
    md.hasStrong && md.hasTable && md.hasCode && md.hasLink,
    `strong=${md.hasStrong} table=${md.hasTable} pre-code=${md.hasCode} a=${md.hasLink}`,
  )
  await screenshot(cdp, '17-教师-材料详情-Markdown渲染.png')

  // ── 下载（主路径第 4 环）──
  // add-token-auth：下载是 fetch + Bearer 头（design D5），裸 fetch 401。
  // 这里模拟客户端：从 localStorage 取 token，对鉴权 API 手工带头。
  const dl = await evaluate(
    cdp,
    `(async () => {
      const H = { Authorization: 'Bearer ' + localStorage.getItem('cc_access') };
      const list = await fetch('/api/materials', { headers: H }).then(r => r.json());
      const target = list.materials.find(m => m.title.includes('验收材料'));
      const r = await fetch('/api/materials/' + target.id + '/file', { headers: H });
      const buf = await r.arrayBuffer();
      return {
        id: target.id,
        status: r.status,
        disposition: r.headers.get('content-disposition'),
        contentType: r.headers.get('content-type'),
        bytes: buf.byteLength,
      };
    })()`,
  )
  check(
    'B11',
    '带 Bearer 头经鉴权 API 下载本班文件（200 + attachment）',
    dl.status === 200 && String(dl.disposition ?? '').includes('attachment'),
    `材料 ${dl.id} → ${dl.status}，${dl.bytes} 字节，Content-Disposition=${dl.disposition}`,
  )
  check('B12', '下载内容大小与上传一致', dl.bytes === Buffer.byteLength(bigBody, 'utf8'), `${dl.bytes} vs ${Buffer.byteLength(bigBody, 'utf8')}`)

  // ── 列表 / 网格切换 ──
  await evaluate(cdp, clickByText('.seg-btn', '网格'))
  await waitFor(cdp, `!!document.querySelector('.material-grid')`, '网格视图切换', 8000)
  const gridInfo = await evaluate(
    cdp,
    `(() => ({
      cards: document.querySelectorAll('.material-card').length,
      listGone: !document.querySelector('.material-list'),
    }))()`,
  )
  check(
    'B13',
    '列表/网格切换可用（R10.1）',
    gridInfo.cards > 0 && gridInfo.listGone,
    `卡片 ${gridInfo.cards} 张，列表已隐藏=${gridInfo.listGone}`,
  )
  await screenshot(cdp, '18-教师-网格视图.png')
  await evaluate(cdp, clickByText('.seg-btn', '列表'))
  await waitFor(cdp, `!!document.querySelector('.material-list')`, '切回列表', 8000)

  // ── 本班搜索（R10.2）──
  const search = await evaluate(
    cdp,
    `(async () => {
      const input = document.querySelector('.search-bar input');
      const proto = HTMLInputElement.prototype;
      Object.getOwnPropertyDescriptor(proto, 'value').set.call(input, '验收材料');
      input.dispatchEvent(new Event('input', { bubbles: true }));
      await new Promise(r => setTimeout(r, 60));
      document.querySelector('.search-bar button[type="submit"]').click();
      await new Promise(r => setTimeout(r, 900));
      return {
        titles: Array.from(document.querySelectorAll('.material-title')).map(e => e.textContent),
      };
    })()`,
  )
  const crossSearch = await evaluate(
    cdp,
    `(async () => {
      const input = document.querySelector('.search-bar input');
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set.call(input, 'B班-班级公约');
      input.dispatchEvent(new Event('input', { bubbles: true }));
      await new Promise(r => setTimeout(r, 60));
      document.querySelector('.search-bar button[type="submit"]').click();
      await new Promise(r => setTimeout(r, 900));
      return {
        titles: Array.from(document.querySelectorAll('.material-title')).map(e => e.textContent),
        placeholder: document.querySelector('.placeholder')?.innerText?.replace(/\\n/g, ' ') ?? '',
      };
    })()`,
  )
  check(
    'B14',
    'R10.2：本班搜索命中（搜「验收材料」）',
    search.titles.length > 0 && search.titles.every((t) => t.includes('验收材料')),
    JSON.stringify(search.titles),
  )
  check(
    'B15',
    'R10.2 + R5.1：用 B 班标题在 A 班搜不到（搜索也在班级范围内）',
    crossSearch.titles.length === 0,
    `命中 ${crossSearch.titles.length} 条；提示：${crossSearch.placeholder.slice(0, 60)}`,
  )

  // ── ⌘/Ctrl+K 命令面板 ──
  await evaluate(
    cdp,
    `(() => {
      const input = document.querySelector('.search-bar input');
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set.call(input, '');
      input.dispatchEvent(new Event('input', { bubbles: true }));
      return 'cleared';
    })()`,
  )
  await evaluate(
    cdp,
    `(() => {
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'k', ctrlKey: true, bubbles: true }));
      return 'sent';
    })()`,
  )
  await waitFor(cdp, `!!document.querySelector('.palette')`, '命令面板打开', 8000)
  const palette = await evaluate(
    cdp,
    `(() => ({
      items: Array.from(document.querySelectorAll('.palette-item span:first-child')).map(e => e.textContent),
      focused: document.activeElement?.className ?? '',
    }))()`,
  )
  check(
    'B16',
    '⌘/Ctrl+K 打开命令面板，且教师多出「上传材料」命令（R3.2 / R10.1）',
    palette.items.includes('上传材料') && palette.focused.includes('palette-input'),
    JSON.stringify(palette.items),
  )
  await screenshot(cdp, '19-教师-命令面板.png', false)
  await evaluate(
    cdp,
    `document.querySelector('.palette-input').dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })); true`,
  )
  await waitFor(cdp, `!document.querySelector('.palette')`, '命令面板关闭', 8000)

  // ═══════════════════════════════════════════════════════════
  // C. 学生：只读 + 上传 403 + 跨班 404
  // ═══════════════════════════════════════════════════════════
  section('C. 学生视角（§8.2 学生只读 / §8.3 学生上传、跨班访问）')

  await loginAs(S_USER, S_PASS)
  const studentLanding = await evaluate(
    cdp,
    `(() => ({
      badge: document.querySelector('.badge')?.textContent ?? '',
      heading: document.querySelector('.page h2')?.textContent ?? '',
      // 工作台上给出的「可做的事」描述
      canDo: Array.from(document.querySelectorAll('.kv dd')).map(e => e.textContent).join(' | '),
    }))()`,
  )
  check(
    'C1',
    '学生登录后角色为「学生」，工作台如实说明只读',
    studentLanding.badge.trim() === '学生' && studentLanding.canDo.includes('只能查看与下载'),
    `${studentLanding.badge} / ${studentLanding.heading} / ${studentLanding.canDo}`,
  )

  await gotoMaterials()
  const studentPage = await evaluate(
    cdp,
    `(() => ({
      uploadBtn: Array.from(document.querySelectorAll('button')).some(b => b.textContent.trim() === '上传材料'),
      titles: Array.from(document.querySelectorAll('.material-title')).map(e => e.textContent),
    }))()`,
  )
  check(
    'C2',
    'R3.2：学生界面**没有**上传入口（依据 /api/me 的角色）',
    studentPage.uploadBtn === false,
    `上传按钮存在=${studentPage.uploadBtn}`,
  )
  check(
    'C3',
    '§8.2 学生只读：同班材料照样可见（读权限不受角色限制）',
    studentPage.titles.length > 0 && studentPage.titles.some((t) => t.includes('验收材料')),
    JSON.stringify(studentPage.titles),
  )

  // 学生能看详情、能下载
  await evaluate(
    cdp,
    `(() => {
      const el = Array.from(document.querySelectorAll('.material-title')).find(e => e.textContent.includes('验收材料'));
      el.click(); return 'clicked';
    })()`,
  )
  await waitFor(cdp, `!!document.querySelector('.markdown-body')`, '学生打开详情', 20000)
  const studentRead = await evaluate(
    cdp,
    `(async () => {
      const H = { Authorization: 'Bearer ' + localStorage.getItem('cc_access') };
      const list = await fetch('/api/materials', { headers: H }).then(r => r.json());
      const target = list.materials.find(m => m.title.includes('验收材料'));
      const r = await fetch('/api/materials/' + target.id + '/file', { headers: H });
      const buf = await r.arrayBuffer();
      return { status: r.status, bytes: buf.byteLength, hasBody: !!document.querySelector('.markdown-body')?.innerText };
    })()`,
  )
  check(
    'C4',
    '§8.2 学生只读：可看详情（200）并可下载本班文件',
    studentRead.status === 200 && studentRead.bytes > 0 && studentRead.hasBody,
    `下载 ${studentRead.status}，${studentRead.bytes} 字节`,
  )
  await screenshot(cdp, '20-学生-材料页-无上传入口.png')

  // 绕过界面直接上传（add-token-auth：必须带学生自己的 token 才能测出 403 ——
  // 不带头是 401「未认证」，测不了「角色不够」这条授权语义）
  const studentUpload = await evaluate(
    cdp,
    `(async () => {
      const fd = new FormData();
      fd.append('file', new File(['学生尝试上传\\n'], 'student-try.md', { type: 'text/markdown' }));
      const r = await fetch('/api/materials', {
        method: 'POST',
        body: fd,
        headers: { Authorization: 'Bearer ' + localStorage.getItem('cc_access') },
      });
      return { status: r.status, body: await r.text() };
    })()`,
  )
  check(
    'C5',
    'R3.1 / R3.2 / §8.3：学生绕过界面直接 POST /api/materials → 403（角色在服务端判定）',
    studentUpload.status === 403,
    `${studentUpload.status} ${studentUpload.body}`,
  )

  // 跨班与不存在同形（带头访问 —— 学生 token 本身合法，404 来自班级判定）
  const crossClass = await evaluate(
    cdp,
    `(async () => {
      const H = { headers: { Authorization: 'Bearer ' + localStorage.getItem('cc_access') } };
      const other = await fetch('/api/materials/2', H).then(async r => ({ s: r.status, b: await r.text() }));
      const ghost = await fetch('/api/materials/99999999', H).then(async r => ({ s: r.status, b: await r.text() }));
      const bad   = await fetch('/api/materials/abc', H).then(async r => ({ s: r.status, b: await r.text() }));
      const file  = await fetch('/api/materials/2/file', H).then(async r => ({ s: r.status, b: await r.text() }));
      return { other, ghost, bad, file };
    })()`,
  )
  const sameShape =
    crossClass.other.s === 404 &&
    crossClass.other.b === crossClass.ghost.b &&
    crossClass.other.b === crossClass.bad.b
  check(
    'C6',
    'R5.2 / §8.3：跨班材料 ID 返回 404，且与「不存在」「非法 ID」逐字节同形',
    sameShape,
    `跨班=${crossClass.other.s}${crossClass.other.b} 不存在=${crossClass.ghost.s}${crossClass.ghost.b} 非法=${crossClass.bad.s}${crossClass.bad.b}`,
  )
  check(
    'C7',
    'R5.2：跨班文件接口同样 404，响应体不泄露别班信息',
    crossClass.file.s === 404 &&
      !crossClass.file.b.includes('B班') &&
      !crossClass.file.b.includes('stored_name'),
    `${crossClass.file.s} ${crossClass.file.b}`,
  )

  // 403 / 404 的**界面**语义（R9.3 / §6.4）。
  //
  // 服务端确实返回 403 与 404（C5/C6 已证）；这里要证的是**界面怎么显示它**。
  // 直接让界面撞上这两个状态码不好造：
  //   · 404：学生根本看不到别班材料，界面上没有能点出 404 的入口；
  //   · 403：学生的上传入口被界面藏了（C2 正是要证这个），也就点不出来。
  // 所以用 CDP 的 Fetch 域把响应改写成**服务端真实返回的那个响应体**再放行 ——
  // 这不是伪造结论，而是在测前端收到该状态码时的行为。改写的响应体与 C5/C6
  // 实测到的服务端响应体逐字相同。
  const intercept = { urlPart: null, method: null, status: 0, body: '' }
  cdp.on('Fetch.requestPaused', async (p) => {
    try {
      if (
        intercept.urlPart &&
        p.request.url.includes(intercept.urlPart) &&
        p.request.method === intercept.method
      ) {
        await cdp.send('Fetch.fulfillRequest', {
          requestId: p.requestId,
          responseCode: intercept.status,
          responseHeaders: [
            { name: 'Content-Type', value: 'application/json; charset=utf-8' },
            { name: 'Cache-Control', value: 'no-store' },
          ],
          body: Buffer.from(intercept.body, 'utf8').toString('base64'),
        })
      } else {
        await cdp.send('Fetch.continueRequest', { requestId: p.requestId })
      }
    } catch {
      /* 页面已经走了，忽略 */
    }
  })
  await cdp.send('Fetch.enable', {
    patterns: [{ urlPattern: '*/api/materials*', requestStage: 'Request' }],
  })

  // D1：404 —— 把材料列表改写成 404，材料页应弹「资源不存在，或不属于你所在的班级。」
  intercept.urlPart = '/api/materials'
  intercept.method = 'GET'
  intercept.status = 404
  intercept.body = crossClass.other.b // 与 C6 实测到的服务端 404 响应体逐字相同
  await evaluate(cdp, `document.querySelector('.search-bar button[type="submit"]').click(); true`)
  const four04Toast = await waitForSoft(
    cdp,
    `!!document.querySelector('.toast-error')`,
    8000,
  )
    ? await evaluate(cdp, `document.querySelector('.toast-error .toast-text')?.textContent ?? ''`)
    : ''
  check(
    'C8',
    'R9.3 界面层：404 时提示「资源不存在，或不属于你所在的班级」，不改写成权限问题',
    four04Toast.includes('不存在'),
    `toast：${JSON.stringify(four04Toast)}`,
  )
  await screenshot(cdp, '21-界面-404语义提示.png', false)

  // D2：403 —— 把上传接口改写成 403，上传对话框应显示「权限不足：上传只有教师可以操作。」
  // 学生没有上传入口，所以先切回教师身份来触发这条界面分支（状态码与响应体仍是服务端形态）。
  intercept.urlPart = null
  await loginAs(T_USER, T_PASS)
  await gotoMaterials()
  await evaluate(cdp, clickByText('button', '上传材料'))
  await waitFor(cdp, `!!document.querySelector('.dialog')`, '上传对话框出现（403 用例）')
  const smallPath = path.join(tmpdir(), 'ui-403-probe.md')
  writeFileSync(smallPath, '# 403 探测\n', 'utf8')
  await setFileInput(cdp, '.dropzone input[type="file"]', smallPath)
  await waitFor(cdp, `!!document.querySelector('.dropzone strong')`, '已选文件（403 用例）')
  intercept.urlPart = '/api/materials'
  intercept.method = 'POST'
  intercept.status = 403
  intercept.body = studentUpload.body // 与 C5 实测到的服务端 403 响应体逐字相同
  await evaluate(cdp, clickByText('.dialog-foot button', '上传'))
  const four03Text = (await waitForSoft(cdp, `!!document.querySelector('.dialog .alert-danger')`, 8000))
    ? await evaluate(cdp, `document.querySelector('.dialog .alert-danger')?.textContent ?? ''`)
    : ''
  check(
    'C9',
    'R9.3 界面层：403 时提示「权限不足：上传只有教师可以操作」，不伪装成「请重新登录」',
    four03Text.includes('权限不足') && !four03Text.includes('重新登录'),
    `对话框提示：${JSON.stringify(four03Text)}`,
  )
  await screenshot(cdp, '22-界面-403语义提示.png', false)
  await evaluate(cdp, `document.querySelector('.dialog-head button')?.click(); true`)
  intercept.urlPart = null
  await cdp.send('Fetch.disable')

  // 纯函数层再核一遍三类文案互不相同（界面以外的第二重保险）
  const toastSemantics = await evaluate(
    cdp,
    `(async () => {
      const mod = await import('/src/api.ts');
      const mk = (status, code) => new mod.ApiError(status, code);
      return {
        forbidden: mod.describeApiError(mk(403, 'forbidden')),
        notFound: mod.describeApiError(mk(404, 'not_found')),
        unauthorized: mod.describeApiError(mk(401, 'unauthorized')),
      };
    })()`,
  )
  check(
    'C10',
    'R9.3：403 / 404 / 401 三类文案互不相同，各自对得上服务端语义',
    toastSemantics.forbidden.includes('权限不足') &&
      toastSemantics.notFound.includes('不存在') &&
      toastSemantics.unauthorized.includes('未登录') &&
      toastSemantics.forbidden !== toastSemantics.unauthorized,
    `403→${toastSemantics.forbidden}｜404→${toastSemantics.notFound}｜401→${toastSemantics.unauthorized}`,
  )

  // ── 落盘 ──
  const evidence = {
    generatedAt: new Date().toISOString(),
    frontendBase: BASE,
    chrome: chromePath,
    accounts: { teacher: T_USER, student: S_USER },
    note:
      '由 scripts/e2e-acceptance.mjs 用真实 Chrome（CDP）跑出。JWT 与口令已脱敏；' +
      'Authorization: Bearer 头的结构刻意保留（它本身就是要验的对象）。' +
      '上传的载荷正文含 <script> 与 onerror 两段 XSS 试探，用于验证 R9.4。',
    screenshots,
    assertions: results,
    requestTraffic: [...reqIndex.values()].map((r) => ({
      method: r.method,
      url: r.url,
      status: r.status,
      authorizationHeader: (
        r.requestHeadersWithCookie?.Authorization ?? r.requestHeaders?.Authorization
      )?.slice(0, 40),
      responseBody: r.responseBody,
    })),
    payload: { name: PAYLOAD_NAME, bytes: Buffer.byteLength(bigBody, 'utf8'), xssProbes: 2 },
    consoleErrors,
  }
  writeFileSync(JSON_OUT, redact(JSON.stringify(evidence, null, 2)), 'utf8')

  const failed = results.filter((r) => !r.ok)
  console.log(`\n${results.length - failed.length}/${results.length} 项断言通过`)
  console.log(`截图 ${screenshots.length} 张：${OUT_DIR}`)
  console.log(`证据 JSON：${JSON_OUT}`)
  if (failed.length) {
    console.log('失败项：' + failed.map((f) => f.id).join(', '))
  }
  exitCode = failed.length === 0 ? 0 : 1
} catch (err) {
  console.error(`\n❌ 流程异常：${err.message}`)
  exitCode = 1
} finally {
  cdp?.close()
  chromeProc?.kill()
}

process.exit(exitCode)
