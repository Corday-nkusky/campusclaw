import { useRef, useState } from 'react'
import * as api from '../api'
import { describeApiError } from '../api'

interface Props {
  onUploaded: (result: api.UploadResult) => void
  onClose: () => void
}

/** 只用于文件选择框的过滤，**不是**访问控制——白名单由服务端判定（R4.2）。 */
const ACCEPT = '.txt,.md,text/plain,text/markdown'

function formatSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`
}

/**
 * 教师上传对话框。
 *
 * 两条要说清的事：
 *  1. 这里的扩展名判断只是给使用者的即时反馈，**真正的白名单在服务端**。
 *     前端放行不代表服务端放行，反之亦然。
 *  2. 进度来自 `XMLHttpRequest.upload.onprogress`（fetch 读不到上传进度）。
 */
export default function UploadDialog({ onUploaded, onClose }: Props) {
  const [file, setFile] = useState<File | null>(null)
  const [percent, setPercent] = useState(0)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [dragging, setDragging] = useState(false)
  const inputRef = useRef<HTMLInputElement>(null)

  const localHint =
    file && !/\.(txt|md)$/i.test(file.name)
      ? '仅支持 .txt 和 .md 格式的文件，这个文件可能无法上传。'
      : null

  async function submit() {
    if (!file || busy) return
    setBusy(true)
    setError(null)
    setPercent(0)
    try {
      const result = await api.uploadMaterial(file, setPercent)
      onUploaded(result)
    } catch (e) {
      setError(describeApiError(e))
    } finally {
      setBusy(false)
    }
  }

  function pick(f: File | null | undefined) {
    if (!f) return
    setFile(f)
    setError(null)
  }

  return (
    <div className="palette-backdrop" onMouseDown={busy ? undefined : onClose}>
      <div
        className="dialog"
        role="dialog"
        aria-label="上传材料"
        onMouseDown={(e) => e.stopPropagation()}
      >
        <div className="dialog-head">
          <h3>上传材料</h3>
          <button
            type="button"
            className="btn-ghost"
            onClick={onClose}
            disabled={busy}
            aria-label="关闭"
          >
            ×
          </button>
        </div>

        <div className="dialog-body">
          <div
            className={dragging ? 'dropzone dragging' : 'dropzone'}
            onDragOver={(e) => {
              e.preventDefault()
              setDragging(true)
            }}
            onDragLeave={() => setDragging(false)}
            onDrop={(e) => {
              e.preventDefault()
              setDragging(false)
              pick(e.dataTransfer.files?.[0])
            }}
            onClick={() => inputRef.current?.click()}
          >
            <input
              ref={inputRef}
              type="file"
              accept={ACCEPT}
              hidden
              onChange={(e) => pick(e.target.files?.[0])}
            />
            {file ? (
              <>
                <strong>{file.name}</strong>
                <span className="muted">{formatSize(file.size)}</span>
              </>
            ) : (
              <>
                <strong>点击选择文件，或拖拽到此处</strong>
                <span className="muted">仅支持 .txt 与 .md</span>
              </>
            )}
          </div>

          {localHint && <p className="alert alert-warn">{localHint}</p>}

          <div className="progress" aria-hidden={!busy}>
            <div className="progress-bar" style={{ width: `${percent}%` }} />
          </div>
          <p className="muted progress-label">
            {busy ? `上传中… ${percent}%` : percent === 100 ? '上传完成，正在处理…' : ''}
          </p>

          {error && <p className="alert alert-danger">{error}</p>}
        </div>

        <div className="dialog-foot">
          <button type="button" className="btn-ghost" onClick={onClose} disabled={busy}>
            取消
          </button>
          <button
            type="button"
            className="btn-primary"
            onClick={() => void submit()}
            disabled={!file || busy}
          >
            {busy ? '上传中…' : '上传'}
          </button>
        </div>
      </div>
    </div>
  )
}
