import { useEffect, useMemo, useRef, useState } from 'react'

/** 一条命令。`hint` 只用于在右侧显示快捷键或说明。 */
export interface Command {
  id: string
  label: string
  hint?: string
  run: () => void | Promise<void>
}

/**
 * ⌘/Ctrl+K 命令面板（R10.1）。
 *
 * 这里是**纯展示 + 键盘导航**，开关与命令清单由外层给：
 * 命令的可用性依赖当前身份与页面状态（例如「上传材料」只有教师有），
 * 放在外层才能一次算清，避免面板自己持有业务判断。
 *
 * 注意：面板里的「上传材料」只是**降低可发现性**的界面便利，
 * 它不构成访问控制——服务端 RequireTeacher 才是（R3.2）。
 */
export default function CommandPalette({
  commands,
  onClose,
}: {
  commands: Command[]
  onClose: () => void
}) {
  const [query, setQuery] = useState('')
  const [cursor, setCursor] = useState(0)
  const inputRef = useRef<HTMLInputElement>(null)

  const filtered = useMemo(() => {
    const kw = query.trim().toLowerCase()
    if (!kw) return commands
    return commands.filter(
      (c) => c.label.toLowerCase().includes(kw) || c.id.toLowerCase().includes(kw),
    )
  }, [commands, query])

  useEffect(() => {
    inputRef.current?.focus()
  }, [])

  useEffect(() => {
    setCursor(0)
  }, [query])

  function run(cmd: Command | undefined) {
    if (!cmd) return
    onClose()
    void cmd.run()
  }

  return (
    <div className="palette-backdrop" onMouseDown={onClose}>
      <div
        className="palette"
        role="dialog"
        aria-label="命令面板"
        onMouseDown={(e) => e.stopPropagation()}
      >
        <input
          ref={inputRef}
          className="palette-input"
          value={query}
          placeholder="输入命令…（↑↓ 选择，Enter 执行，Esc 关闭）"
          onChange={(e) => setQuery(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Escape') {
              e.preventDefault()
              onClose()
            } else if (e.key === 'ArrowDown') {
              e.preventDefault()
              setCursor((i) => Math.min(i + 1, filtered.length - 1))
            } else if (e.key === 'ArrowUp') {
              e.preventDefault()
              setCursor((i) => Math.max(i - 1, 0))
            } else if (e.key === 'Enter') {
              e.preventDefault()
              run(filtered[cursor])
            }
          }}
        />
        <ul className="palette-list">
          {filtered.map((c, i) => (
            <li key={c.id}>
              <button
                type="button"
                className={i === cursor ? 'palette-item active' : 'palette-item'}
                onMouseEnter={() => setCursor(i)}
                onClick={() => run(c)}
              >
                <span>{c.label}</span>
                {c.hint ? <span className="palette-hint">{c.hint}</span> : null}
              </button>
            </li>
          ))}
          {filtered.length === 0 && (
            <li className="palette-empty">
              <span className="muted">没有匹配的命令</span>
            </li>
          )}
        </ul>
      </div>
    </div>
  )
}
