import { useEffect, useState } from 'react'
import * as api from '../api'
import { describeApiError, type SearchHit, type SearchMode } from '../api'

/**
 * 知识检索页（add-class-scoped-retrieval tasks 9.1–9.3；呈现层由 polish-knowledge-ui 重构）：
 * 检索（三模式）+ 八字段溯源列表 + 点击回原文高亮 + 问答区与 [n] 出处。
 * 样式全部来自 styles.css 的 kn- 类族，本文件不含内联样式
 * （唯一例外：分值条宽度是数据驱动的动态值，见 D3）。
 */

type Props = {
  /** 原文弹层是模态：开合同态给外层，与命令面板互斥（单一活动模态）。 */
  onModalOpenChange: (open: boolean) => void
}

/** D4：查询词在 snippet 里的客户端高亮——按空白拆词、不区分大小写的字面匹配，
 *  匹配不到（如纯语义召回）就不高亮。纯呈现层：不参与召回/排序，不影响后端。 */
function withQueryMarks(text: string, query: string) {
  const terms = [...new Set(query.trim().split(/\s+/).filter(Boolean))]
  if (terms.length === 0) return text
  const pattern = terms.map((t) => t.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')).join('|')
  return text.split(new RegExp(`(${pattern})`, 'gi')).map((part, i) =>
    i % 2 === 1 ? (
      <mark key={i} className="kn-mark">
        {part}
      </mark>
    ) : (
      <span key={i}>{part}</span>
    ),
  )
}

export default function KnowledgePage({ onModalOpenChange }: Props) {
  const [query, setQuery] = useState('')
  const [mode, setMode] = useState<SearchMode>('hybrid')
  const [hits, setHits] = useState<SearchHit[] | null>(null)
  const [searching, setSearching] = useState(false)
  const [searchErr, setSearchErr] = useState<string | null>(null)
  /** 提交检索那一刻的查询词：高亮跟着结果走，不受输入框后续编辑影响 */
  const [lastQuery, setLastQuery] = useState('')

  const [question, setQuestion] = useState('')
  const [answer, setAnswer] = useState<string | null>(null)
  const [citations, setCitations] = useState<SearchHit[]>([])
  const [asking, setAsking] = useState(false)
  const [askErr, setAskErr] = useState<string | null>(null)

  /** 回原文弹层的状态 */
  const [viewing, setViewing] = useState<{
    title: string
    body: string
    start: number
    end: number
  } | null>(null)

  /**
   * 命中条宽按「本组最高分」归一化。三种检索方式的原始分不可比：
   * vector 是余弦相似度（0–1），keyword 是 MySQL 全文分（可 >1），hybrid 是 RRF 分（~0.03）。
   * 直接把原始分当百分比显示会得到「217%」或「3%」这类误导数字，因此只呈现相对长短。
   */
  const maxHitScore =
    hits && hits.length > 0 ? Math.max(...hits.map((h) => h.score)) : 1

  const doSearch = async () => {
    const q = query.trim()
    if (!q || searching) return
    setSearching(true)
    setSearchErr(null)
    setLastQuery(q)
    try {
      const res = await api.searchKnowledge(q, mode)
      setHits(res.hits)
    } catch (e) {
      setHits(null)
      setSearchErr(describeApiError(e))
    } finally {
      setSearching(false)
    }
  }

  const doAsk = async () => {
    const q = question.trim()
    if (!q || asking) return
    setAsking(true)
    setAskErr(null)
    setAnswer(null)
    setCitations([])
    try {
      const res = await api.askKnowledge(q)
      setAnswer(res.answer)
      setCitations(res.citations)
    } catch (e) {
      setAskErr(describeApiError(e))
    } finally {
      setAsking(false)
    }
  }

  /** 点击命中 → 拉详情 → 按偏移高亮（R3.3 溯源闭环） */
  const openHit = async (h: SearchHit) => {
    try {
      const detail = await api.material(h.material_id)
      setViewing({ title: detail.title, body: detail.body, start: h.start_offset, end: h.end_offset })
    } catch (e) {
      setSearchErr(describeApiError(e))
    }
  }

  /** D6：弹层打开期间监听 ESC 关闭 */
  useEffect(() => {
    if (!viewing) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setViewing(null)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [viewing])

  // 原文弹层的开合同态给外层：命令面板与它同时只能开一个（单一活动模态）
  useEffect(() => {
    onModalOpenChange(viewing !== null)
  }, [viewing, onModalOpenChange])

  const citedNums = (text: string): number[] => {
    const out: number[] = []
    for (const m of text.matchAll(/\[(\d{1,3})\]/g)) {
      const n = Number(m[1])
      if (n >= 1 && n <= citations.length && !out.includes(n)) out.push(n)
    }
    return out
  }

  return (
    <main className="page">
      <h1>知识检索</h1>
      <p className="muted">
        在本班资料中检索，每条结果都标明出处，点击可查看原文。
      </p>

      {/* ── 检索区（对齐材料页 .search-bar 观感）── */}
      <form
        className="search-bar kn-form"
        onSubmit={(e) => {
          e.preventDefault()
          void doSearch()
        }}
      >
        <input
          className="input"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder="输入关键词或语义问句，如：细胞周期"
        />
        <select className="kn-select" value={mode} onChange={(e) => setMode(e.target.value as SearchMode)}>
          <option value="hybrid">综合（默认）</option>
          <option value="keyword">关键词</option>
          <option value="vector">语义</option>
        </select>
        <button type="submit" className="btn-primary" disabled={searching || !query.trim()}>
          {searching ? '检索中…' : '检索'}
        </button>
      </form>

      {searchErr && (
        <div className="alert alert-danger" role="alert">
          {searchErr}
        </div>
      )}

      {/* 三态：加载 → 骨架卡；结果 → 命中卡列表；空 → 引导文案（D5） */}
      {searching && (
        <div className="kn-hits" aria-hidden="true">
          {[0, 1].map((i) => (
            <div key={i} className="kn-sk-card">
              <div className="kn-skeleton kn-sk-title" />
              <div className="kn-skeleton kn-sk-line" />
              <div className="kn-skeleton kn-sk-line kn-sk-short" />
            </div>
          ))}
        </div>
      )}

      {!searching && hits !== null && hits.length > 0 && (
        <section>
          <p className="kn-count muted">
            命中 {hits.length} 条
          </p>
          <ul className="kn-hits">
            {hits.map((h) => (
              <li
                key={h.chunk_id}
                className="kn-hit"
                role="button"
                tabIndex={0}
                onClick={() => void openHit(h)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter' || e.key === ' ') {
                    e.preventDefault()
                    void openHit(h)
                  }
                }}
                title="点击查看原文"
              >
                <div className="kn-hit-head">
                  <strong className="kn-hit-title">{h.material_title}</strong>
                </div>
                {/* 相对呈现：条宽 = score ÷ 本组最高分（D3）。动态宽度是数据，进不了类表。 */}
                <div className="kn-scorebar" aria-hidden="true">
                  <div
                    className="kn-scorebar-fill"
                    style={{ width: `${(h.score / maxHitScore) * 100}%` }}
                  />
                </div>
                <div className="kn-snippet">{withQueryMarks(h.snippet, lastQuery)}</div>
                <div className="kn-meta">
                  《{h.material_title}》第 {h.chunk_index + 1} 段
                </div>
              </li>
            ))}
          </ul>
        </section>
      )}

      {!searching && hits !== null && hits.length === 0 && (
        <div className="placeholder">没有命中的内容。换个说法试试，或切换检索方式。</div>
      )}

      {/* ── 问答区 ── */}
      <section className="kn-ask">
        <h2>问一问本班资料</h2>
        <form
          className="search-bar kn-form"
          onSubmit={(e) => {
            e.preventDefault()
            void doAsk()
          }}
        >
          <input
            className="input"
            value={question}
            onChange={(e) => setQuestion(e.target.value)}
            placeholder="例如：细胞分裂的方式是什么？"
          />
          <button type="submit" className="btn-primary" disabled={asking || !question.trim()}>
            {asking ? '思考中…' : '提问'}
          </button>
        </form>

        {asking && (
          <p className="kn-thinking">
            正在检索本班资料并组织回答
            <span className="kn-dots" />
          </p>
        )}

        {askErr && (
          <div className="alert alert-danger" role="alert">
            {askErr}
          </div>
        )}

        {answer !== null && (
          <div className="kn-answer">
            <div className="kn-answer-text">{answer}</div>
            <div className="kn-citations">
              <span>出处：</span>
              {citations.length === 0
                ? '无（资料中未找到相关内容）'
                : citedNums(answer).map((n) => {
                    const c = citations[n - 1]
                    if (!c) return null
                    return (
                      <span
                        key={n}
                        className="kn-cite"
                        title={`《${c.material_title}》第 ${c.chunk_index + 1} 段`}
                      >
                        [{n}] 《{c.material_title}》
                      </span>
                    )
                  })}
            </div>
          </div>
        )}
      </section>

      {/* ── 回原文弹层（D6）── */}
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
    </main>
  )
}
