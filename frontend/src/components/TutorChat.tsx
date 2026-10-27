import { useEffect, useRef, useState } from 'react'
import * as api from '../api'
import {
  ApiError,
  describeApiError,
  type Conversation,
  type SearchHit,
} from '../api'
import { renderMarkdown } from '../lib/markdown'
import type { ToastKind } from './Toast'

/**
 * 解题助手 · 对话区（第 5 课核心链路的前端一面）。
 *
 * 三条只属于本课的呈现约定：
 *  1. **逐段渲染**：SSE 的每个 `delta` 立刻追加到气泡里，不等整段回答完成；
 *  2. **引用可点**：`meta.citations` 渲染成 [n] 芯片，点击拉材料详情并按偏移高亮
 *     （与知识检索页同一条溯源闭环，复用 `highlightSlice` + 同一套弹层样式）；
 *  3. **status 不骗人**：对话链路的 `meta.status` 恒为 `ok` —— 有没有依据是看
 *     `citations` 空不空。所以「无依据」提示只能由 citations 为空推出，不能读 status。
 */

type Props = {
  conversation: Conversation | null
  /** 当前会话所选智能体的名字（仅用于标题展示）。 */
  agentLabel: string
  notify: (kind: ToastKind, text: string) => void
  onSessionExpired: () => void
  onModalOpenChange: (open: boolean) => void
  /** 一轮问答落库完成（会话标题可能变了），让外层刷新会话列表。 */
  onTurnDone: () => void
}

/** 气泡。流式期间 `streaming` 为真，`content` 随每个 delta 增长。 */
type Bubble = {
  key: string
  role: 'user' | 'assistant'
  content: string
  citations: SearchHit[]
  skillName: string
  streaming: boolean
  /** 流开始后失败时的提示（HTTP 仍 200，是 `event: error`）。 */
  failed: string | null
}

let seq = 0
const nextKey = (): string => `b${++seq}`

export default function TutorChat({
  conversation,
  agentLabel,
  notify,
  onSessionExpired,
  onModalOpenChange,
  onTurnDone,
}: Props) {
  const [bubbles, setBubbles] = useState<Bubble[]>([])
  const [loadingHistory, setLoadingHistory] = useState(false)
  const [input, setInput] = useState('')
  const [sending, setSending] = useState(false)
  const abortRef = useRef<AbortController | null>(null)
  const scrollRef = useRef<HTMLDivElement | null>(null)

  /** 原文弹层（点击引用芯片打开）。 */
  const [viewing, setViewing] = useState<{
    title: string
    body: string
    start: number
    end: number
  } | null>(null)

  const convId = conversation?.id ?? null

  /**
   * 回调放 ref：历史加载的 effect **只**依赖 `convId`。
   *
   * 这是踩过坑之后的写法：`notify` / `onSessionExpired` 这类 prop 在父层很容易
   * 每次渲染都换新身份（App 里传的就是内联箭头函数）。一旦把它们写进依赖数组，
   * 父层任何一次重渲染都会让 effect 重跑、**从服务端重拉历史覆盖本地状态** ——
   * 而此时正在流式生成的气泡还没落库，于是回答被冲掉、只剩一条 user 气泡。
   */
  const notifyRef = useRef(notify)
  notifyRef.current = notify
  const expiredRef = useRef(onSessionExpired)
  expiredRef.current = onSessionExpired
  /** 生成中标记：流式回答期间禁止用历史覆盖本地气泡。 */
  const inFlightRef = useRef(false)

  // 切换会话 → 载入历史
  useEffect(() => {
    if (convId === null) {
      setBubbles([])
      return
    }
    // 正在流式生成时不要重拉历史：服务端此刻还没有 assistant 那条
    if (inFlightRef.current) return
    let cancelled = false
    setLoadingHistory(true)
    api
      .getConversation(convId)
      .then((res) => {
        if (cancelled) return
        setBubbles(
          res.messages.map((m) => ({
            key: `m${m.id}`,
            role: m.role,
            content: m.content,
            citations: m.citations ?? [],
            skillName: m.skill_name ?? '',
            streaming: false,
            failed: null,
          })),
        )
      })
      .catch((e) => {
        if (cancelled) return
        if (e instanceof ApiError && e.status === 401) {
          expiredRef.current()
          return
        }
        notifyRef.current('error', describeApiError(e))
      })
      .finally(() => {
        if (!cancelled) setLoadingHistory(false)
      })
    return () => {
      cancelled = true
    }
  }, [convId])

  // 弹层开合同态给外层（单一活动模态：命令面板必须让位）
  useEffect(() => {
    onModalOpenChange(viewing !== null)
  }, [viewing, onModalOpenChange])

  useEffect(() => {
    if (!viewing) return
    const onKey = (e: KeyboardEvent): void => {
      if (e.key === 'Escape') setViewing(null)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [viewing])

  // 内容增长时贴底
  useEffect(() => {
    const el = scrollRef.current
    if (el) el.scrollTop = el.scrollHeight
  }, [bubbles])

  // 卸载时中止在飞的流
  useEffect(() => () => abortRef.current?.abort(), [])

  const send = async (): Promise<void> => {
    const text = input.trim()
    if (!text || sending || convId === null) return
    setInput('')
    setSending(true)

    const userBubble: Bubble = {
      key: nextKey(),
      role: 'user',
      content: text,
      citations: [],
      skillName: '',
      streaming: false,
      failed: null,
    }
    const botKey = nextKey()
    const botBubble: Bubble = {
      key: botKey,
      role: 'assistant',
      content: '',
      citations: [],
      skillName: '',
      streaming: true,
      failed: null,
    }
    setBubbles((prev) => [...prev, userBubble, botBubble])
    inFlightRef.current = true

    const patch = (up: Partial<Bubble>): void =>
      setBubbles((prev) => prev.map((b) => (b.key === botKey ? { ...b, ...up } : b)))

    const ctrl = new AbortController()
    abortRef.current = ctrl
    let acc = ''
    let streamErrored: string | null = null

    try {
      await api.streamAsk(
        convId,
        text,
        {
          onMeta: (meta) => patch({ citations: meta.citations ?? [], skillName: meta.skill ?? '' }),
          onDelta: (t) => {
            acc += t
            patch({ content: acc })
          },
          onDone: () => patch({ streaming: false }),
          onStreamError: (msg) => {
            streamErrored = msg
            patch({ streaming: false, failed: msg })
          },
        },
        ctrl.signal,
      )
      // 流正常走完但没收到 done（连接被掐）：如实说，不假装成功
      if (!streamErrored && !acc) {
        patch({ streaming: false, failed: '没有收到回答内容。' })
      }
      onTurnDone()
    } catch (e) {
      if (e instanceof DOMException && e.name === 'AbortError') {
        patch({ streaming: false, failed: '已停止生成。' })
      } else if (e instanceof ApiError && e.status === 401) {
        onSessionExpired()
      } else {
        const msg = describeApiError(e)
        patch({ streaming: false, failed: msg })
        notify('error', msg)
      }
    } finally {
      inFlightRef.current = false
      abortRef.current = null
      setSending(false)
    }
  }

  const stop = (): void => abortRef.current?.abort()

  const openCitation = async (h: SearchHit): Promise<void> => {
    try {
      const detail = await api.material(h.material_id)
      setViewing({ title: detail.title, body: detail.body, start: h.start_offset, end: h.end_offset })
    } catch (e) {
      notify('error', describeApiError(e))
    }
  }

  if (conversation === null) {
    return (
      <div className="tt-chat tt-chat-empty placeholder">
        选择左侧一个会话，或新建会话后开始提问。
      </div>
    )
  }

  return (
    <>
      <div className="tt-chat">
        <div className="tt-chat-scroll" ref={scrollRef}>
          {loadingHistory && <p className="muted tt-loading">正在载入会话…</p>}
          {!loadingHistory && bubbles.length === 0 && (
            <p className="muted tt-loading">
              已在会话《{conversation.title}》中，向「{agentLabel}」提问吧。
            </p>
          )}

          {bubbles.map((b) => (
            <div key={b.key} className={b.role === 'user' ? 'tt-msg tt-msg-user' : 'tt-msg tt-msg-bot'}>
              <div className="tt-msg-role">{b.role === 'user' ? '我' : agentLabel}</div>
              <div className="tt-msg-body">
                {b.role === 'assistant' ? (
                  b.content ? (
                    <div
                      className="markdown-body"
                      // markdown-it 默认 html:false，原始 HTML 被转义（见 lib/markdown.ts）
                      dangerouslySetInnerHTML={{ __html: renderMarkdown(b.content) }}
                    />
                  ) : (
                    <span className="tt-caret" aria-label="正在生成" />
                  )
                ) : (
                  <span className="tt-plain">{b.content}</span>
                )}

                {b.streaming && b.content && <span className="tt-caret" aria-label="正在生成" />}

                {b.failed && (
                  <div className="alert alert-danger tt-inline-alert" role="alert">
                    {b.failed}
                  </div>
                )}

                {b.role === 'assistant' && !b.streaming && (
                  <div className="tt-cites">
                    {b.citations.length === 0 ? (
                      <span className="muted tt-cites-empty">未依据本班资料作答</span>
                    ) : (
                      <>
                        <span className="muted">出处：</span>
                        {b.citations.map((c, i) => (
                          <button
                            key={`${c.chunk_id}-${i}`}
                            type="button"
                            className="tt-cite"
                            title={`《${c.material_title}》第 ${c.chunk_index + 1} 段`}
                            onClick={() => void openCitation(c)}
                          >
                            [{i + 1}] 《{c.material_title}》
                          </button>
                        ))}
                      </>
                    )}
                    {b.skillName && <span className="tt-skill">技能：{b.skillName}</span>}
                  </div>
                )}
              </div>
            </div>
          ))}
        </div>

        <form
          className="tt-composer"
          onSubmit={(e) => {
            e.preventDefault()
            void send()
          }}
        >
          <textarea
            className="input tt-input"
            rows={2}
            value={input}
            onChange={(e) => setInput(e.target.value)}
            onKeyDown={(e) => {
              // Enter 发送、Shift+Enter 换行（与常见聊天一致）
              if (e.key === 'Enter' && !e.shiftKey) {
                e.preventDefault()
                void send()
              }
            }}
            placeholder="描述你的题目或困惑，例如：这道题为什么先移项？"
            disabled={sending}
          />
          {sending ? (
            <button type="button" className="btn-secondary" onClick={stop}>
              停止
            </button>
          ) : (
            <button type="submit" className="btn-primary" disabled={!input.trim()}>
              发送
            </button>
          )}
        </form>
      </div>

      {viewing && (
        <div className="kn-backdrop" role="dialog" aria-modal="true" onClick={() => setViewing(null)}>
          <div className="kn-modal" onClick={(e) => e.stopPropagation()}>
            <div className="kn-modal-head">
              <span className="kn-modal-bar" aria-hidden="true" />
              <div className="kn-modal-heading">
                <strong className="kn-modal-title">{viewing.title}</strong>
              </div>
              <button type="button" className="btn-ghost" onClick={() => setViewing(null)}>
                关闭
              </button>
            </div>
            <div className="kn-modal-body">
              {(() => {
                const { before, mark, after } = api.highlightSlice(viewing.body, viewing.start, viewing.end)
                return (
                  <>
                    {before}
                    <mark className="kn-mark">{mark}</mark>
                    {after}
                  </>
                )
              })()}
            </div>
          </div>
        </div>
      )}
    </>
  )
}
