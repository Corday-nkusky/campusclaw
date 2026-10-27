#!/usr/bin/env node
/**
 * 平台能力补齐项的前端端到端验证（第 7 课形式交付的一部分：证明"界面入口真的可用"）。
 *
 * 覆盖（全部**用界面本身**操作，不绕过 UI 直接调接口）：
 *   P1–P2  教师登录 → 进入解题助手「配置」，五个面板齐全
 *   P3–P4  提示词模板：新建一份含 3 个 {{变量}} 的模板 → 列表显示变量数 → 点「试渲染」看到预览
 *   P5     技能文件上传：注入一个 .md 文件 → 列表出现该技能
 *   P6     智能体绑定模板：选模板 → 出现 3 个变量输入框 → 填值保存
 *   P7     MCP：登记一台服务器 → 连接 → 工具清单显示 `unit_convert@demo-tools`
 *
 * 与 e2e-tutor.mjs 同一套极简 CDP 客户端思路（各脚本自带一份，不共享模块）。
 *
 * 用法：
 *   node scripts/e2e-platform.mjs --teacher-pass 'xxx' [--base http://127.0.0.1:5173] [--mcp http://127.0.0.1:18090]
 *
 * 退出码：0 = 全部通过；1 = 有断言失败；2 = 环境不满足（缺口令 / 找不到 Chrome）。
 */

import { spawn } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { cleanupDemoData } from './lib/cleanup-demo.mjs'

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

const args = parseArgs(process.argv.slice(2))
const BASE = (args.base ?? 'http://127.0.0.1:5173').replace(/\/$/, '')
const MCP_URL = args.mcp ?? 'http://127.0.0.1:18090'
const OUT_DIR = args.out ?? 'docs/screenshots-l7'
const PORT = Number(args.port ?? 9337)
const TEACHER_USER = args['teacher-user'] ?? 'teacher_a'
const TEACHER_PASS = args['teacher-pass']

if (!TEACHER_PASS) {
  console.error('缺少口令：--teacher-pass 必填')
  process.exit(2)
}

const CHROME_CANDIDATES = [
  args.chrome,
  'C:/Program Files/Google/Chrome/Application/chrome.exe',
  'C:/Program Files (x86)/Google/Chrome/Application/chrome.exe',
  'C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe',
].filter(Boolean)
const chromePath = CHROME_CANDIDATES.find((p) => existsSync(p))
if (!chromePath) {
  console.error('找不到 Chrome / Edge，可用 --chrome 指定路径')
  process.exit(2)
}
mkdirSync(OUT_DIR, { recursive: true })

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

  close() {
    try {
      this.ws.close()
    } catch {
      /* 关不掉不影响退出 */
    }
  }
}

async function launchChrome() {
  const profile = mkdtempSync(path.join(tmpdir(), 'cc-platform-'))
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
      '--window-size=1440,1200',
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
    if (await evaluate(cdp, expr)) return true
    await sleep(120)
  }
  const body = await evaluate(cdp, `document.body.innerText.slice(0, 400)`)
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

/** 在某个容器里按出现顺序设置第 nth 个输入框（0 起）。 */
function setNthValue(containerSel, nth, value) {
  return `(() => {
  const box = document.querySelector(${JSON.stringify(containerSel)});
  if (!box) return 'NO_CONTAINER';
  const els = box.querySelectorAll('input.input, textarea.input');
  const el = els[${nth}];
  if (!el) return 'NO_ELEMENT_' + els.length;
  const proto = el instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
  Object.getOwnPropertyDescriptor(proto, 'value').set.call(el, ${JSON.stringify(value)});
  el.dispatchEvent(new Event('input', { bubbles: true }));
  return el.value;
})()`
}

function clickByText(selector, text) {
  return `(() => {
  const el = Array.from(document.querySelectorAll(${JSON.stringify(selector)}))
    .find((b) => b.textContent.trim().includes(${JSON.stringify(text)}));
  if (!el) return 'NO_ELEMENT';
  el.click();
  return 'clicked';
})()`
}

async function screenshot(cdp, name) {
  const r = await cdp.send('Page.captureScreenshot', { format: 'png', captureBeyondViewport: true })
  const file = path.join(OUT_DIR, name)
  writeFileSync(file, Buffer.from(r.data, 'base64'))
  return file
}

const results = []
function check(id, label, ok, detail = '') {
  results.push({ id, label, ok: Boolean(ok), detail: String(detail) })
  console.log(`  ${ok ? '✅' : '❌'} ${id}  ${label}${detail ? ` —— ${detail}` : ''}`)
}

// ── 主流程 ─────────────────────────────────────────────────────

let chromeProc
let cdp
const stamp = String(Date.now() % 1000000)
const TPL_NAME = `生物助教-${stamp}`
const TPL_BODY = `你是{{班级}}的{{学科}}老师，负责带{{年级}}学生。先确认学生理解了题意，再给下一步提示。`
const SKILL_NAME = `上传技能-${stamp}`
const SKILL_FILE = `名称：${SKILL_NAME}
适用条件：上传,验证
工具：knowledge_search
资料：
启用：是

这是通过界面文件上传导入的技能，用来验证上传入口与粘贴走同一套解析器。`
const AGENT_NAME = `模板助教-${stamp}`
// 带时间戳保证唯一：同名服务器会因唯一约束返回 400，而重跑脚本不该被上一轮的数据挡住
const SERVER_NAME = args['server-name'] ?? `demo-${stamp}`

try {
  console.log(`\n=== 启动 Chrome（${chromePath}）`)
  const launched = await launchChrome()
  chromeProc = launched.proc
  cdp = await Cdp.connect(launched.wsUrl)
  await cdp.send('Runtime.enable')
  await cdp.send('Page.enable')

  console.log('\n=== P1 教师登录')
  await cdp.send('Page.navigate', { url: BASE })
  await waitFor(cdp, `!!document.querySelector('#username')`, '登录页')
  await evaluate(cdp, setInputValue('#username', TEACHER_USER))
  await evaluate(cdp, setInputValue('#password', TEACHER_PASS))
  await evaluate(cdp, clickByText('button', '登录'))
  await waitFor(cdp, `!!document.querySelector('.app-nav')`, '登录后的主导航')
  check('P1', '教师登录成功，看到主导航', true)

  console.log('\n=== P2 进入「解题助手 → 配置」')
  await evaluate(cdp, clickByText('.nav-tab', '解题助手'))
  await waitFor(cdp, `!!document.querySelector('.tt-head')`, '解题助手页')
  await evaluate(cdp, clickByText('.seg-btn', '配置'))
  await waitFor(cdp, `!!document.querySelector('.tt-config')`, '配置区')
  const panels = await evaluate(
    cdp,
    `Array.from(document.querySelectorAll('.tt-config .panel-head h2')).map((h) => h.textContent.trim())`,
  )
  const want = ['提示词', '提示词模板', '技能', '智能体', 'MCP 外部工具']
  const missing = want.filter((w) => !panels.includes(w))
  check('P2', '配置区五个面板齐全', missing.length === 0, `实际：${panels.join(' / ')}`)

  console.log('\n=== P3 新建提示词模板（含 3 个变量）')
  await evaluate(cdp, clickByText('.btn-secondary', '新建模板'))
  await waitFor(cdp, `document.body.innerText.includes('新建模板') && !!document.querySelector('.tt-editor')`, '模板编辑器')
  await evaluate(cdp, setNthValue('.tt-editor', 0, TPL_NAME))
  await evaluate(cdp, setNthValue('.tt-editor', 1, '第 7 课验证用模板'))
  await evaluate(cdp, setNthValue('.tt-editor', 2, TPL_BODY))
  await evaluate(cdp, clickByText('.tt-editor .btn-primary', '保存'))
  await waitFor(cdp, `document.body.innerText.includes(${JSON.stringify(TPL_NAME)})`, '模板出现在列表', 15000)
  const tplRow = await evaluate(
    cdp,
    `(() => {
      const li = Array.from(document.querySelectorAll('.tt-row')).find((x) => x.textContent.includes(${JSON.stringify(TPL_NAME)}));
      return li ? li.textContent : '';
    })()`,
  )
  check('P3', '模板已创建并在列表显示变量数', tplRow.includes('3 个变量'), tplRow.slice(0, 90))

  console.log('\n=== P4 试渲染（不调模型，看模板长什么样）')
  await evaluate(
    cdp,
    `(() => {
      const li = Array.from(document.querySelectorAll('.tt-row')).find((x) => x.textContent.includes(${JSON.stringify(TPL_NAME)}));
      if (!li) return 'NO_ROW';
      const b = Array.from(li.querySelectorAll('button')).find((x) => x.textContent.includes('试渲染'));
      if (!b) return 'NO_BUTTON';
      b.click();
      return 'clicked';
    })()`,
  )
  await waitFor(cdp, `!!document.querySelector('.tt-preview')`, '渲染预览')
  const preview = await evaluate(cdp, `document.querySelector('.tt-preview').textContent`)
  const unfilledText = await evaluate(
    cdp,
    `(() => {
      const box = Array.from(document.querySelectorAll('.tt-editor')).find((e) => e.textContent.includes('渲染预览'));
      return box ? box.textContent : '';
    })()`,
  )
  check(
    'P4',
    '试渲染不传变量时，占位符原样保留且列出未填项',
    preview.includes('{{班级}}') && preview.includes('{{学科}}') && unfilledText.includes('仍未填'),
    preview.slice(0, 60),
  )

  console.log('\n=== P5 上传技能文件（.md，走 multipart）')
  await evaluate(
    cdp,
    `(() => {
      const input = document.querySelector('input[type=file]');
      if (!input) return 'NO_INPUT';
      const dt = new DataTransfer();
      dt.items.add(new File([${JSON.stringify(SKILL_FILE)}], 'uploaded-skill.md', { type: 'text/markdown' }));
      input.files = dt.files;
      input.dispatchEvent(new Event('change', { bubbles: true }));
      return 'dispatched';
    })()`,
  )
  await waitFor(cdp, `document.body.innerText.includes(${JSON.stringify(SKILL_NAME)})`, '上传的技能出现在列表', 20000)
  check('P5', '上传的 .md 技能已入列表', true, SKILL_NAME)

  console.log('\n=== P6 智能体绑定模板并填变量')
  await evaluate(cdp, clickByText('.btn-secondary', '新建智能体'))
  await waitFor(cdp, `document.body.innerText.includes('新建智能体') && !!document.querySelector('.tt-editor')`, '智能体编辑器')
  const AGENT_BOX = `Array.from(document.querySelectorAll('.tt-editor')).find((e) => (e.querySelector('.tt-editor-head')?.textContent || '').includes('新建智能体'))`
  const agentSet = (nth, value) => `(() => {
  const box = ${AGENT_BOX};
  if (!box) return 'NO_BOX';
  const el = box.querySelectorAll('input.input, textarea.input')[${nth}];
  if (!el) return 'NO_ELEMENT';
  const proto = el instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
  Object.getOwnPropertyDescriptor(proto, 'value').set.call(el, ${JSON.stringify(value)});
  el.dispatchEvent(new Event('input', { bubbles: true }));
  return el.value;
})()`
  await evaluate(cdp, agentSet(0, AGENT_NAME))
  // 模板下拉：选中刚建的那份
  const picked = await evaluate(
    cdp,
    `(() => {
      const box = ${AGENT_BOX};
      const sel = box ? box.querySelector('select') : null;
      if (!sel) return 'NO_SELECT';
      const opt = Array.from(sel.options).find((o) => o.textContent.includes(${JSON.stringify(TPL_NAME)}));
      if (!opt) return 'NO_OPTION';
      const setter = Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, 'value').set;
      setter.call(sel, opt.value);
      sel.dispatchEvent(new Event('change', { bubbles: true }));
      return 'picked:' + opt.textContent;
    })()`,
  )
  await waitFor(cdp, `document.querySelectorAll('.tt-var input').length === 3`, '三个模板变量输入框')
  const vars = await evaluate(cdp, `Array.from(document.querySelectorAll('.tt-var .muted')).map((x) => x.textContent)`)
  await evaluate(
    cdp,
    `(() => {
      const els = document.querySelectorAll('.tt-var input');
      const vals = ['1 班', '生物学', '高一'];
      const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set;
      els.forEach((el, i) => {
        setter.call(el, vals[i]);
        el.dispatchEvent(new Event('input', { bubbles: true }));
      });
      return els.length;
    })()`,
  )
  await evaluate(
    cdp,
    `(() => {
      const box = ${AGENT_BOX};
      if (!box) return 'NO_BOX';
      const b = Array.from(box.querySelectorAll('button')).find((x) => x.textContent.includes('保存'));
      if (!b) return 'NO_BUTTON';
      if (b.disabled) return 'DISABLED';
      b.click();
      return 'clicked';
    })()`,
  )
  await waitFor(cdp, `document.body.innerText.includes(${JSON.stringify(AGENT_NAME)})`, '智能体出现在列表', 15000)
  check('P6', '智能体绑定模板并填了三个变量', vars.length === 3 && picked.startsWith('picked:'), vars.join(' '))

  console.log('\n=== P7 MCP 服务器：登记 → 连接 → 工具清单')
  await evaluate(cdp, clickByText('.btn-secondary', '登记服务器'))
  await waitFor(cdp, `document.body.innerText.includes('登记 MCP 服务器')`, 'MCP 编辑器')
  const mcpForm = await evaluate(
    cdp,
    `(() => {
      const box = Array.from(document.querySelectorAll('.tt-editor'))
        .find((e) => (e.querySelector('.tt-editor-head')?.textContent || '').includes('登记 MCP 服务器'));
      if (!box) return 'NO_BOX';
      const els = box.querySelectorAll('input.input');
      if (els.length < 2) return 'NO_INPUT_' + els.length;
      const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set;
      setter.call(els[0], ${JSON.stringify(SERVER_NAME)});
      els[0].dispatchEvent(new Event('input', { bubbles: true }));
      setter.call(els[1], ${JSON.stringify(MCP_URL)});
      els[1].dispatchEvent(new Event('input', { bubbles: true }));
      return 'filled:' + els.length;
    })()`,
  )
  await evaluate(
    cdp,
    `(() => {
      const box = Array.from(document.querySelectorAll('.tt-editor'))
        .find((e) => (e.querySelector('.tt-editor-head')?.textContent || '').includes('登记 MCP 服务器'));
      if (!box) return 'NO_BOX';
      const b = Array.from(box.querySelectorAll('button')).find((x) => x.textContent.includes('保存'));
      if (!b) return 'NO_BUTTON';
      b.click();
      return 'clicked';
    })()`,
  )
  await waitFor(cdp, `document.body.innerText.includes(${JSON.stringify(SERVER_NAME)})`, '服务器出现在列表', 15000)
  const createdRow = await evaluate(
    cdp,
    `(() => {
      const li = Array.from(document.querySelectorAll('.tt-row')).find((x) => x.textContent.includes(${JSON.stringify(SERVER_NAME)}));
      return li ? li.textContent : '';
    })()`,
  )
  check('P7a', 'MCP 服务器已登记', mcpForm.startsWith('filled:') && createdRow.includes('未连接'), createdRow.slice(0, 60))

  await evaluate(
    cdp,
    `(() => {
      const li = Array.from(document.querySelectorAll('.tt-row')).find((x) => x.textContent.includes(${JSON.stringify(SERVER_NAME)}));
      if (!li) return 'NO_ROW';
      const labels = Array.from(li.querySelectorAll('button')).map((x) => x.textContent.trim());
      const b = Array.from(li.querySelectorAll('button')).find((x) => x.textContent.trim().includes('连接'));
      if (!b) return 'NO_BUTTON: ' + labels.join('/');
      b.click();
      return 'clicked: ' + labels.join('/');
    })()`,
  )
  await waitFor(
    cdp,
    `(() => {
      const li = Array.from(document.querySelectorAll('.tt-row')).find((x) => x.textContent.includes(${JSON.stringify(SERVER_NAME)}));
      return !!li && /\\d+ 个工具/.test(li.textContent);
    })()`,
    '连接后显示工具数',
    20000,
  )
  const toolText = await evaluate(
    cdp,
    `(() => {
      const li = Array.from(document.querySelectorAll('.tt-row')).find((x) => x.textContent.includes(${JSON.stringify(SERVER_NAME)}));
      const b = li && Array.from(li.querySelectorAll('button')).find((x) => x.textContent.includes('看工具'));
      if (b) b.click();
      return li ? li.textContent : '';
    })()`,
  )
  await sleep(600)
  const listed = await evaluate(cdp, `document.body.innerText.includes('unit_convert@' + ${JSON.stringify(SERVER_NAME)})`)
  check('P7b', '连接后工具清单出现「工具名@服务器名」', listed, toolText.slice(0, 60))

  await screenshot(cdp, 'l7-platform-config.png')
  console.log(`\n  截图：${path.join(OUT_DIR, 'l7-platform-config.png')}`)
} catch (e) {
  check('EXC', '流程异常', false, e.message)
} finally {
  if (cdp) cdp.close()
  if (chromeProc) chromeProc.kill()

  // 收尾清理：本脚本为了验证界面入口，会在配置页里真建模板/技能/智能体/MCP 服务器。
  // 不清理的话每跑一次就多四条带时间戳的垃圾，跑几轮配置页就没法看了。
  // 清理失败只告警，不改退出码 —— 断言结果才是这个脚本的结论。
  try {
    const { deleted, skipped } = await cleanupDemoData({ base: BASE, pass: TEACHER_PASS })
    if (deleted.length > 0) {
      console.log(`\n  已清理本轮演示数据 ${deleted.length} 条：${deleted.map((d) => `${d.kind}#${d.id}`).join('、')}`)
    }
    for (const s of skipped) console.log(`  ⚠ 未清理 ${s.kind}#${s.id} ${s.name} —— ${s.reason}`)
  } catch (e) {
    console.warn(`  ⚠ 演示数据清理失败（不影响断言结果）：${e.message}`)
  }
}

const failed = results.filter((r) => !r.ok)
console.log(`\n=== 结果：${results.length - failed.length}/${results.length} 通过`)
if (failed.length > 0) {
  for (const f of failed) console.log(`  ❌ ${f.id} ${f.label} —— ${f.detail}`)
  process.exit(1)
}
process.exit(0)
