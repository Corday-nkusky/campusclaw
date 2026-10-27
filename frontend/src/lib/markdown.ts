import MarkdownIt from 'markdown-it'

/**
 * 材料正文的 Markdown 渲染器（R9.4、R10.1）。
 *
 * ── 为什么是这个库 ────────────────────────────────────────────
 *
 * R9.4 要求「按 Markdown 库的**默认转义策略**渲染，不执行材料内脚本」。
 * markdown-it 的默认值就是 `html: false`：正文里的原始 HTML 标签会被**转义成文本**，
 * 不会被解析进 DOM。所以 `<script>alert(1)</script>` 会原样显示成一串字符。
 *
 * 反例值得记下来：`marked` 从 v5 起**移除了 sanitize 选项**，默认不再转义原始 HTML，
 * 直接用它渲染用户上传的正文就等于给自己开一个存储型 XSS。选库时这是决定性的差别。
 *
 * `html: false` 是默认值，这里显式写出来是为了防止后来者「顺手打开」。
 * 一旦打开，整个 XSS 防线就只剩后端那句 `Content-Disposition: attachment`——
 * 而那条只保护下载路径，保护不了页面内渲染。
 */
const md = new MarkdownIt({
  html: false, // ← 唯一的 XSS 防线，别动
  linkify: true, // GFM 风格的裸链接自动成链
  breaks: true, // 材料正文多为手写，换行即换行更符合直觉
  typographer: false, // 不做智能引号替换：材料正文要按原样呈现
})

/**
 * 渲染 Markdown 为 HTML 字符串，交给 `dangerouslySetInnerHTML`。
 *
 * 名字里带 dangerously 是 React 的提醒，不是笔误：安全性由上面 `html: false`
 * 保证（输出中不含未经转义的用户 HTML）。改用别的渲染器前先确认它同样默认转义。
 */
export function renderMarkdown(source: string): string {
  return md.render(source)
}
