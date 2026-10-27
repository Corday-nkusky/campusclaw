import { useCallback, useEffect, useState } from 'react'
import * as api from '../api'
import { ApiError, describeApiError, type McpCall, type McpServer } from '../api'
import type { ToastKind } from './Toast'

/**
 * MCP 外部工具面板（平台能力 ⑤）。
 *
 * 一条链路只有三步，界面上也刻意做成三步：
 *
 *   登记服务器 → 连接（拉工具清单进库） → 技能里写 `工具名@服务器名` 引用
 *
 * **没有第二步就没有第三步**：技能里的引用是按「服务器名 + 工具名」去缓存表里查的，
 * 没连接过就等于这个工具不存在——所以「连接」不是可选的刷新动作，是启用工具的前提。
 *
 * 令牌只进不出：服务端只回 `has_auth`，不下发 `auth_token`。因此编辑时输入框留空
 * 表示「保持原样」，要清空必须显式勾选——把「留空」当清空会让人在无意间把能用的配置改成坏的。
 */

type Props = {
  notify: (kind: ToastKind, text: string) => void
  onSessionExpired: () => void
}

type Editing = {
  id: number | null
  name: string
  baseUrl: string
  authToken: string
  clearToken: boolean
  enabled: boolean
}

export default function McpPanel({ notify, onSessionExpired }: Props) {
  const [servers, setServers] = useState<McpServer[]>([])
  const [calls, setCalls] = useState<McpCall[]>([])
  const [loading, setLoading] = useState(true)
  const [editing, setEditing] = useState<Editing | null>(null)
  /** 正在连接的服务器 id（按钮显示进度，也防连点）。 */
  const [connecting, setConnecting] = useState<number | null>(null)
  const [showCalls, setShowCalls] = useState(false)
  const [showTools, setShowTools] = useState<number | null>(null)

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

  const reload = useCallback(async () => {
    try {
      const res = await api.listMcpServers()
      setServers(res.servers)
    } catch (e) {
      handle(e)
    } finally {
      setLoading(false)
    }
  }, [handle])

  const loadCalls = useCallback(async () => {
    try {
      const res = await api.listMcpCalls(50)
      setCalls(res.tool_calls)
    } catch (e) {
      handle(e)
    }
  }, [handle])

  useEffect(() => {
    void reload()
  }, [reload])

  const openNew = (): void => {
    setEditing({ id: null, name: '', baseUrl: '', authToken: '', clearToken: false, enabled: true })
  }

  const openEdit = (s: McpServer): void => {
    setEditing({
      id: s.id,
      name: s.name,
      baseUrl: s.base_url,
      authToken: '', // 服务端不下发令牌，留空 = 不改
      clearToken: false,
      enabled: s.enabled,
    })
  }

  const save = async (): Promise<void> => {
    if (!editing) return
    const body: api.McpServerBody = {
      name: editing.name.trim(),
      base_url: editing.baseUrl.trim(),
      enabled: editing.enabled,
    }
    // 三态：勾了清空 → 传空串；填了新值 → 传新值；都没动 → 不传（保留原令牌）
    if (editing.clearToken) body.auth_token = ''
    else if (editing.authToken.trim() !== '') body.auth_token = editing.authToken.trim()

    try {
      if (editing.id === null) {
        await api.createMcpServer(body)
        notify('success', '服务器已登记。下一步点「连接」把工具清单拉进来。')
      } else {
        await api.updateMcpServer(editing.id, body)
        notify('success', '服务器已更新。改了地址要重新连接一次。')
      }
      setEditing(null)
      await reload()
    } catch (e) {
      handle(e)
    }
  }

  const connect = async (s: McpServer): Promise<void> => {
    setConnecting(s.id)
    try {
      const res = await api.connectMcpServer(s.id)
      notify('success', `已连接「${res.server}」，拉到 ${res.count} 个工具。`)
      setShowTools(s.id)
      await reload()
    } catch (e) {
      // 502 = 服务器不可达（没起、地址错、令牌错），不是我们的 bug，如实说出来
      if (e instanceof ApiError && e.status === 502) {
        notify('error', '连不上这台 MCP 服务器：请确认它已启动、地址与令牌正确。')
      } else {
        handle(e)
      }
    } finally {
      setConnecting(null)
    }
  }

  const remove = async (s: McpServer): Promise<void> => {
    if (!window.confirm(`删除服务器「${s.name}」？引用它工具的技能会解析失败。调用记录会保留。`)) return
    try {
      await api.deleteMcpServer(s.id)
      notify('success', `已删除服务器「${s.name}」。`)
      await reload()
    } catch (e) {
      handle(e)
    }
  }

  const toggleCalls = async (): Promise<void> => {
    const next = !showCalls
    setShowCalls(next)
    if (next) await loadCalls()
  }

  if (loading) return <p className="muted tt-loading">正在载入 MCP 配置…</p>

  return (
    <section className="panel">
      <div className="panel-head">
        <h2>MCP 外部工具</h2>
        <span className="muted">
          登记服务器 → 连接拉取工具清单 → 在技能的「工具」里写{' '}
          <code>工具名@服务器名</code>，学生提问时长会按需调用。
        </span>
        <div className="spacer" />
        <button type="button" className="btn-ghost" onClick={() => void toggleCalls()}>
          {showCalls ? '收起调用记录' : '查看调用记录'}
        </button>
        <button type="button" className="btn-secondary" onClick={openNew}>
          登记服务器
        </button>
      </div>

      {servers.length === 0 ? (
        <div className="placeholder">
          还没有登记 MCP 服务器。没有外部工具时平台照样可用——技能只会用到内置的知识库检索。
        </div>
      ) : (
        <ul className="tt-rows">
          {servers.map((s) => (
            <li key={s.id} className="tt-row">
              <div className="tt-row-main">
                <strong>{s.name}</strong>
                <span className={s.enabled ? 'badge badge-teacher' : 'badge badge-student'}>
                  {s.enabled ? '已启用' : '未启用'}
                </span>
                {s.has_auth && <span className="badge badge-student">带令牌</span>}
                <span className={s.tool_count > 0 ? 'badge badge-teacher' : 'badge badge-student'}>
                  {s.tool_count > 0 ? `${s.tool_count} 个工具` : '未连接'}
                </span>
                <div className="tt-row-meta muted">{s.base_url}</div>
              </div>
              {s.tool_count > 0 && (
                <button
                  type="button"
                  className="btn-ghost"
                  onClick={() => setShowTools(showTools === s.id ? null : s.id)}
                >
                  {showTools === s.id ? '隐藏工具' : '看工具'}
                </button>
              )}
              <button
                type="button"
                className="btn-ghost"
                onClick={() => void connect(s)}
                disabled={connecting === s.id}
              >
                {connecting === s.id ? '连接中…' : '连接'}
              </button>
              <button type="button" className="btn-ghost" onClick={() => openEdit(s)}>
                编辑
              </button>
              <button type="button" className="btn-ghost" onClick={() => void remove(s)}>
                删除
              </button>
            </li>
          ))}
        </ul>
      )}

      {showTools !== null && (
        <div className="tt-editor">
          <div className="tt-editor-head">
            <strong>工具清单</strong>
            <span className="muted">
              在技能的「工具」里写成 <code>工具名@服务器名</code>；函数名下发时会转成{' '}
              <code>工具名__服务器名</code>（<code>@</code> 不能出现在函数名里）。
            </span>
          </div>
          {(() => {
            const s = servers.find((x) => x.id === showTools)
            if (!s || s.tools.length === 0) return <span className="muted">这台服务器还没有工具缓存。</span>
            return (
              <ul className="tt-rows">
                {s.tools.map((t) => (
                  <li key={t.name} className="tt-row">
                    <div className="tt-row-main">
                      <code>{`${t.name}@${s.name}`}</code>
                      <div className="tt-row-meta muted">{t.description || '（无说明）'}</div>
                    </div>
                  </li>
                ))}
              </ul>
            )
          })()}
        </div>
      )}

      {editing && (
        <div className="tt-editor">
          <div className="tt-editor-head">
            <strong>{editing.id === null ? '登记 MCP 服务器' : '编辑 MCP 服务器'}</strong>
            <span className="muted">
              服务器名只能用小写字母、数字与连字符；地址是它的 HTTP 入口（如{' '}
              <code>http://127.0.0.1:18090</code>）。
            </span>
          </div>

          <label className="field">
            <span>服务器名</span>
            <input
              className="input"
              value={editing.name}
              onChange={(e) => setEditing({ ...editing, name: e.target.value })}
              placeholder="例如：demo-tools"
            />
          </label>

          <label className="field">
            <span>地址</span>
            <input
              className="input"
              value={editing.baseUrl}
              onChange={(e) => setEditing({ ...editing, baseUrl: e.target.value })}
              placeholder="http://127.0.0.1:18090"
            />
          </label>

          <label className="field">
            <span>访问令牌（可留空）</span>
            <input
              className="input"
              type="password"
              value={editing.authToken}
              onChange={(e) => setEditing({ ...editing, authToken: e.target.value, clearToken: false })}
              placeholder={editing.id === null ? '无需鉴权时留空' : '留空表示不修改已保存的令牌'}
              disabled={editing.clearToken}
            />
          </label>

          {editing.id !== null && (
            <label className="tt-check">
              <input
                type="checkbox"
                checked={editing.clearToken}
                onChange={(e) => setEditing({ ...editing, clearToken: e.target.checked, authToken: '' })}
              />
              <span>清除已保存的令牌（改成无需鉴权）</span>
            </label>
          )}

          <label className="tt-check">
            <input
              type="checkbox"
              checked={editing.enabled}
              onChange={(e) => setEditing({ ...editing, enabled: e.target.checked })}
            />
            <span>启用（停用后引用它的技能解析不到工具，但整轮问答不受影响）</span>
          </label>

          <div className="tt-form-actions">
            <button
              type="button"
              className="btn-primary"
              onClick={() => void save()}
              disabled={!editing.name.trim() || !editing.baseUrl.trim()}
            >
              保存
            </button>
            <button type="button" className="btn-ghost" onClick={() => setEditing(null)}>
              取消
            </button>
          </div>
        </div>
      )}

      {showCalls && (
        <div className="tt-editor">
          <div className="tt-editor-head">
            <strong>最近 50 次工具调用</strong>
            <span className="muted">
              成功的、失败的都留痕。失败会以 <code>role: tool</code> 回传给模型，让它自己决定下一步。
            </span>
          </div>
          {calls.length === 0 ? (
            <span className="muted">还没有任何调用记录。</span>
          ) : (
            <ul className="tt-rows">
              {calls.map((c) => (
                <li key={c.id} className="tt-row">
                  <div className="tt-row-main">
                    <code>{`${c.tool_name}@${c.server_name}`}</code>
                    <span className={c.status === 'ok' ? 'badge badge-teacher' : 'badge badge-student'}>
                      {c.status === 'ok' ? '成功' : '失败'}
                    </span>
                    <span className="muted">{c.duration_ms} ms</span>
                    <div className="tt-row-meta muted">
                      参数 {c.arguments || '{}'}
                      {c.status === 'ok' ? ` → ${c.result.slice(0, 120)}` : ` · ${c.error_text ?? ''}`}
                    </div>
                  </div>
                  <button
                    type="button"
                    className="btn-ghost"
                    onClick={() => void loadCalls()}
                    title="刷新"
                  >
                    刷新
                  </button>
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
    </section>
  )
}
