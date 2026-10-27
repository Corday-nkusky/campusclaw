import { useCallback, useEffect, useRef, useState } from 'react'
import * as api from '../api'
import {
  ApiError,
  describeApiError,
  type Agent,
  type Material,
  type PromptTemplate,
  type Skill,
} from '../api'
import type { ToastKind } from './Toast'
import McpPanel from './McpPanel'

/**
 * 解题助手 · 教师配置（提示词 / 提示词模板 / 技能 / 智能体 / MCP 工具）。
 *
 * 分块对应被刻意分开的几个概念：
 *  - **班级提示词**回答「这个班的助手默认是谁」（一班一条，兜底用）；
 *  - **提示词模板**是可复用的骨架，正文写 `{{变量}}`，绑到智能体后按变量渲染；
 *  - **技能**回答「这类题怎么引导」（可多条，按适用条件词表路由）；
 *  - **智能体**把提示词/模板、技能与材料绑在一起，成为学生可选的一个角色；
 *  - **MCP 服务器**提供技能里能引用的外部工具（`工具名@服务器名`）。
 *
 * 技能用「技能文件原文」编辑而不是拆成表单字段：服务端契约收的就是 source，
 * 拆成字段再拼回去等于在前端重实现一遍解析规则，两边一旦不一致就成了假象。
 * 上传 `.md` 走的是同一个解析器，所以编辑框里存的就是文件原文。
 */

type Props = {
  notify: (kind: ToastKind, text: string) => void
  onSessionExpired: () => void
}

const SKILL_TEMPLATE = `名称：解题引导
适用条件：题目,解题,不会做
工具：knowledge_search
资料：
启用：是

先让学生说出自己的思路，再用追问把他推到下一步；只引导，不给最终答案。`

const TPL_TEMPLATE = `你是{{班级}}的{{学科}}老师，负责带{{年级}}学生。
语气温和，先确认学生对题意的理解，再给下一步提示。
不要直接给出最终答案。`

export default function TutorConfig({ notify, onSessionExpired }: Props) {
  const [prompt, setPrompt] = useState('')
  const [promptSaving, setPromptSaving] = useState(false)

  const [skills, setSkills] = useState<Skill[]>([])
  const [agents, setAgents] = useState<Agent[]>([])
  const [materials, setMaterials] = useState<Material[]>([])
  const [templates, setTemplates] = useState<PromptTemplate[]>([])
  const [loading, setLoading] = useState(true)

  /** 上传技能文件用的隐藏 file input。 */
  const skillFileRef = useRef<HTMLInputElement>(null)
  const [skillUploading, setSkillUploading] = useState(false)

  /** 技能编辑器：null = 关闭；id 为 null 表示新建。 */
  const [skillEdit, setSkillEdit] = useState<{ id: number | null; source: string } | null>(null)
  /** 模板编辑器。 */
  const [tplEdit, setTplEdit] = useState<{
    id: number | null
    name: string
    description: string
    body: string
  } | null>(null)
  /** 模板渲染预览（按当前填的变量试算一次，不调模型）。 */
  const [tplPreview, setTplPreview] = useState<{ id: number; text: string; unfilled: string[] } | null>(null)
  /** 智能体编辑器。 */
  const [agentEdit, setAgentEdit] = useState<{
    id: number | null
    name: string
    prompt: string
    enabled: boolean
    skillIds: number[]
    materialIds: number[]
    templateId: number
    templateVars: Record<string, string>
  } | null>(null)

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
      const [p, s, a, m, t] = await Promise.all([
        api.getPrompt(),
        api.listSkills(),
        api.listAgents(),
        api.materials(),
        api.listTemplates(),
      ])
      setPrompt(p.body)
      setSkills(s.skills)
      setAgents(a.agents)
      setMaterials(m.materials)
      setTemplates(t.templates)
    } catch (e) {
      handle(e)
    } finally {
      setLoading(false)
    }
  }, [handle])

  useEffect(() => {
    void reload()
  }, [reload])

  const savePrompt = async (): Promise<void> => {
    setPromptSaving(true)
    try {
      const res = await api.putPrompt(prompt)
      setPrompt(res.body)
      notify('success', '提示词已保存。')
    } catch (e) {
      handle(e)
    } finally {
      setPromptSaving(false)
    }
  }

  const saveSkill = async (): Promise<void> => {
    if (!skillEdit) return
    try {
      if (skillEdit.id === null) {
        await api.createSkill(skillEdit.source)
        notify('success', '技能已创建。')
      } else {
        await api.updateSkill(skillEdit.id, skillEdit.source)
        notify('success', '技能已更新。')
      }
      setSkillEdit(null)
      await reload()
    } catch (e) {
      handle(e)
    }
  }

  const removeSkill = async (s: Skill): Promise<void> => {
    if (!window.confirm(`删除技能「${s.name}」？绑定了它的智能体会同时解除绑定。`)) return
    try {
      await api.deleteSkill(s.id)
      notify('success', `已删除技能「${s.name}」。`)
      await reload()
    } catch (e) {
      handle(e)
    }
  }

  /**
   * 上传技能文件（`.md` / `.txt`）。
   *
   * 不在这里校验文件格式：服务端用与粘贴完全相同的解析器，
   * 它拒绝时带 `detail` 说明哪一行不合规，直接转述给用户比前端猜规则准。
   */
  const uploadSkill = async (file: File): Promise<void> => {
    setSkillUploading(true)
    try {
      const s = await api.uploadSkillFile(file)
      notify('success', `已导入技能「${s.name}」。`)
      await reload()
    } catch (e) {
      handle(e)
    } finally {
      setSkillUploading(false)
      // 清空 value，否则同一个文件再选一次不触发 change
      if (skillFileRef.current) skillFileRef.current.value = ''
    }
  }

  const saveTpl = async (): Promise<void> => {
    if (!tplEdit) return
    const body = {
      name: tplEdit.name.trim(),
      description: tplEdit.description.trim(),
      body: tplEdit.body.trim(),
    }
    try {
      if (tplEdit.id === null) {
        await api.createTemplate(body)
        notify('success', '模板已创建。')
      } else {
        await api.updateTemplate(tplEdit.id, body)
        notify('success', '模板已更新。')
      }
      setTplEdit(null)
      setTplPreview(null)
      await reload()
    } catch (e) {
      handle(e)
    }
  }

  const removeTpl = async (t: PromptTemplate): Promise<void> => {
    if (!window.confirm(`删除模板「${t.name}」？绑定了它的智能体会退回只用自身提示词。`)) return
    try {
      await api.deleteTemplate(t.id)
      notify('success', `已删除模板「${t.name}」。`)
      await reload()
    } catch (e) {
      handle(e)
    }
  }

  const removeAgent = async (a: Agent): Promise<void> => {
    if (!window.confirm(`删除智能体「${a.name}」？`)) return
    try {
      await api.deleteAgent(a.id)
      notify('success', `已删除智能体「${a.name}」。`)
      await reload()
    } catch (e) {
      handle(e)
    }
  }

  /**
   * 试渲染：**不传任何变量**，看骨架原文与漏填清单。
   *
   * 刻意不传占位值：服务端对未提供的变量原样保留 `{{名字}}`，
   * 这样"漏填"会以占位符和 `unfilled` 两个形态同时出现，与真正送进 system 的文本一致。
   * 若在这里塞"（X未填）"之类的假值，预览挺好读，但会把"漏填检查"这件事演成假的。
   */
  const previewTpl = async (t: PromptTemplate): Promise<void> => {
    try {
      const r = await api.renderTemplate(t.id, {})
      setTplPreview({ id: t.id, text: r.rendered, unfilled: r.unfilled })
    } catch (e) {
      handle(e)
    }
  }

  const saveAgent = async (): Promise<void> => {
    if (!agentEdit) return
    const body = {
      name: agentEdit.name.trim(),
      prompt: agentEdit.prompt.trim(),
      enabled: agentEdit.enabled,
      skill_ids: agentEdit.skillIds,
      material_ids: agentEdit.materialIds,
      template_id: agentEdit.templateId,
      template_vars: agentEdit.templateId === 0 ? {} : agentEdit.templateVars,
    }
    try {
      if (agentEdit.id === null) {
        await api.createAgent(body)
        notify('success', '智能体已创建。')
      } else {
        await api.updateAgent(agentEdit.id, body)
        notify('success', '智能体已更新。')
      }
      setAgentEdit(null)
      await reload()
    } catch (e) {
      handle(e)
    }
  }

  const toggle = <T,>(list: T[], v: T): T[] =>
    list.includes(v) ? list.filter((x) => x !== v) : [...list, v]

  if (loading) return <p className="muted tt-loading">正在载入配置…</p>

  return (
    <div className="tt-config">
      {/* ── 提示词 ── */}
      <section className="panel">
        <div className="panel-head">
          <h2>提示词</h2>
          <span className="muted">规定「助手是谁」，本班共用一条。</span>
        </div>
        <textarea
          className="input tt-textarea"
          rows={5}
          value={prompt}
          onChange={(e) => setPrompt(e.target.value)}
          placeholder="例如：你是一位耐心的学科助教，只引导学生自己推导，不直接给出答案。"
        />
        <div className="tt-form-actions">
          <button type="button" className="btn-primary" onClick={() => void savePrompt()} disabled={promptSaving}>
            {promptSaving ? '保存中…' : '保存提示词'}
          </button>
          <span className="muted">1–2000 字</span>
        </div>
      </section>

      {/* ── 提示词模板 ── */}
      <section className="panel">
        <div className="panel-head">
          <h2>提示词模板</h2>
          <span className="muted">
            可复用的骨架，正文里写 <code>{'{{变量}}'}</code>；绑到智能体后每轮渲染成系统提示词。
          </span>
          <div className="spacer" />
          <button
            type="button"
            className="btn-secondary"
            onClick={() => setTplEdit({ id: null, name: '', description: '', body: TPL_TEMPLATE })}
          >
            新建模板
          </button>
        </div>

        {templates.length === 0 ? (
          <div className="placeholder">
            还没有模板。模板不是必须的——不绑模板时，助手只用智能体自己的提示词。
          </div>
        ) : (
          <ul className="tt-rows">
            {templates.map((t) => (
              <li key={t.id} className="tt-row">
                <div className="tt-row-main">
                  <strong>{t.name}</strong>
                  <span className="badge badge-neutral">{t.variables.length} 个变量</span>
                  <div className="tt-row-meta muted">
                    {t.description || '（无说明）'}
                    {t.variables.length > 0 ? ` · ${t.variables.join('、')}` : ''}
                  </div>
                </div>
                <button type="button" className="btn-ghost" onClick={() => void previewTpl(t)}>
                  试渲染
                </button>
                <button
                  type="button"
                  className="btn-ghost"
                  onClick={() =>
                    setTplEdit({ id: t.id, name: t.name, description: t.description, body: t.body })
                  }
                >
                  编辑
                </button>
                <button type="button" className="btn-ghost tt-danger" onClick={() => void removeTpl(t)}>
                  删除
                </button>
              </li>
            ))}
          </ul>
        )}

        {tplPreview && (
          <div className="tt-editor">
            <div className="tt-editor-head">
              <strong>渲染预览</strong>
              <span className="muted">
                用占位符本身当变量值试算一次，走的是服务端同一个渲染函数，不调模型。
                {tplPreview.unfilled.length > 0
                  ? ` 仍未填：${tplPreview.unfilled.join('、')}`
                  : ' 全部变量已填充。'}
              </span>
            </div>
            <pre className="tt-preview tt-mono">{tplPreview.text}</pre>
            <div className="tt-form-actions">
              <button type="button" className="btn-ghost" onClick={() => setTplPreview(null)}>
                关闭
              </button>
            </div>
          </div>
        )}

        {tplEdit && (
          <div className="tt-editor">
            <div className="tt-editor-head">
              <strong>{tplEdit.id === null ? '新建模板' : '编辑模板'}</strong>
              <span className="muted">名称 1–64 字，正文 1–4000 字，变量不超过 64 个。</span>
            </div>
            <label className="field">
              <span>名称</span>
              <input
                className="input"
                value={tplEdit.name}
                onChange={(e) => setTplEdit({ ...tplEdit, name: e.target.value })}
                placeholder="例如：高中生物助教"
              />
            </label>
            <label className="field">
              <span>说明</span>
              <input
                className="input"
                value={tplEdit.description}
                onChange={(e) => setTplEdit({ ...tplEdit, description: e.target.value })}
                placeholder="这份模板用于什么场景（可留空）"
              />
            </label>
            <div className="field">
              <span>正文</span>
              <textarea
                className="input tt-textarea tt-mono"
                rows={8}
                value={tplEdit.body}
                onChange={(e) => setTplEdit({ ...tplEdit, body: e.target.value })}
              />
            </div>
            <div className="tt-form-actions">
              <button
                type="button"
                className="btn-primary"
                onClick={() => void saveTpl()}
                disabled={!tplEdit.name.trim() || !tplEdit.body.trim()}
              >
                保存
              </button>
              <button type="button" className="btn-ghost" onClick={() => setTplEdit(null)}>
                取消
              </button>
            </div>
          </div>
        )}
      </section>

      {/* ── 技能 ── */}
      <section className="panel">
        <div className="panel-head">
          <h2>技能</h2>
          <span className="muted">规定「这类题怎么引导」，按适用条件词表命中。</span>
          <div className="spacer" />
          <input
            ref={skillFileRef}
            type="file"
            accept=".md,.txt,text/markdown,text/plain"
            className="file-input-hidden"
            onChange={(e) => {
              const f = e.target.files?.[0]
              if (f) void uploadSkill(f)
            }}
          />
          <button
            type="button"
            className="btn-ghost"
            onClick={() => skillFileRef.current?.click()}
            disabled={skillUploading}
          >
            {skillUploading ? '导入中…' : '上传技能文件'}
          </button>
          <button
            type="button"
            className="btn-secondary"
            onClick={() => setSkillEdit({ id: null, source: SKILL_TEMPLATE })}
          >
            新建技能
          </button>
        </div>

        {skills.length === 0 ? (
          <div className="placeholder">还没有技能。至少建一条「解题引导」类技能。</div>
        ) : (
          <ul className="tt-rows">
            {skills.map((s) => (
              <li key={s.id} className="tt-row">
                <div className="tt-row-main">
                  <strong>{s.name}</strong>
                  <span className={s.enabled ? 'badge badge-teacher' : 'badge badge-student'}>
                    {s.enabled ? '已启用' : '未启用'}
                  </span>
                  <div className="tt-row-meta muted">
                    适用条件：{s.trigger || '（未设）'}
                    {s.subject ? ` · 资料：${s.subject}` : ''}
                    {s.tools.length > 0 ? ` · 工具：${s.tools.join('、')}` : ''}
                  </div>
                </div>
                <button
                  type="button"
                  className="btn-ghost"
                  onClick={() => setSkillEdit({ id: s.id, source: s.source })}
                >
                  编辑
                </button>
                <button type="button" className="btn-ghost tt-danger" onClick={() => void removeSkill(s)}>
                  删除
                </button>
              </li>
            ))}
          </ul>
        )}

        {skillEdit && (
          <div className="tt-editor">
            <div className="tt-editor-head">
              <strong>{skillEdit.id === null ? '新建技能' : '编辑技能'}</strong>
              <span className="muted">
                头部五键（名称 / 适用条件 / 工具 / 资料 / 启用），空行以下写说明。
                「工具」可写 <code>knowledge_search</code>（内置检索），也可写
                <code>工具名@服务器名</code> 引用下方已连接的外部工具，逗号分隔。
              </span>
            </div>
            <textarea
              className="input tt-textarea tt-mono"
              rows={10}
              value={skillEdit.source}
              onChange={(e) => setSkillEdit({ ...skillEdit, source: e.target.value })}
            />
            <div className="tt-form-actions">
              <button type="button" className="btn-primary" onClick={() => void saveSkill()}>
                保存
              </button>
              <button type="button" className="btn-ghost" onClick={() => setSkillEdit(null)}>
                取消
              </button>
            </div>
          </div>
        )}
      </section>

      {/* ── 智能体 ── */}
      <section className="panel">
        <div className="panel-head">
          <h2>智能体</h2>
          <span className="muted">把提示词、技能与材料绑成一个学生可选的「老师」。</span>
          <div className="spacer" />
          <button
            type="button"
            className="btn-secondary"
            onClick={() =>
              setAgentEdit({
                id: null,
                name: '',
                prompt: '',
                enabled: true,
                skillIds: [],
                materialIds: [],
                templateId: 0,
                templateVars: {},
              })
            }
          >
            新建智能体
          </button>
        </div>

        {agents.length === 0 ? (
          <div className="placeholder">还没有智能体。学生新建会话前至少需要一个已启用的智能体。</div>
        ) : (
          <ul className="tt-rows">
            {agents.map((a) => (
              <li key={a.id} className="tt-row">
                <div className="tt-row-main">
                  <strong>{a.name}</strong>
                  <span className={a.enabled ? 'badge badge-teacher' : 'badge badge-student'}>
                    {a.enabled ? '已启用' : '未启用'}
                  </span>
                  <div className="tt-row-meta muted">
                    技能 {a.skill_ids.length} 条 · 材料 {a.material_ids.length} 份
                  </div>
                </div>
                <button
                  type="button"
                  className="btn-ghost"
                  onClick={() =>
                    setAgentEdit({
                      id: a.id,
                      name: a.name,
                      prompt: a.prompt,
                      enabled: a.enabled,
                      skillIds: a.skill_ids,
                      materialIds: a.material_ids,
                      templateId: a.template_id ?? 0,
                      templateVars: a.template_vars ?? {},
                    })
                  }
                >
                  编辑
                </button>
                <button type="button" className="btn-ghost tt-danger" onClick={() => void removeAgent(a)}>
                  删除
                </button>
              </li>
            ))}
          </ul>
        )}

        {agentEdit && (
          <div className="tt-editor">
            <div className="tt-editor-head">
              <strong>{agentEdit.id === null ? '新建智能体' : '编辑智能体'}</strong>
            </div>

            <label className="field">
              <span>名称</span>
              <input
                className="input"
                value={agentEdit.name}
                onChange={(e) => setAgentEdit({ ...agentEdit, name: e.target.value })}
                placeholder="例如：细胞学助教"
              />
            </label>

            <label className="field">
              <span>角色提示词</span>
              <textarea
                className="input tt-textarea"
                rows={4}
                value={agentEdit.prompt}
                onChange={(e) => setAgentEdit({ ...agentEdit, prompt: e.target.value })}
                placeholder="留空时服务端会回退到默认角色设定。"
              />
            </label>

            <label className="field">
              <span>提示词模板（可选）</span>
              <select
                className="kn-select"
                value={agentEdit.templateId}
                onChange={(e) => {
                  const id = Number(e.target.value)
                  // 换模板就重置变量表：旧变量对新模板没有意义
                  setAgentEdit({ ...agentEdit, templateId: id, templateVars: {} })
                }}
              >
                <option value={0}>不绑定（只用上面的提示词）</option>
                {templates.map((t) => (
                  <option key={t.id} value={t.id}>
                    {t.name}
                  </option>
                ))}
              </select>
            </label>

            {agentEdit.templateId !== 0 && (
              <div className="field">
                <span>模板变量</span>
                {(() => {
                  const tpl = templates.find((t) => t.id === agentEdit.templateId)
                  if (!tpl) return <span className="muted">模板已被删除，保存时会退回不绑定。</span>
                  if (tpl.variables.length === 0) {
                    return <span className="muted">这份模板没有变量，绑定后直接按原文发送。</span>
                  }
                  return (
                    <div className="tt-var-grid">
                      {tpl.variables.map((v) => (
                        <label key={v} className="tt-var">
                          <span className="muted">{`{{${v}}}`}</span>
                          <input
                            className="input"
                            value={agentEdit.templateVars[v] ?? ''}
                            onChange={(e) =>
                              setAgentEdit({
                                ...agentEdit,
                                templateVars: { ...agentEdit.templateVars, [v]: e.target.value },
                              })
                            }
                            placeholder={`填入「${v}」`}
                          />
                        </label>
                      ))}
                    </div>
                  )
                })()}
                <span className="muted">
                  留空的变量不会消失——它会原样以 <code>{'{{名字}}'}</code> 留在提示词里，便于在报文里发现漏填。
                </span>
              </div>
            )}

            <div className="field">
              <span>绑定技能</span>
              {skills.length === 0 ? (
                <span className="muted">先在上方创建技能。</span>
              ) : (
                <div className="tt-checks">
                  {skills.map((s) => (
                    <label key={s.id} className="tt-check">
                      <input
                        type="checkbox"
                        checked={agentEdit.skillIds.includes(s.id)}
                        onChange={() => setAgentEdit({ ...agentEdit, skillIds: toggle(agentEdit.skillIds, s.id) })}
                      />
                      <span>{s.name}</span>
                    </label>
                  ))}
                </div>
              )}
            </div>

            <div className="field">
              <span>绑定材料（检索只在这些材料内进行）</span>
              {materials.length === 0 ? (
                <span className="muted">本班还没有材料。</span>
              ) : (
                <div className="tt-checks">
                  {materials.map((m) => (
                    <label key={m.id} className="tt-check">
                      <input
                        type="checkbox"
                        checked={agentEdit.materialIds.includes(m.id)}
                        onChange={() =>
                          setAgentEdit({ ...agentEdit, materialIds: toggle(agentEdit.materialIds, m.id) })
                        }
                      />
                      <span>{m.title}</span>
                    </label>
                  ))}
                </div>
              )}
            </div>

            <label className="tt-check">
              <input
                type="checkbox"
                checked={agentEdit.enabled}
                onChange={(e) => setAgentEdit({ ...agentEdit, enabled: e.target.checked })}
              />
              <span>启用（学生新建会话时可见）</span>
            </label>

            <div className="tt-form-actions">
              <button
                type="button"
                className="btn-primary"
                onClick={() => void saveAgent()}
                disabled={
                  !agentEdit.name.trim() || (!agentEdit.prompt.trim() && agentEdit.templateId === 0)
                }
              >
                保存
              </button>
              <button type="button" className="btn-ghost" onClick={() => setAgentEdit(null)}>
                取消
              </button>
              {!agentEdit.prompt.trim() && agentEdit.templateId === 0 && (
                <span className="muted">角色提示词与提示词模板至少要填一个。</span>
              )}
            </div>
          </div>
        )}
      </section>

      {/* ── MCP 外部工具 ── */}
      <McpPanel notify={notify} onSessionExpired={onSessionExpired} />
    </div>
  )
}
