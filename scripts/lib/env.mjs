// 读取仓库根的 .env，补进 process.env（已有值不覆盖）。
//
// 为什么需要：这些浏览器脚本原本只接受命令行 --pass 传口令，
// 于是每跑一次都得去 .env 里翻 SEED_*_PASSWORD 再粘贴一遍。
// 口令本来就明文躺在 .env 里，脚本自己读一次就行 —— 显式传参仍然优先。
//
// 只支持 .env 的常见形态：KEY=VALUE、# 注释、单/双引号包裹的值。
// 不做 ${VAR} 展开：.env 里没有用到，加了反而要处理递归。

import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import path from 'node:path'

export function loadEnv() {
  const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..')
  let text
  try {
    text = readFileSync(path.join(root, '.env'), 'utf8')
  } catch {
    return // 没有 .env 就什么都不做，交给调用方按原有方式报错
  }
  for (const line of text.split(/\r?\n/)) {
    if (!line || line.trimStart().startsWith('#')) continue
    const m = /^\s*([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(.*)$/.exec(line)
    if (!m) continue
    let v = m[2].trim()
    if (v.length >= 2 && ((v.startsWith('"') && v.endsWith('"')) || (v.startsWith("'") && v.endsWith("'")))) {
      v = v.slice(1, -1)
    }
    if (process.env[m[1]] === undefined) process.env[m[1]] = v
  }
}
