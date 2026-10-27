#!/usr/bin/env node
/**
 * 第 5 课「解题助手」前端端到端验证：用**本机已装的真实 Chrome**（CDP）把
 * 教师配置 → 学生提问 → 流式渲染 → 引用溯源 这一整条界面链路各跑一遍，
 * 产出截图 + 机器可判定的断言。退出码即判定。
 *
 * 覆盖：
 *   T1–T8   教师视角：进入解题助手、配置页三块（提示词/技能/智能体）可用，
 *           用**界面本身**建一条技能与一个智能体（不绕过 UI 直接调接口）
 *   T9–T12  对话：新建会话、提问、**逐段渲染**（生成中可见流式光标）、
 *           引用芯片出现并**可点开原文**（弹层含材料正文）
 *   T13     无命中：仍作答，且标注「未依据本班资料作答」（status 恒 ok，只能靠 citations 判空）
 *   T14–T16 学生视角：看不到「配置」、看得到已启用智能体、照样能问出流式回答
 *
 * 为什么不用 Playwright：它要另下约 500MB 的 Chromium，本机 Google Chrome 已就绪。
 * 与 browser-verify.mjs / e2e-acceptance.mjs 同一套极简 CDP 客户端思路（各自独立，
 * 这几支脚本历史上就各带一份，不共享模块以免互相牵制）。
 *
 * 用法：
 *   node scripts/e2e-tutor.mjs --teacher-pass 'xxx' --student-pass 'yyy'
 * 可选：
 *   --base    前端地址（默认 http://127.0.0.1:5173）
 *   --out     截图目录（默认 docs/screenshots-l5）
 *   --port    CDP 调试端口（默认 9336）
 *   --chrome  Chrome 可执行文件路径
 *
 * 退出码：0 = 全部断言通过；1 = 有断言失败或流程异常。
 *
 * 前置：MySQL + 后端（API_PORT，默认 18080）+ **Qdrant** + 前端 5173 都在跑；
 *       种子材料已建好索引（重启后端会自动补）。
 *       本脚本自建带随机后缀的技能/智能体，不依赖也不修改种子数据以外的既有配置。
 */

import { spawn } from 'node:child_process'
import { loadEnv } from './lib/env.mjs'
import { existsSync, mkdirSync, mkdtempSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'

// ── 参数 ────────────────────────────────────────────────────────

function parseArgs(argv) {
  const out = {}
  for (let i = 0; i < argv.length; i += 1) {
    const a = argv[i]
    if (!a.startsWith('--')) continue
    out[a.slice(2)] = argv[i + 1]
    i += 1
  }
  return out
}

// 口令默认取 .env 的种子口令：命令行参数仍然优先。
loadEnv()
const args = parseArgs(process.argv.slice(2))
const BASE = (args.base ?? 'http://127.0.0.1:5173').replace(/\/$/, '')
const OUT_DIR = args.out ?? 'docs/screenshots-l5'
const PORT = Number(args.port ?? 9336)
const TEACHER_USER = args['teacher-user'] ?? 'teacher_a'
const STUDENT_USER = args['student-user'] ?? 'student_a1'
const TEACHER_PASS = args['teacher-pass'] ?? process.env.SEED_TEACHER_PASSWORD
const STUDENT_PASS = args['student-pass'] ?? process.env.SEED_STUDENT_PASSWORD

if (!TEACHER_PASS || !STUDENT_PASS) {
  console.error('缺少口令：--teacher-pass 与 --student-pass 都要给')
  process.exit(2)
}

const CHROME_CANDIDATES = [
  args.chrome,
  'C:/Program Files/Google/Chrome/Application/chrome.exe',
  'C:/Program Files (x86)/Google/Chrome/Application/chrome.exe',
  'C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe',
  'C:/Program Files/Microsoft/Edge/Application/msedge.exe',
].filter(Boolean)
const chromePath = CHROME_CANDIDATES.find((p) => existsSync(p))
if (!chromePath) {
  console.error('找不到 Chrome / Edge，可用 --chrome 指定路径')
  process.exit(2)
}

mkdirSync(OUT_DIR, { recursive: true })

// ── 极简 CDP 客户端 ─────────────────────────────────────────────

const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

class Cdp {
  constructor(ws) {
    this.ws = ws
    this.seq = 0
    this.pending = new Map()
    ws.addEventListener('message', (ev) => {
      const msg = JSON.parse(ev.data)
      if (msg.id && this.pending.has(msg.id)) {
        const { resolve, reject } = this.pending.get(msg.id)
        this.pending.delete(msg.id)
        if (msg.error) reject(new Error(`${msg.error.message}（${msg.error.code}）`))
        else resolve(msg.result)
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

  close() {
    try {
      this.ws.close()
    } catch {
      /* 关不掉不影响退出 */
    }
  }
}

async function launchChrome() {
  const profile = mkdtempSync(path.join(tmpdir(), 'cc-tutor-'))
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
        if (page?.webSocketDebuggerUrl) return { proc, wsUrl: page.webSocketDebuggerUrl }
      }
    } catch {
      /* 端点还没起来 */
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
  const body = await evaluate(cdp, `document.body.innerText.slice(0, 300)`)
  throw new Error(`等待超时：${label}\n  当前页面文本：${body}`)
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

/** 按可见文本找元素并点击（前缀匹配，容忍图标字符）。 */
function clickByText(selector, text) {
  return `(() => {
  const el = Array.from(document.querySelectorAll(${JSON.stringify(selector)}))
    .find((b) => b.textContent.trim().includes(${JSON.stringify(text)}));
  if (!el) return 'NO_ELEMENT';
  el.click();
  return 'clicked';
})()`
}

/** 勾选某个「标签文本含 X」的复选框（已勾选则不动）。 */
function checkByLabel(text) {
  return `(() => {
  const items = Array.from(document.querySelectorAll('.tt-editor .tt-check'));
  const el = items.find((l) => l.textContent.includes(${JSON.stringify(text)}));
  if (!el) return 'NO_ELEMENT';
  const cb = el.querySelector('input[type=checkbox]');
  if (!cb) return 'NO_INPUT';
  if (!cb.checked) cb.click();
  return cb.checked ? 'checked' : 'unchecked';
})()`
}

async function screenshot(cdp, name) {
  const r = await cdp.send('Page.captureScreenshot', { format: 'png', captureBeyondViewport: true })
  const file = path.join(OUT_DIR, name)
  writeFileSync(file, Buffer.from(r.data, 'base64'))
  return file
}

// ── 断言收集 ───────────────────────────────────────────────────

const results = []
function check(id, label, ok, detail = '') {
  results.push({ id, label, ok: Boolean(ok), detail: String(detail) })
  console.log(`  ${ok ? '✅' : '❌'} ${id}  ${label}${detail ? ` —— ${detail}` : ''}`)
}

// ── 主流程 ─────────────────────────────────────────────────────

let chromeProc
let cdp
const stamp = String(Date.now() % 1000000)
const SKILL_NAME = `引导-${stamp}`
const AGENT_NAME = `助教-${stamp}`
const SKILL_SOURCE = `名称：${SKILL_NAME}
适用条件：细胞,周期,解题
工具：knowledge_search
资料：细胞学
启用：是

先让学生说出自己的思路，再一步步追问；只引导，不给最终答案。`

const QUESTION = '细胞周期是什么？'
const OFF_TOPIC = '量子色动力学的渐近自由是什么？'

try {
  console.log(`\n=== 启动 Chrome（${chromePath}）`)
  const launched = await launchChrome()
  chromeProc = launched.proc
  cdp = await Cdp.connect(launched.wsUrl)
  await cdp.send('Runtime.enable')
  await cdp.send('Page.enable')

  console.log(`\n=== A. 教师：进入解题助手`)
  await cdp.send('Page.navigate', { url: BASE })
  await waitFor(cdp, `!!document.querySelector('#username')`, '登录页')
  await evaluate(cdp, setInputValue('#username', TEACHER_USER))
  await evaluate(cdp, setInputValue('#password', TEACHER_PASS))
  await evaluate(cdp, `document.querySelector('button[type="submit"]').click(); true`)
  await waitFor(cdp, `!!document.querySelector('.app-nav')`, '登录后主界面', 20000)

  await evaluate(cdp, clickByText('.app-nav .nav-tab', '解题助手'))
  await waitFor(cdp, `document.querySelector('.page h1')?.textContent.includes('解题助手') === true`, '解题助手页')
  check('T1', '教师可进入「解题助手」页', true)
  check(
    'T2',
    '教师看到「对话 / 配置」双视角切换',
    (await evaluate(cdp, `document.querySelectorAll('.segmented .seg-btn').length`)) === 2,
    `分段按钮 ${await evaluate(cdp, `document.querySelectorAll('.segmented .seg-btn').length`)} 个`,
  )
  await screenshot(cdp, '01-教师-解题助手.png')

  // ── 配置页 ──
  console.log(`\n=== B. 教师：配置页（提示词 / 技能 / 智能体）`)
  await evaluate(cdp, clickByText('.segmented .seg-btn', '配置'))
  await waitFor(cdp, `!!document.querySelector('.tt-config')`, '配置页渲染')
  const panels = await evaluate(
    cdp,
    `Array.from(document.querySelectorAll('.tt-config .panel h2')).map(h => h.textContent.trim())`,
  )
  check(
    'T3',
    '配置页三块齐备（提示词 / 技能 / 智能体）',
    ['提示词', '技能', '智能体'].every((t) => panels.includes(t)),
    JSON.stringify(panels),
  )

  // 提示词：写入并保存
  await evaluate(cdp, setInputValue('.tt-config .panel:nth-of-type(1) textarea', `你是耐心的助教（${stamp}）。`))
  await evaluate(cdp, clickByText('.tt-config .panel:nth-of-type(1) .btn-primary', '保存提示词'))
  let promptSaved = false
  try {
    await waitFor(cdp, `document.body.innerText.includes('提示词已保存')`, '提示词保存 toast', 8000)
    promptSaved = true
  } catch {
    promptSaved = false
  }
  check('T4', '提示词可保存（走 PUT /api/prompt）', promptSaved)

  // 技能：用界面新建
  await evaluate(cdp, clickByText('.tt-config button', '新建技能'))
  await waitFor(cdp, `!!document.querySelector('.tt-editor textarea')`, '技能编辑器')
  await evaluate(cdp, setInputValue('.tt-editor textarea', SKILL_SOURCE))
  await evaluate(cdp, clickByText('.tt-editor .btn-primary', '保存'))
  await waitFor(cdp, `document.body.innerText.includes('技能已创建')`, '技能创建 toast', 10000)
  await waitFor(cdp, `document.body.innerText.includes(${JSON.stringify(SKILL_NAME)})`, '技能出现在列表', 10000)
  const skillRow = await evaluate(
    cdp,
    `(() => {
      const r = Array.from(document.querySelectorAll('.tt-row')).find(x => x.textContent.includes(${JSON.stringify(SKILL_NAME)}));
      return r ? r.innerText.replace(/\\n/g, ' ') : '';
    })()`,
  )
  check('T5', '技能经界面创建成功且解析字段正确（已启用 + 适用条件）', skillRow.includes('已启用') && skillRow.includes('细胞'), skillRow.slice(0, 90))

  // 智能体：用界面新建，勾选刚建的技能与本班材料
  await evaluate(cdp, clickByText('.tt-config button', '新建智能体'))
  await waitFor(cdp, `!!document.querySelector('.tt-editor input')`, '智能体编辑器')
  await evaluate(cdp, setInputValue('.tt-editor input', AGENT_NAME))
  await evaluate(cdp, setInputValue('.tt-editor textarea', '你是细胞学助教，只引导，不给最终答案。'))
  await evaluate(cdp, checkByLabel(SKILL_NAME))
  await evaluate(cdp, checkByLabel('细胞学'))
  await evaluate(cdp, clickByText('.tt-editor .btn-primary', '保存'))
  await waitFor(cdp, `document.body.innerText.includes('智能体已创建')`, '智能体创建 toast', 10000)
  const agentRow = await evaluate(
    cdp,
    `(() => {
      const r = Array.from(document.querySelectorAll('.tt-row')).find(x => x.textContent.includes(${JSON.stringify(AGENT_NAME)}));
      return r ? r.innerText.replace(/\\n/g, ' ') : '';
    })()`,
  )
  check(
    'T6',
    '智能体经界面创建成功，且已绑定 1 条技能 / 1 份材料',
    agentRow.includes('技能 1 条') && agentRow.includes('材料 1 份'),
    agentRow.slice(0, 90),
  )
  await screenshot(cdp, '02-教师-配置页.png')

  // ── 对话页（教师视角）──
  console.log(`\n=== C. 教师：对话与流式渲染`)
  await evaluate(cdp, clickByText('.segmented .seg-btn', '对话'))
  await waitFor(cdp, `!!document.querySelector('.tt-layout')`, '对话布局')

  // 智能体列表是异步拉取的：等下拉真的填充后再断言
  await waitFor(cdp, `document.querySelectorAll('.tt-select option').length > 0`, '智能体下拉加载', 20000)
  const options = await evaluate(
    cdp,
    `Array.from(document.querySelectorAll('.tt-select option')).map(o => o.textContent)`,
  )
  const hasAgentOption = options.some((t) => t.includes(AGENT_NAME))
  check('T7', '新建会话的智能体下拉里出现刚建的智能体', hasAgentOption, JSON.stringify(options).slice(0, 120))

  await evaluate(
    cdp,
    `(() => {
      const sel = document.querySelector('.tt-select');
      const proto = HTMLSelectElement.prototype;
      const opt = Array.from(sel.options).find(o => o.textContent.includes(${JSON.stringify(AGENT_NAME)}));
      if (!opt) return 'NO_OPTION';
      Object.getOwnPropertyDescriptor(proto, 'value').set.call(sel, opt.value);
      sel.dispatchEvent(new Event('change', { bubbles: true }));
      return opt.value;
    })()`,
  )
  await evaluate(cdp, clickByText('.tt-new .btn-primary', '新建会话'))
  await waitFor(cdp, `document.querySelectorAll('.tt-conv').length > 0`, '会话出现在列表', 15000)
  await waitFor(cdp, `!!document.querySelector('.tt-composer textarea')`, '输入框出现', 15000)
  check('T8', '新建会话后出现提问输入框', true)

  // 提问：观察流式（先出现增量文本，再收到 done）
  await evaluate(cdp, setInputValue('.tt-composer textarea', QUESTION))
  await evaluate(cdp, clickByText('.tt-composer .btn-primary', '发送'))

  // 逐段渲染：抓两次文本快照，并观察「生成中」的证据（流式光标 / 「停止」按钮）。
  // 只比长度会误判：模型答得快时两次采样可能都已结束。所以以「看见过生成中」为主证据。
  await waitFor(cdp, `!!document.querySelector('.tt-msg-bot')`, '助手气泡出现', 30000)
  let sawStreaming = false
  let lenA = 0
  let lenB = 0
  for (let i = 0; i < 80; i += 1) {
    const snap = await evaluate(
      cdp,
      `({
        len: document.querySelector('.tt-msg-bot .tt-msg-body')?.innerText.length ?? 0,
        caret: !!document.querySelector('.tt-caret'),
        stopping: !!document.querySelector('.tt-composer .btn-secondary'),
      })`,
    )
    if (snap.caret || snap.stopping) sawStreaming = true
    if (snap.len > 0 && lenA === 0) lenA = snap.len
    lenB = snap.len
    if (snap.len > 0 && !snap.caret && !snap.stopping) break
    await sleep(150)
  }
  await waitFor(cdp, `!!document.querySelector('.tt-cite') || !!document.querySelector('.tt-cites-empty')`, '引用区出现', 90000)
  // 等引用区就位后，判断是「有引用」还是「无依据」——两种都要看到，不能停在生成中
  const citeState = await evaluate(
    cdp,
    `({
      cites: document.querySelectorAll('.tt-cite').length,
      empty: !!document.querySelector('.tt-cites-empty'),
      caret: !!document.querySelector('.tt-caret'),
      text: document.querySelector('.tt-msg-bot .markdown-body')?.innerText ?? '',
      citesText: document.querySelector('.tt-cites')?.innerText.replace(/\\n/g, ' ') ?? '',
    })`,
  )
  check(
    'T9',
    '回答逐段渲染（生成中可见流式光标 / 停止按钮，文本随 delta 增长）',
    sawStreaming && lenB > 0,
    `看到生成中=${sawStreaming}，${lenA} 字 → ${lenB} 字`,
  )
  check('T10', '回答带本班资料的引用芯片', citeState.cites > 0, `引用 ${citeState.cites} 条；${citeState.citesText.slice(0, 80)}`)
  check('T11', '回答正文非空', citeState.text.trim().length > 0, `${citeState.text.trim().length} 字`)

  // 点引用 → 弹层含材料原文
  if (citeState.cites > 0) {
    await evaluate(cdp, `document.querySelector('.tt-cite').click(); true`)
    await waitFor(cdp, `!!document.querySelector('.kn-modal')`, '引用弹层', 15000)
    const modalText = await evaluate(cdp, `document.querySelector('.kn-modal-body')?.innerText ?? ''`)
    check('T12', '点击引用可打开材料原文（弹层含正文）', modalText.trim().length > 0, `${modalText.trim().length} 字`)
    await screenshot(cdp, '03-教师-流式回答与引用.png')
    await evaluate(cdp, `document.querySelector('.kn-modal .btn-ghost').click(); true`)
    await waitFor(cdp, `!document.querySelector('.kn-modal')`, '关闭弹层', 8000)
  } else {
    check('T12', '点击引用可打开材料原文（弹层含正文）', false, '没有引用芯片可点（T10 已失败，先修那条）')
  }

  // 无命中：仍作答 + 明示未依据资料。
  // 这里逐步验证，避免「新建→输入→发送」之间的异步竞争把失败点糊掉。
  const convsBefore = await evaluate(cdp, `document.querySelectorAll('.tt-conv').length`)
  await evaluate(cdp, clickByText('.tt-new .btn-primary', '新建会话'))
  let convsAfter = convsBefore
  for (let i = 0; i < 120; i += 1) {
    convsAfter = await evaluate(cdp, `document.querySelectorAll('.tt-conv').length`)
    if (convsAfter > convsBefore) break
    await sleep(150)
  }
  console.log(`  · 新建会话：列表 ${convsBefore} → ${convsAfter} 条`)
  const typed = await evaluate(cdp, setInputValue('.tt-composer textarea', OFF_TOPIC))
  const sendBtn = await evaluate(
    cdp,
    `(() => {
      const b = document.querySelector('.tt-composer .btn-primary');
      return b ? { text: b.textContent.trim(), disabled: b.disabled } : null;
    })()`,
  )
  console.log(`  · 输入框回填=${JSON.stringify(typed).slice(0, 40)}…，发送按钮=${JSON.stringify(sendBtn)}`)
  const msgBefore = await evaluate(cdp, `document.querySelectorAll('.tt-msg').length`)
  await evaluate(cdp, clickByText('.tt-composer .btn-primary', '发送'))
  let offReady = false
  let offSnap = null
  for (let i = 0; i < 400; i += 1) {
    offSnap = await evaluate(
      cdp,
      `({
        msgs: document.querySelectorAll('.tt-msg').length,
        empty: !!document.querySelector('.tt-cites-empty'),
        text: Array.from(document.querySelectorAll('.tt-msg-bot .markdown-body')).map(e => e.innerText).join(''),
      })`,
    )
    if (offSnap.empty) {
      offReady = true
      break
    }
    await sleep(150)
  }
  check(
    'T13',
    '检索落空仍作答，并标注「未依据本班资料作答」（status 恒 ok 的呈现）',
    offReady && offSnap.text.trim().length > 0,
    `会话 ${convsBefore}→${convsAfter}；消息 ${msgBefore}→${offSnap?.msgs}；标注=${offReady}；仍给出 ${offSnap?.text.trim().length ?? 0} 字`,
  )
  await screenshot(cdp, '04-教师-无命中仍作答.png')

  // ── 学生视角 ──
  console.log(`\n=== D. 学生：只看得到对话`)
  await evaluate(cdp, clickByText('header .btn-ghost', '退出登录'))
  await waitFor(cdp, `!!document.querySelector('#username')`, '回到登录页', 20000)
  await evaluate(cdp, setInputValue('#username', STUDENT_USER))
  await evaluate(cdp, setInputValue('#password', STUDENT_PASS))
  await evaluate(cdp, `document.querySelector('button[type="submit"]').click(); true`)
  await waitFor(cdp, `!!document.querySelector('.app-nav')`, '学生主界面', 20000)
  await evaluate(cdp, clickByText('.app-nav .nav-tab', '解题助手'))
  await waitFor(cdp, `document.querySelector('.page h1')?.textContent.includes('解题助手') === true`, '学生解题助手页')

  const segCount = await evaluate(cdp, `document.querySelectorAll('.segmented .seg-btn').length`)
  check('T14', '学生看不到「配置」（界面层按角色隐藏）', segCount === 0, `分段按钮 ${segCount} 个`)

  // 智能体列表是异步拉取的：必须等下拉真的填充后再断言，否则会误判成「看不到」
  await waitFor(
    cdp,
    `document.querySelectorAll('.tt-select option').length > 0`,
    '学生的智能体下拉加载',
    20000,
  )
  const studentAgent = await evaluate(
    cdp,
    `Array.from(document.querySelectorAll('.tt-select option')).some(o => o.textContent.includes(${JSON.stringify(AGENT_NAME)}))`,
  )
  check('T15', '学生看得到教师建好的已启用智能体', studentAgent)
  await screenshot(cdp, '05-学生-解题助手.png')

  await evaluate(
    cdp,
    `(() => {
      const sel = document.querySelector('.tt-select');
      const proto = HTMLSelectElement.prototype;
      const opt = Array.from(sel.options).find(o => o.textContent.includes(${JSON.stringify(AGENT_NAME)}));
      Object.getOwnPropertyDescriptor(proto, 'value').set.call(sel, opt.value);
      sel.dispatchEvent(new Event('change', { bubbles: true }));
      return opt.value;
    })()`,
  )
  await evaluate(cdp, clickByText('.tt-new .btn-primary', '新建会话'))
  await waitFor(cdp, `document.querySelectorAll('.tt-conv').length > 0`, '学生会话出现', 15000)
  await waitFor(cdp, `!!document.querySelector('.tt-composer textarea')`, '学生输入框', 15000)
  await evaluate(cdp, setInputValue('.tt-composer textarea', QUESTION))
  await evaluate(cdp, clickByText('.tt-composer .btn-primary', '发送'))
  await waitFor(cdp, `!!document.querySelector('.tt-cite')`, '学生侧引用芯片', 90000)
  const stuAnswer = await evaluate(cdp, `document.querySelector('.tt-msg-bot .markdown-body')?.innerText ?? ''`)
  check('T16', '学生同样能问出带引用的流式回答', stuAnswer.trim().length > 0, `${stuAnswer.trim().length} 字`)
  await screenshot(cdp, '06-学生-流式回答.png')
} catch (e) {
  console.error(`\n❌ 流程异常：${e.message}`)
  results.push({ id: 'FLOW', label: '流程异常', ok: false, detail: e.message })
} finally {
  cdp?.close()
  chromeProc?.kill()
}

// ── 汇总 ───────────────────────────────────────────────────────

const pass = results.filter((r) => r.ok).length
const total = results.length
console.log(`\n结果：${pass}/${total} 通过；截图见 ${OUT_DIR}/`)
if (pass !== total) {
  console.log('未通过：')
  for (const r of results.filter((x) => !x.ok)) console.log(`  - ${r.id} ${r.label}：${r.detail}`)
}
process.exit(pass === total && total > 0 ? 0 : 1)
