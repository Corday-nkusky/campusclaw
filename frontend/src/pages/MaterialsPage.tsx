import { useCallback, useEffect, useState } from 'react'
import * as api from '../api'
import { ApiError, describeApiError, type MaterialDetail, type MaterialList, type Me } from '../api'
import { renderMarkdown } from '../lib/markdown'
import UploadDialog from '../components/UploadDialog'

export type View = 'list' | 'grid'

interface Props {
  identity: Me
  /** 视图模式由外层持有：命令面板也要能切换它，状态放两处必然不一致。 */
  view: View
  onViewChange: (view: View) => void
  /** 递增即触发一次重新加载（命令面板的「刷新材料列表」用）。 */
  reloadToken: number
  /** 递增即打开上传对话框（命令面板的「上传材料」用）。 */
  uploadSignal: number
  /** 收到 401 时通知外层清状态回登录页（R9.3）。 */
  onSessionExpired: () => void
  notify: (kind: 'info' | 'success' | 'error', text: string) => void
  /** 上传对话框是模态：开合同态给外层，与命令面板互斥（单一活动模态）。 */
  onModalOpenChange: (open: boolean) => void
}

function formatSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`
}

function formatTime(iso: string): string {
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString('zh-CN', { hour12: false })
}

/**
 * 材料页（R9、R10）。
 *
 * 三条不能动摇的约定：
 *
 *  1. **隔离不在这里做**。列表来自 `GET /api/materials`（服务端已按会话班级过滤），
 *     详情走 `GET /api/materials/{id}`（服务端 fetch-then-check）。
 *     前端没有任何「过滤掉别班材料」的代码——有的话就等于把隔离做在客户端，
 *     而客户端逻辑对调用方不构成约束（R5.1 / R3.2）。
 *  2. **上传入口的显示依据只有 `identity.role`**，而 `identity` 来自 `GET /api/me`。
 *     界面隐藏只是降低可发现性；绕过界面直接调接口，服务端照样 403。
 *  3. **404 不做「友好化」改写**。跨班访问按规约就该表现为 404，
 *     界面的正确行为是照服务端语义提示，而不是猜「也许是你没权限」（R9.3）。
 */
export default function MaterialsPage({
  identity,
  view,
  onViewChange,
  reloadToken,
  uploadSignal,
  onSessionExpired,
  notify,
  onModalOpenChange,
}: Props) {
  const [list, setList] = useState<MaterialList | null>(null)
  const [loading, setLoading] = useState(true)
  const [keyword, setKeyword] = useState('')
  const [detail, setDetail] = useState<MaterialDetail | null>(null)
  const [detailLoading, setDetailLoading] = useState(false)
  const [showUpload, setShowUpload] = useState(false)

  const isTeacher = identity.role === 'teacher'

  /** 401 一律交给外层回登录页；其余错误按服务端语义提示（R9.3）。 */
  const fail = useCallback(
    (e: unknown, prefix: string): void => {
      if (e instanceof ApiError && e.status === 401) {
        onSessionExpired()
        return
      }
      notify('error', `${prefix}${describeApiError(e)}`)
    },
    [notify, onSessionExpired],
  )

  const load = useCallback(
    async (q?: string) => {
      setLoading(true)
      try {
        setList(await api.materials(q))
      } catch (e) {
        fail(e, '加载材料失败：')
      } finally {
        setLoading(false)
      }
    },
    [fail],
  )

  // reloadToken 变化 = 外部（命令面板）要求刷新一次
  useEffect(() => {
    void load()
  }, [load, reloadToken])

  // uploadSignal 变化 = 外部要求打开上传对话框。isTeacher 兜住「学生被外部触发」的情况；
  // 但即便真的触发到，服务端照样 403 —— 界面判断从来不是访问控制（R3.2）。
  useEffect(() => {
    if (uploadSignal > 0 && isTeacher) setShowUpload(true)
  }, [uploadSignal, isTeacher])

  // 上传对话框的开合同态给外层：命令面板与它同时只能开一个（单一活动模态）
  useEffect(() => {
    onModalOpenChange(showUpload)
  }, [showUpload, onModalOpenChange])

  async function openDetail(id: number) {
    if (detail?.id === id) {
      setDetail(null)
      return
    }
    setDetailLoading(true)
    try {
      setDetail(await api.material(id))
    } catch (e) {
      setDetail(null)
      fail(e, '打开材料失败：')
    } finally {
      setDetailLoading(false)
    }
  }

  const items = list?.materials ?? []

  /**
   * 下载（add-refresh-token）：Bearer 头浏览器导航带不了，
   * access 过期时由 api.ts 静默刷新后重放（下载是只读，重放安全）。
   * 所以是 fetch blob → ObjectURL → 动态 <a download>，而不是 <a href>。
   */
  const download = useCallback(
    async (id: number, title: string) => {
      try {
        const { url, filename } = await api.downloadMaterialFile(id, title)
        const a = document.createElement('a')
        a.href = url
        a.download = filename
        document.body.appendChild(a)
        a.click()
        a.remove()
        URL.revokeObjectURL(url)
      } catch (e) {
        fail(e, '下载失败：')
      }
    },
    [fail],
  )

  return (
    <main className="page">
      <h2>本班材料</h2>
      <p className="page-lead">
        {isTeacher
          ? '本班的全部教学材料都在这里，可以搜索、查看和下载；要新增材料，点右上角「上传材料」。'
          : '本班的全部教学材料都在这里，可以搜索、查看和下载。'}
      </p>

      <section className="panel">
        <div className="panel-head">
          <h3>{list ? list.class_name : '加载中…'}</h3>
          <span className="muted" style={{ fontSize: 12 }}>
            {list ? `共 ${list.count} 条` : ''}
          </span>
          <div className="spacer" />
          <div className="segmented" role="group" aria-label="视图切换">
            <button
              type="button"
              className={view === 'list' ? 'seg-btn active' : 'seg-btn'}
              onClick={() => onViewChange('list')}
            >
              列表
            </button>
            <button
              type="button"
              className={view === 'grid' ? 'seg-btn active' : 'seg-btn'}
              onClick={() => onViewChange('grid')}
            >
              网格
            </button>
          </div>
          {isTeacher && (
            <button type="button" className="btn-primary" onClick={() => setShowUpload(true)}>
              上传材料
            </button>
          )}
          <button
            type="button"
            className="btn-ghost"
            onClick={() => void load(keyword)}
            disabled={loading}
          >
            {loading ? '加载中…' : '刷新'}
          </button>
        </div>

        <div className="panel-body">
          <form
            className="search-bar"
            onSubmit={(e) => {
              e.preventDefault()
              void load(keyword)
            }}
          >
            <input
              className="input"
              type="search"
              placeholder="在本班材料中搜索标题…"
              value={keyword}
              onChange={(e) => setKeyword(e.target.value)}
            />
            <button className="btn-primary" type="submit" disabled={loading}>
              搜索
            </button>
            {keyword && (
              <button
                type="button"
                className="btn-ghost"
                onClick={() => {
                  setKeyword('')
                  void load('')
                }}
              >
                清空
              </button>
            )}
          </form>

          {!loading && items.length === 0 && (
            <div className="placeholder">
              {list?.query ? `本班没有标题匹配「${list.query}」的材料。` : '本班暂无材料。'}
              <br />
              搜索只在<strong>本班范围内</strong>进行，别班的材料不会出现在结果里。
            </div>
          )}

          {items.length > 0 && view === 'list' && (
            <ul className="material-list">
              {items.map((m) => (
                <li key={m.id} className="material-item">
                  <div className="material-main">
                    <button
                      type="button"
                      className="material-title"
                      onClick={() => void openDetail(m.id)}
                    >
                      {m.title}
                    </button>
                    <span className="material-meta">
                      {formatSize(m.size_bytes)} · {m.uploader} · {formatTime(m.created_at)}
                    </span>
                  </div>
                  <button type="button" className="btn-ghost" onClick={() => void download(m.id, m.title)}>
                    下载
                  </button>
                </li>
              ))}
            </ul>
          )}

          {items.length > 0 && view === 'grid' && (
            <div className="material-grid">
              {items.map((m) => (
                <article key={m.id} className="material-card">
                  <button
                    type="button"
                    className="material-title"
                    onClick={() => void openDetail(m.id)}
                  >
                    {m.title}
                  </button>
                  <span className="material-meta">
                    {formatSize(m.size_bytes)} · {m.uploader} · {formatTime(m.created_at)}
                  </span>
                  <button type="button" className="btn-ghost" onClick={() => void download(m.id, m.title)}>
                    下载
                  </button>
                </article>
              ))}
            </div>
          )}

          {detailLoading && <p className="muted">正在打开…</p>}

          {detail && (
            <div className="detail-box">
              <div className="detail-head">
                <strong>{detail.title}</strong>
                <span className="muted" style={{ fontSize: 12 }}>
                  {detail.class_name} · 上传者 {detail.uploader} ·{' '}
                  {formatSize(detail.size_bytes)}
                </span>
                <div className="spacer" />
                <button type="button" className="btn-ghost" onClick={() => void download(detail.id, detail.title)}>
                  下载
                </button>
                <button type="button" className="btn-ghost" onClick={() => setDetail(null)}>
                  收起
                </button>
              </div>
              {/*
                正文按 Markdown 库的**默认转义策略**渲染（R9.4）：
                渲染器是 markdown-it 且 html:false，正文中的原始 HTML 会被转义成文本，
                <script> / onerror 之类的载荷到不了 DOM。详见 src/lib/markdown.ts。
              */}
              <div
                className="markdown-body"
                dangerouslySetInnerHTML={{ __html: renderMarkdown(detail.body || '') }}
              />
            </div>
          )}
        </div>
      </section>

      {showUpload && (
        <UploadDialog
          onClose={() => setShowUpload(false)}
          onUploaded={(result) => {
            setShowUpload(false)
            notify('success', `已上传「${result.title}」。`)
            void load(keyword)
          }}
        />
      )}
    </main>
  )
}
