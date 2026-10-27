import { useCallback, useRef, useState } from 'react'

export type ToastKind = 'info' | 'success' | 'error'

export interface Toast {
  id: number
  kind: ToastKind
  text: string
}

/**
 * 极简 toast 队列（R10.1 的「toast 提示」）。
 *
 * 刻意不引第三方库：需要的能力只有「推一条、几秒后自己消失」。
 * 错误类提示停留更久——要有时间读完「为什么失败」，尤其是 403/404 这类
 * 必须保持服务端语义的提示（R9.3）。
 */
export function useToasts() {
  const [toasts, setToasts] = useState<Toast[]>([])
  const seq = useRef(0)

  const dismiss = useCallback((id: number) => {
    setToasts((prev) => prev.filter((t) => t.id !== id))
  }, [])

  const notify = useCallback(
    (kind: ToastKind, text: string) => {
      const id = ++seq.current
      setToasts((prev) => [...prev, { id, kind, text }])
      window.setTimeout(() => dismiss(id), kind === 'error' ? 6000 : 3200)
    },
    [dismiss],
  )

  return { toasts, notify, dismiss }
}

export function ToastHost({
  toasts,
  onDismiss,
}: {
  toasts: Toast[]
  onDismiss: (id: number) => void
}) {
  if (toasts.length === 0) return null

  return (
    <div className="toast-host" role="status" aria-live="polite">
      {toasts.map((t) => (
        <div key={t.id} className={`toast toast-${t.kind}`}>
          <span className="toast-text">{t.text}</span>
          <button
            type="button"
            className="toast-close"
            aria-label="关闭提示"
            onClick={() => onDismiss(t.id)}
          >
            ×
          </button>
        </div>
      ))}
    </div>
  )
}
