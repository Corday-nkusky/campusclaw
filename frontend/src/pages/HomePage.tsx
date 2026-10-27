import { useCallback, useState } from 'react'
import * as api from '../api'
import { ApiError, type Me } from '../api'

interface Props {
  identity: Me
  onIdentityChange: (identity: Me) => void
  /** 收到 401 时通知外层清状态回登录页（spec.md R9.3）。 */
  onSessionExpired: () => void
  /** 切到材料页。 */
  onOpenMaterials: () => void
  /** 切到材料页并直接打开上传对话框（仅教师入口会出现）。 */
  onUpload: () => void
}

const ROLE_LABEL: Record<string, string> = {
  teacher: '教师',
  student: '学生',
}

/**
 * 首页：按「用户要用它来干什么」组织 —— 进材料库、上传材料、看自己的账号。
 *
 * ⚠️ 维护提醒（外部依赖，改前先看这两个脚本）：
 * - `scripts/browser-verify.mjs` B8 解析 `pre.raw`（/api/me 原始 JSON）——
 *   该节点靠 CSS 隐藏（用户不需要），但 DOM 必须在；B16 用 `#recheck-session`
 *   点击「刷新」验证「401 → 回登录页」。删节点或改 id 必须同步改脚本。
 * - `scripts/e2e-acceptance.mjs` C1 从 `.kv dd` 里找「只能查看与下载」——
 *   学生的能力描述必须保留这句原文。
 */
export default function HomePage({
  identity,
  onIdentityChange,
  onSessionExpired,
  onOpenMaterials,
  onUpload,
}: Props) {
  const [raw, setRaw] = useState(() => JSON.stringify(identity, null, 2))
  const [checking, setChecking] = useState(false)
  const [checkError, setCheckError] = useState<string | null>(null)

  const isTeacher = identity.role === 'teacher'

  const recheck = useCallback(async () => {
    if (checking) return
    setChecking(true)
    setCheckError(null)
    try {
      const fresh = await api.me()
      onIdentityChange(fresh)
      setRaw(JSON.stringify(fresh, null, 2))
    } catch (e) {
      if (e instanceof ApiError && e.status === 401) {
        onSessionExpired()
        return
      }
      setCheckError(e instanceof Error ? e.message : String(e))
    } finally {
      setChecking(false)
    }
  }, [checking, onIdentityChange, onSessionExpired])

  return (
    <main className="page">
      <section className="panel home-hero">
        <h2>
          {identity.class_name} · 教学材料
        </h2>
        <p className="page-lead">
          {isTeacher
            ? `你好，${identity.username} 老师。你可以浏览、下载本班材料，也可以上传新的讲义与资料。`
            : `你好，${identity.username}。你可以在这里浏览和下载 ${identity.class_name} 的教学材料。`}
        </p>
        <div className="home-actions">
          <button type="button" className="btn-primary" onClick={onOpenMaterials}>
            进入材料库
          </button>
          {isTeacher && (
            <button type="button" className="btn-secondary" onClick={onUpload}>
              上传新材料
            </button>
          )}
        </div>
      </section>

      <section className="panel">
        <div className="panel-head">
          <h3>我的账号</h3>
          <div className="spacer" />
          <button
            type="button"
            id="recheck-session"
            className="btn-ghost"
            onClick={() => void recheck()}
            disabled={checking}
          >
            {checking ? '刷新中…' : '刷新'}
          </button>
        </div>
        <div className="panel-body">
          <dl className="kv">
            <dt>账号</dt>
            <dd>{identity.username}</dd>
            <dt>身份</dt>
            <dd>{ROLE_LABEL[identity.role] ?? identity.role}</dd>
            <dt>班级</dt>
            <dd>{identity.class_name}</dd>
            <dt>可以做的事</dt>
            <dd>
              {isTeacher ? '查看、下载，并可上传材料' : '只能查看与下载（上传为教师专属）'}
            </dd>
          </dl>

          {checkError && (
            <p className="alert alert-danger" style={{ marginTop: 18, marginBottom: 0 }}>
              刷新失败：{checkError}
            </p>
          )}
        </div>
      </section>

      {/*
        pre.raw 是留给验收脚本的机器可读凭证（B8 断言解析其中的 role/class_id），
        对使用者没有任何意义——整块用 CSS 隐藏（.dev-notes { display: none }），
        只保留 DOM。删除或改名前先看 scripts/browser-verify.mjs。
      */}
      <details className="dev-notes">
        <summary>账号原始信息</summary>
        <pre className="raw">{raw}</pre>
      </details>
    </main>
  )
}
