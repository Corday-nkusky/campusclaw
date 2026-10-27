import { useCallback, useEffect, useMemo, useState } from 'react'
import * as api from '../api'
import { ApiError, describeApiError, type Agent, type Conversation, type Me } from '../api'
import TutorChat from '../components/TutorChat'
import TutorConfig from '../components/TutorConfig'
import type { ToastKind } from '../components/Toast'

/**
 * 解题助手页（第 5 课）。
 *
 * 两种视角合在一页，按角色给不同能力：
 *  - **学生**：只见「对话」——选一个智能体建会话、流式提问、点引用看原文；
 *  - **教师**：多一个「配置」——维护提示词 / 技能 / 智能体，也照样能自己试聊。
 *
 * 权限只做**界面层**的显隐，真正的门在服务端（`PUT /api/prompt`、`POST /api/skills`、
 * `POST /api/agents` 都套了 RequireTeacher）。所以学生即使手工发请求也是 403，
 * 这里藏按钮只是省得他点出一个必然失败的入口。
 */

type Props = {
  identity: Me
  notify: (kind: ToastKind, text: string) => void
  onSessionExpired: () => void
  onModalOpenChange: (open: boolean) => void
}

type Mode = 'chat' | 'config'

export default function TutorPage({ identity, notify, onSessionExpired, onModalOpenChange }: Props) {
  const isTeacher = identity.role === 'teacher'

  const [mode, setMode] = useState<Mode>('chat')
  const [agents, setAgents] = useState<Agent[]>([])
  const [conversations, setConversations] = useState<Conversation[]>([])
  const [loading, setLoading] = useState(true)

  /** 新建会话时选中的智能体（默认取第一个可用的）。 */
  const [pickAgent, setPickAgent] = useState<number | ''>('')
  const [currentId, setCurrentId] = useState<number | null>(null)
  const [creating, setCreating] = useState(false)

  const handle = useCallback(
    (e: unknown): void => {
      if (e instanceof ApiError && e.status === 401) {
        onSessionExpired()
        return
      }
      notify('error', describeApiError(e))
    },
    [notify, onSessionExpired],
  )

  const loadConversations = useCallback(async () => {
    const res = await api.listConversations()
    setConversations(res.conversations)
    return res.conversations
  }, [])

  const loadAll = useCallback(async () => {
    try {
      const [ag, convs] = await Promise.all([api.listAgents(), api.listConversations()])
      setAgents(ag.agents)
      setConversations(convs.conversations)
      const firstAgent = ag.agents[0]
      const firstConv = convs.conversations[0]
      setPickAgent((cur) => (cur === '' && firstAgent ? firstAgent.id : cur))
      setCurrentId((cur) => (cur === null && firstConv ? firstConv.id : cur))
    } catch (e) {
      handle(e)
    } finally {
      setLoading(false)
    }
  }, [handle])

  useEffect(() => {
    void loadAll()
  }, [loadAll])

  const createConv = async (): Promise<void> => {
    if (pickAgent === '' || creating) return
    setCreating(true)
    try {
      const conv = await api.createConversation(pickAgent)
      await loadConversations()
      setCurrentId(conv.id)
    } catch (e) {
      handle(e)
    } finally {
      setCreating(false)
    }
  }

  const current = useMemo(
    () => conversations.find((c) => c.id === currentId) ?? null,
    [conversations, currentId],
  )

  const agentName = useCallback(
    (id: number): string => agents.find((a) => a.id === id)?.name ?? '助手',
    [agents],
  )

  /** 一轮问答结束后刷新列表：会话标题在首轮由服务端按提问改写。 */
  const onTurnDone = useCallback(() => {
    void loadConversations().catch(handle)
  }, [loadConversations, handle])

  const hasAgents = agents.length > 0

  return (
    <main className="page page-wide">
      <div className="tt-head">
        <div>
          <h1>解题助手</h1>
          <p className="muted">
            选定一位助教后提问，回答会先检索本班资料并标出出处；助教只引导思路，不直接给最终答案。
          </p>
        </div>
        {isTeacher && (
          <div className="segmented">
            <button
              type="button"
              className={mode === 'chat' ? 'seg-btn active' : 'seg-btn'}
              onClick={() => {
                setMode('chat')
                // 配置页可能刚建/停用了智能体，切回来时让列表跟上
                void loadAll()
              }}
            >
              对话
            </button>
            <button
              type="button"
              className={mode === 'config' ? 'seg-btn active' : 'seg-btn'}
              onClick={() => setMode('config')}
            >
              配置
            </button>
          </div>
        )}
      </div>

      {loading ? (
        <p className="muted tt-loading">正在载入…</p>
      ) : mode === 'config' ? (
        <TutorConfig notify={notify} onSessionExpired={onSessionExpired} />
      ) : (
        <div className="tt-layout">
          {/* ── 左栏：会话 ── */}
          <aside className="tt-side">
            {!hasAgents ? (
              <div className="placeholder">
                {isTeacher
                  ? '还没有已启用的智能体。到「配置」里建一个，学生才能开始提问。'
                  : '本班还没有开放提问的智能体，请等待教师配置。'}
              </div>
            ) : (
              <>
                <div className="tt-new">
                  <select
                    className="kn-select tt-select"
                    value={pickAgent}
                    onChange={(e) => setPickAgent(e.target.value === '' ? '' : Number(e.target.value))}
                  >
                    {agents.map((a) => (
                      <option key={a.id} value={a.id}>
                        {a.name}
                      </option>
                    ))}
                  </select>
                  <button
                    type="button"
                    className="btn-primary"
                    onClick={() => void createConv()}
                    disabled={creating}
                  >
                    {creating ? '新建中…' : '新建会话'}
                  </button>
                </div>

                {conversations.length === 0 ? (
                  <p className="muted tt-side-empty">还没有会话，选好助教后点「新建会话」。</p>
                ) : (
                  <ul className="tt-convs">
                    {conversations.map((c) => (
                      <li key={c.id}>
                        <button
                          type="button"
                          className={c.id === currentId ? 'tt-conv active' : 'tt-conv'}
                          onClick={() => setCurrentId(c.id)}
                        >
                          <span className="tt-conv-title">{c.title}</span>
                          <span className="tt-conv-agent muted">{agentName(c.agent_id)}</span>
                        </button>
                      </li>
                    ))}
                  </ul>
                )}
              </>
            )}
          </aside>

          {/* ── 右栏：对话 ── */}
          <section className="tt-main">
            <TutorChat
              conversation={current}
              agentLabel={current ? agentName(current.agent_id) : '助手'}
              notify={notify}
              onSessionExpired={onSessionExpired}
              onModalOpenChange={onModalOpenChange}
              onTurnDone={onTurnDone}
            />
          </section>
        </div>
      )}
    </main>
  )
}
