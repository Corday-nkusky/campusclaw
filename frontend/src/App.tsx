import { useCallback, useEffect, useMemo, useState } from 'react'
import * as api from './api'
import { ApiError, type Me } from './api'
import LoginPage from './pages/LoginPage'
import HomePage from './pages/HomePage'
import MaterialsPage from './pages/MaterialsPage'
import KnowledgePage from './pages/KnowledgePage'
import TutorPage from './pages/TutorPage'
import CommandPalette, { type Command } from './components/CommandPalette'
import { ToastHost, useToasts } from './components/Toast'

/**
 * 身份状态机。三种相位互斥，避免出现「已登录但身份为空」这种中间态。
 *
 * 关键约定（spec.md R9.1，add-refresh-token）：
 * `identity` 的**唯一**来源是 `GET /api/me`。localStorage 只保存凭证
 * （access / refresh / access 过期时刻），整个前端没有一处把本地存储的值
 * 当身份或权限用 —— 所以「篡改 localStorage 的 role」这件事在结构上无从生效
 * （本地根本不存 role）。access 过期由 api.ts 静默刷新，用户无感。
 */
type Phase = 'loading' | 'anonymous' | 'authenticated'

/** 登录后的页面。材料页与工作台平级，不是弹层。 */
type Tab = 'workbench' | 'materials' | 'knowledge' | 'tutor'
type View = 'list' | 'grid'

const ROLE_LABEL: Record<string, string> = {
  teacher: '教师',
  student: '学生',
}

export default function App() {
  const [phase, setPhase] = useState<Phase>('loading')
  const [identity, setIdentity] = useState<Me | null>(null)
  const [bootNotice, setBootNotice] = useState<string | null>(null)
  const [tab, setTab] = useState<Tab>('workbench')
  const [view, setView] = useState<View>('list')
  const [paletteOpen, setPaletteOpen] = useState(false)
  /**
   * 单一活动模态：上传对话框、原文弹层这类「非命令面板模态」的开合由此上报。
   * 语义是「同一时刻只能有一个模态」——它们开着的时候命令面板既不能开（快捷键与
   * 按钮都拦），已在开着的也要让位（关掉），否则两个模态互相叠压、Esc 只关最上层。
   */
  const [modalOpen, setModalOpen] = useState(false)
  const handleModalOpenChange = useCallback((open: boolean) => setModalOpen(open), [])
  /** 由命令面板触发材料页动作的两个信号（递增即触发一次）。 */
  const [reloadToken, setReloadToken] = useState(0)
  const [uploadSignal, setUploadSignal] = useState(0)

  const { toasts, notify, dismiss } = useToasts()

  /** 向服务端问一次「我是谁」。401 = 凭证无效（静默刷新也已失败），清凭证回登录页。 */
  const refreshIdentity = useCallback(async () => {
    try {
      const id = await api.me()
      setIdentity(id)
      setBootNotice(null)
      setTab('workbench')
      setPhase('authenticated')
    } catch (e) {
      setIdentity(null)
      if (e instanceof ApiError && e.status === 401) {
        api.dropToken() // access 与 refresh 都失效：清掉，别让死凭证留在本地
      }
      setPhase('anonymous')
      // 401 是「未登录」的正常分支，不该报错；其它状态码说明后端有问题，要如实说
      setBootNotice(e instanceof ApiError && e.status !== 401 ? '服务暂时不可用，请稍后再试。' : null)
    }
  }, [])

  // 首屏与每次刷新都重新问服务端 —— 刷新后身份照样以服务端为准
  useEffect(() => {
    void refreshIdentity()
  }, [refreshIdentity])

  const enterAnonymous = useCallback((notice: string | null) => {
    api.dropToken() // 登出与 401 两条路都要保证本地不留死凭证（R9.3）
    setIdentity(null)
    setBootNotice(notice)
    setTab('workbench')
    setPaletteOpen(false)
    setPhase('anonymous')
  }, [])

  /**
   * 会话失效的统一出口。**必须是稳定引用**（useCallback）：
   * 它会被当作 prop 传进各页面，而页面里的副作用（如会话历史加载）会把它写进
   * 依赖数组；每次渲染都换新身份的话，父层任何一次重渲染都会让那些副作用重跑。
   */
  const handleSessionExpired = useCallback(
    () => enterAnonymous('会话已失效，请重新登录。'),
    [enterAnonymous],
  )

  const handleLogout = useCallback(async () => {
    try {
      await api.logout()
    } catch {
      // 服务端登出失败也要断开本地界面：本地状态不是权限来源，
      // 但停在一个已登录样子的界面上，更容易让人误判会话还在
    }
    enterAnonymous(null)
  }, [enterAnonymous])

  // 别的模态打开的瞬间，命令面板必须让位（单一活动模态）
  useEffect(() => {
    if (modalOpen) setPaletteOpen(false)
  }, [modalOpen])

  // ⌘/Ctrl+K 开关命令面板（R10.1）。模态互斥：有别的模态开着时快捷键不抢焦点
  useEffect(() => {
    function onKeyDown(e: KeyboardEvent) {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
        if (modalOpen) return
        e.preventDefault()
        setPaletteOpen((open) => !open)
      }
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [modalOpen])

  const isTeacher = identity?.role === 'teacher'

  /**
   * 命令清单。可用性只在**一个地方**决定，面板本身不做业务判断。
   *
   * 注意「上传材料」命令：它只是把入口整得更明显。
   * 学生看不到它，但即使手工构造请求，服务端 RequireTeacher 仍返回 403（R3.2）。
   */
  const commands = useMemo<Command[]>(() => {
    const list: Command[] = [
      {
        id: 'goto-materials',
        label: '转到「材料」页',
        hint: '本班材料列表',
        run: () => setTab('materials'),
      },
      {
        id: 'goto-workbench',
        label: '转到「工作台」页',
        hint: '账号信息',
        run: () => setTab('workbench'),
      },
      {
        id: 'goto-knowledge',
        label: '转到「知识检索」页',
        hint: '检索与问答',
        run: () => setTab('knowledge'),
      },
      {
        id: 'goto-tutor',
        label: '转到「解题助手」页',
        hint: '智能体对话',
        run: () => setTab('tutor'),
      },
      {
        id: 'view-list',
        label: '材料视图：列表',
        run: () => {
          setView('list')
          setTab('materials')
        },
      },
      {
        id: 'view-grid',
        label: '材料视图：网格',
        run: () => {
          setView('grid')
          setTab('materials')
        },
      },
      {
        id: 'reload-materials',
        label: '刷新材料列表',
        run: () => {
          setTab('materials')
          setReloadToken((n) => n + 1)
        },
      },
      {
        id: 'recheck-identity',
        label: '刷新登录状态',
        run: async () => {
          try {
            const fresh = await api.me()
            setIdentity(fresh)
            notify('success', `已确认登录：${fresh.username}（${ROLE_LABEL[fresh.role] ?? fresh.role}）`)
          } catch (e) {
            if (e instanceof ApiError && e.status === 401) {
              enterAnonymous('会话已失效，请重新登录。')
              return
            }
            notify('error', `刷新失败：${api.describeApiError(e)}`)
          }
        },
      },
    ]

    if (isTeacher) {
      list.push({
        id: 'upload',
        label: '上传材料',
        hint: '教师专属',
        run: () => {
          setTab('materials')
          setUploadSignal((n) => n + 1)
        },
      })
    }

    list.push({
      id: 'logout',
      label: '退出登录',
      run: () => handleLogout(),
    })

    return list
  }, [enterAnonymous, handleLogout, isTeacher, notify])

  if (phase === 'loading') {
    return (
      <div className="center-screen">
        <span className="muted">正在确认登录状态…</span>
      </div>
    )
  }

  if (phase !== 'authenticated' || !identity) {
    return <LoginPage notice={bootNotice} onLoggedIn={refreshIdentity} />
  }

  return (
    <>
      <header className="app-header">
        <div className="brand">
          <span className="brand-mark" aria-hidden="true" />
          <span className="brand-name">CampusClaw</span>
        </div>

        <nav className="app-nav" aria-label="主导航">
          <button
            type="button"
            className={tab === 'workbench' ? 'nav-tab active' : 'nav-tab'}
            aria-current={tab === 'workbench' ? 'page' : undefined}
            onClick={() => setTab('workbench')}
          >
            工作台
          </button>
          <button
            type="button"
            className={tab === 'materials' ? 'nav-tab active' : 'nav-tab'}
            aria-current={tab === 'materials' ? 'page' : undefined}
            onClick={() => setTab('materials')}
          >
            材料
          </button>
          <button
            type="button"
            className={tab === 'knowledge' ? 'nav-tab active' : 'nav-tab'}
            aria-current={tab === 'knowledge' ? 'page' : undefined}
            onClick={() => setTab('knowledge')}
          >
            知识检索
          </button>
          <button
            type="button"
            className={tab === 'tutor' ? 'nav-tab active' : 'nav-tab'}
            aria-current={tab === 'tutor' ? 'page' : undefined}
            onClick={() => setTab('tutor')}
          >
            解题助手
          </button>
        </nav>

        <div className="spacer" />

        <button
          type="button"
          className="btn-palette"
          onClick={() => setPaletteOpen(true)}
          disabled={modalOpen}
          title={modalOpen ? '请先关闭当前弹层' : '打开命令面板'}
        >
          命令 <kbd>⌘K</kbd>
        </button>

        <div className="who">
          <span className="name">{identity.username}</span>
          <span className={isTeacher ? 'badge badge-teacher' : 'badge badge-student'}>
            {ROLE_LABEL[identity.role] ?? identity.role}
          </span>
          <span className="muted">{identity.class_name}</span>
        </div>

        <button type="button" className="btn-ghost" onClick={() => void handleLogout()}>
          退出登录
        </button>
      </header>

      {tab === 'workbench' ? (
        <HomePage
          identity={identity}
          onIdentityChange={setIdentity}
          onSessionExpired={handleSessionExpired}
          onOpenMaterials={() => setTab('materials')}
          onUpload={() => {
            setTab('materials')
            setUploadSignal((s) => s + 1)
          }}
        />
      ) : tab === 'materials' ? (
        <MaterialsPage
          identity={identity}
          view={view}
          onViewChange={setView}
          reloadToken={reloadToken}
          uploadSignal={uploadSignal}
          notify={notify}
          onSessionExpired={handleSessionExpired}
          onModalOpenChange={handleModalOpenChange}
        />
      ) : tab === 'knowledge' ? (
        <KnowledgePage onModalOpenChange={handleModalOpenChange} />
      ) : (
        <TutorPage
          identity={identity}
          notify={notify}
          onSessionExpired={handleSessionExpired}
          onModalOpenChange={handleModalOpenChange}
        />
      )}

      {paletteOpen && <CommandPalette commands={commands} onClose={() => setPaletteOpen(false)} />}

      <ToastHost toasts={toasts} onDismiss={dismiss} />
    </>
  )
}
