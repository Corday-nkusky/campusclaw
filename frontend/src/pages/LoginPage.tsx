import { useState, type FormEvent } from 'react'
import * as api from '../api'
import { ApiError } from '../api'

interface Props {
  /** 首屏探测身份时的提示（例如后端不可达），可为空。 */
  notice: string | null
  onLoggedIn: () => void | Promise<void>
}

/**
 * 把抛出的异常翻译成给人看的一句话。
 *
 * 注意 401 的文案：服务端对「账号不存在 / 口令错误 / 被限流」返回**完全同形**的
 * 响应（spec.md R1.2），所以前端在结构上就拿不到区分依据 —— 这里也不去猜。
 * 想区分只能改后端，而那正是规约禁止的（会变成账号枚举的依据）。
 */
// 刻意**不带** cc_ 前缀：cc_* 是凭证命名空间，登出时要求整套清空（browser-verify B16）。
// 这是登录表单的输入偏好，不是凭证，混进那个命名空间会让"凭证是否清干净"判断失真。
const REMEMBER_KEY = 'login_pref'

/**
 * 「记住账号口令」：把登录表单的输入值存本机 localStorage，省掉每次手抄。
 *
 * 为什么连口令一起存：本仓是本机开发环境，种子账号口令本来就明文写在 .env 里，
 * 每次跑验收、换浏览器都要再抄一遍，成本远大于收益。
 * 生产形态**不该**这样（口令不该落 localStorage）—— 上线应换浏览器密码管理器
 * 或 SSO。这里的取舍只为本地开发省事，不是给线上做法背书。
 */
function loadSaved(): { username: string; password: string } | null {
  try {
    const raw = localStorage.getItem(REMEMBER_KEY)
    if (!raw) return null
    const v = JSON.parse(raw) as { username?: unknown; password?: unknown }
    if (typeof v?.username === 'string' && typeof v?.password === 'string') {
      return { username: v.username, password: v.password }
    }
  } catch {
    // 存档坏了就当没有：不能让一段坏 JSON 把登录页卡死
  }
  return null
}

function describeError(err: unknown): string {
  if (err instanceof ApiError) {
    if (err.status === 401) {
      return '账号或口令不正确。'
    }
    if (err.status === 0) {
      return err.message
    }
    return '登录失败，请稍后再试。'
  }
  return '登录失败：未知错误'
}

export default function LoginPage({ notice, onLoggedIn }: Props) {
  const [saved] = useState(loadSaved)
  const [username, setUsername] = useState(saved?.username ?? '')
  const [password, setPassword] = useState(saved?.password ?? '')
  const [remember, setRemember] = useState(true)
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function handleSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault()
    if (pending) return
    setError(null)
    setPending(true)
    try {
      await api.login(username.trim(), password)
      if (remember) {
        localStorage.setItem(
          REMEMBER_KEY,
          JSON.stringify({ username: username.trim(), password }),
        )
      } else {
        localStorage.removeItem(REMEMBER_KEY)
      }
      // 登录响应只含 access + refresh，不含身份；凭证已由 api.login 存入 localStorage。
      // 身份要另外问 /api/me —— 同一个来源，两种界面不打架。
      await onLoggedIn()
    } catch (err) {
      setError(describeError(err))
    } finally {
      setPending(false)
    }
  }

  return (
    <div className="center-screen">
      <div className="card">
        <div className="brand">
          <span className="brand-mark" aria-hidden="true" />
          <span className="brand-name">CampusClaw</span>
        </div>
        <h1>登录</h1>
        <p className="subtitle">班级教学材料与知识库</p>

        {notice && (
          <p className="alert alert-notice" role="status">
            {notice}
          </p>
        )}
        {error && (
          <p className="alert alert-danger" role="alert">
            {error}
          </p>
        )}

        <form onSubmit={handleSubmit} noValidate>
          <div className="field">
            <label htmlFor="username">账号</label>
            <input
              id="username"
              name="username"
              type="text"
              autoComplete="username"
              autoFocus
              spellCheck={false}
              disabled={pending}
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              placeholder="请输入账号"
            />
          </div>

          <div className="field">
            <label htmlFor="password">口令</label>
            <input
              id="password"
              name="password"
              type="password"
              autoComplete="current-password"
              disabled={pending}
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
          </div>

          <label className="remember-row">
            <input
              id="remember"
              name="remember"
              type="checkbox"
              checked={remember}
              disabled={pending}
              onChange={(e) => setRemember(e.target.checked)}
            />
            <span>记住账号和口令（仅存在本机浏览器）</span>
          </label>

          <button className="btn-primary" type="submit" disabled={pending}>
            {pending ? '正在登录…' : '登录'}
          </button>
        </form>

        <p className="footnote">账号由任课教师统一发放，登录遇到问题请联系教师。</p>
      </div>
    </div>
  )
}
