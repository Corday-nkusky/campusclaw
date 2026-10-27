/**
 * 前端与后端的**唯一**交互出口。
 *
 * 四条约定（对应 spec.md R9，add-refresh-token 后改版）：
 *
 *  1. 身份只来自 `GET /api/me`。localStorage 只保存**凭证**（`cc_access` / `cc_refresh` /
 *     `cc_access_exp`），不保存任何身份/角色字段；前端不解析 access token —— 篡改本地
 *     数据无从影响权限（R9.1）。
 *  2. access 失效或临近过期时**静默刷新**：用 refresh 换新 access 并重放原请求，
 *     用户无感（R9.5）。revoke 与刷新都是服务端说了算。
 *  3. 静默刷新也失败（refresh 不存在/过期/已吊销/重放）才抛 `ApiError(401)`，
 *     由界面清凭证回登录页（R9.3）。
 *  4. 认证走 `Authorization: Bearer <access>` 请求头。请求路径仍写相对路径 `/api/...`：
 *     开发期由 Vite 代理到 8080，生产期由 Nginx 反代，两种形态在浏览器眼里都是同源。
 */

export type Role = 'teacher' | 'student'

/** localStorage 键。**只允许存凭证**，不允许存身份。 */
const ACCESS_KEY = 'cc_access'
const REFRESH_KEY = 'cc_refresh'
/** access 的过期时刻（毫秒时间戳）。仅作本地"是否该提前刷新"的提示，**不是授权依据**。 */
const ACCESS_EXP_KEY = 'cc_access_exp'
/** 提前刷新余量：access 剩余寿命小于它就先刷新，避免"发出请求那刻刚好过期"。 */
const REFRESH_SKEW_MS = 30_000

/** `POST /api/login` 的响应体（add-refresh-token R1.1）。 */
interface LoginResponse {
  ok: boolean
  access_token: string
  refresh_token: string
  token_type: string
  expires_in: number
}

/** `POST /api/refresh` 的响应体（R11.1）：与登录同形，但无 `ok`。 */
interface RefreshResponse {
  access_token: string
  refresh_token: string
  token_type: string
  expires_in: number
}

function readStore(key: string): string | null {
  try {
    return localStorage.getItem(key)
  } catch {
    // localStorage 被禁用等极端情况：等同未登录
    return null
  }
}

function getAccess(): string | null {
  return readStore(ACCESS_KEY)
}

function getRefresh(): string | null {
  return readStore(REFRESH_KEY)
}

/** 保存一对令牌（登录与刷新共用）。`expiresIn` 为 access 剩余秒数。 */
function saveTokens(access: string, refresh: string, expiresIn: number): void {
  try {
    localStorage.setItem(ACCESS_KEY, access)
    localStorage.setItem(REFRESH_KEY, refresh)
    localStorage.setItem(ACCESS_EXP_KEY, String(Date.now() + expiresIn * 1000))
  } catch {
    // 写入失败（隐私模式等）：不抛出，退化为"本次会话不持久"
  }
}

function clearTokens(): void {
  try {
    localStorage.removeItem(ACCESS_KEY)
    localStorage.removeItem(REFRESH_KEY)
    localStorage.removeItem(ACCESS_EXP_KEY)
  } catch {
    // 同上
  }
}

/** access 是否临近/已过期（本地判断，仅用于决定"是否值得先刷新"）。 */
function accessExpiring(): boolean {
  const raw = readStore(ACCESS_EXP_KEY)
  if (!raw) return false // 无记录时不主动刷新，让服务端 401 来决定
  const exp = Number(raw)
  return Number.isFinite(exp) && Date.now() >= exp - REFRESH_SKEW_MS
}

/** `GET /api/me` 的响应体，字段与后端 handlers.go 的 Me 一一对应。 */
export interface Me {
  user_id: number
  username: string
  role: Role
  class_id: number
  class_name: string
}

export class ApiError extends Error {
  /** HTTP 状态码；0 表示请求没拿到响应（网络层失败）。 */
  readonly status: number

  constructor(status: number, message: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
  }
}

async function readError(res: Response): Promise<string> {
  try {
    const body: unknown = await res.json()
    if (body && typeof body === 'object' && 'error' in body) {
      const code = (body as { error: unknown }).error
      if (typeof code === 'string') return code
    }
  } catch {
    // 响应体不是 JSON，退回到状态码描述
  }
  return `HTTP ${res.status}`
}

/** 已登录时给请求带上 `Authorization: Bearer` 头。 */
function authHeaders(): Record<string, string> {
  const token = getAccess()
  return token ? { Authorization: `Bearer ${token}` } : {}
}

/**
 * 静默刷新：用 refresh 换一对新令牌。成功返回 true。
 *
 * **互斥**（design D7）：同一时刻只允许一个刷新在飞，并发的调用都等同一个 Promise。
 * 这既省请求，也避免并发刷新触发服务端的"重放检测"（旧 refresh 被换两次 → 判定泄露 → 全吊销）。
 */
let refreshInFlight: Promise<boolean> | null = null

function refreshTokens(): Promise<boolean> {
  if (refreshInFlight) return refreshInFlight

  refreshInFlight = (async () => {
    const refresh = getRefresh()
    if (!refresh) return false
    try {
      const res = await fetch('/api/refresh', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ refresh_token: refresh }),
      })
      if (!res.ok) {
        // refresh 无效（过期/已吊销/重放）：清凭证，交给上层回登录页
        clearTokens()
        return false
      }
      const body = (await res.json()) as RefreshResponse
      if (!body.access_token || !body.refresh_token) {
        clearTokens()
        return false
      }
      saveTokens(body.access_token, body.refresh_token, body.expires_in)
      return true
    } catch {
      // 网络层失败：**不清凭证**（可能只是暂时断网，refresh 仍然有效）
      return false
    }
  })().finally(() => {
    refreshInFlight = null
  })

  return refreshInFlight
}

/** 用当前 access 头发起一次请求（不处理刷新逻辑）。 */
async function rawFetch(path: string, init: RequestInit = {}): Promise<Response> {
  try {
    return await fetch(path, { ...init, headers: { ...authHeaders(), ...init.headers } })
  } catch {
    // fetch 只在网络层失败时 reject（DNS、连接被拒、被 CORS 拦下）
    throw new ApiError(0, '网络异常：无法连接服务器，请稍后再试。')
  }
}

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  // 进入前：access 临近过期 → 先刷新，避免"发出请求那刻刚好过期"
  if (accessExpiring()) await refreshTokens()

  let res = await rawFetch(path, init)

  // 收到 401：可能只是 access 过期。刷新一次后**重放**原请求（R9.5）。
  if (res.status === 401) {
    const ok = await refreshTokens()
    if (ok) res = await rawFetch(path, init)
  }

  if (!res.ok) {
    throw new ApiError(res.status, await readError(res))
  }
  return (await res.json()) as T
}

/** `POST /api/login` —— 成功后保存服务端签发的 access + refresh（R1.1）。 */
export function login(username: string, password: string): Promise<{ ok: boolean }> {
  return request<LoginResponse>('/api/login', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username, password }),
  }).then((res) => {
    if (!res.access_token || !res.refresh_token) throw new ApiError(500, '服务端未返回令牌')
    saveTokens(res.access_token, res.refresh_token, res.expires_in)
    return { ok: res.ok }
  })
}

/**
 * `POST /api/logout` —— 服务端**删除该 refresh 记录**（R1.4，add-refresh-token 语义反转）。
 * 随后本地清除全部凭证。已签发的 access 在其余下 TTL（≤15 分钟）内仍有效，属既定权衡。
 */
export function logout(): Promise<{ ok: boolean }> {
  const refresh = getRefresh()
  return request<{ ok: boolean }>('/api/logout', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ refresh_token: refresh ?? '' }),
  }).finally(() => clearTokens())
}

/** `GET /api/me` —— 当前身份。未登录返回 401。 */
export function me(): Promise<Me> {
  return request<Me>('/api/me')
}

/** 主动清除本地凭证（401 引导回登录页前调用，R9.3）。 */
export function dropToken(): void {
  clearTokens()
}

/** 材料列表项。**不含** `stored_name` —— 服务端刻意不下发磁盘存储名（R5.3）。 */
export interface Material {
  id: number
  title: string
  size_bytes: number
  uploader_id: number
  uploader: string
  created_at: string
}

/** `GET /api/materials` 的响应体。`class_id` / `class_name` 是服务端答的「本次查询范围」。 */
export interface MaterialList {
  class_id: number
  class_name: string
  query: string
  count: number
  materials: Material[]
}

/** `GET /api/materials/{id}` 的响应体。 */
export interface MaterialDetail extends Material {
  class_id: number
  class_name: string
  body: string
  viewer: Me
}

/**
 * `GET /api/materials` —— 本班材料列表。
 *
 * 搜索走**服务端查询**而不是前端 filter：后端把班级条件与搜索条件写成 AND，
 * 前端过滤做不到这一点（前端手里只有本班数据，过滤范围无从校验）。
 */
export function materials(keyword?: string): Promise<MaterialList> {
  const q = keyword?.trim() ?? ''
  const suffix = q ? `?q=${encodeURIComponent(q)}` : ''
  return request<MaterialList>(`/api/materials${suffix}`)
}

/** `GET /api/materials/{id}` —— 材料详情（含知识库正文）。跨班与不存在同为 404。 */
export function material(id: number): Promise<MaterialDetail> {
  return request<MaterialDetail>(`/api/materials/${id}`)
}

/**
 * 下载材料文件（add-token-auth design.md D5）。
 *
 * Bearer 头**浏览器导航带不了**，所以不能用 `<a href>` 直接下载：
 * 先 fetch（带 Authorization 头）拿到 blob，再用 ObjectURL 触发保存。
 * 文件名优先取响应头 `Content-Disposition`，解析不到退回 `标题.ext`。
 *
 * add-refresh-token：下载是**幂等读**，access 过期时走静默刷新后重放（R9.5）。
 */
export async function downloadMaterialFile(
  id: number,
  fallbackTitle: string,
): Promise<{ url: string; filename: string }> {
  const path = `/api/materials/${id}/file`
  if (accessExpiring()) await refreshTokens()

  let res: Response
  try {
    res = await fetch(path, { headers: authHeaders() })
  } catch {
    throw new ApiError(0, '网络异常：无法连接服务器，请稍后再试。')
  }

  // access 过期：静默刷新后重放一次（下载是只读，重放安全）
  if (res.status === 401 && (await refreshTokens())) {
    try {
      res = await fetch(path, { headers: authHeaders() })
    } catch {
      throw new ApiError(0, '网络异常：无法连接服务器，请稍后再试。')
    }
  }
  if (!res.ok) {
    throw new ApiError(res.status, await readError(res))
  }

  const blob = await res.blob()
  const url = URL.createObjectURL(blob)
  return { url, filename: filenameFromDisposition(res.headers.get('Content-Disposition'), fallbackTitle) }
}

/** 从 Content-Disposition 解析文件名；失败时用「标题 + 原扩展名」兜底。 */
function filenameFromDisposition(header: string | null, fallbackTitle: string): string {
  if (header) {
    // RFC 5987 形态：filename*=UTF-8''<percent-encoded>
    const star = /filename\*=(?:UTF-8|utf-8)''([^;]+)/.exec(header)
    if (star?.[1]) {
      try {
        return decodeURIComponent(star[1].trim())
      } catch {
        // 非法编码，走下面的普通形态
      }
    }
    const plain = /filename="?([^";]+)"?/.exec(header)
    if (plain?.[1]) return plain[1].trim()
  }
  // 兜底：标题 + .md/.txt 扩展名（服务端只接受这两种，R4.2）
  return /\.(md|txt)$/i.test(fallbackTitle) ? fallbackTitle : `${fallbackTitle}.md`
}

/** `POST /api/materials` 的 201 响应体。 */
export interface UploadResult {  id: number
  title: string
  class_id: number
  class_name: string
  size_bytes: number
  uploader: string
}

/** 服务端 error 码 → 给人看的话。键与后端各 handler 下发的 `error` 值一一对应。 */
const ERROR_TEXT: Record<string, string> = {
  unauthorized: '未登录或凭证已失效，请重新登录。',
  forbidden: '权限不足：上传只有教师可以操作。',
  not_found: '资源不存在，或不属于你所在的班级。',
  unsupported_type: '不支持的文件类型，只允许 .txt 与 .md。',
  payload_too_large: '文件超过大小上限。',
  empty_content: '文件内容不能为空。',
  invalid_encoding: '文件内容不是合法的 UTF-8 文本。',
  bad_request: '请求格式不正确。',
  db_unavailable: '服务暂时不可用，请稍后再试。',
  invalid_credentials: '账号或口令不正确。',
  invalid_refresh: '登录状态已失效，请重新登录。',
}

/**
 * 把错误翻译成中文提示。
 *
 * 关键是**不改变语义**（R9.3）：403 必须说「权限不足」，404 必须说「不存在」。
 * 把 403 显示成「请重新登录」就是把授权问题伪装成认证问题，
 * 使用者会去重新登录，而真实原因（角色不够）被掩盖。
 */
export function describeApiError(e: unknown): string {
  if (!(e instanceof ApiError)) {
    return e instanceof Error ? e.message : String(e)
  }
  if (e.status === 0) return e.message
  return ERROR_TEXT[e.message] ?? `请求失败，请稍后再试。（错误码 ${e.status}）`
}

/**
 * `POST /api/materials` —— 教师上传材料，带真实上传进度。
 *
 * 这里用 XMLHttpRequest 而不是 fetch：**fetch 无法报告上传进度**
 * （只有下载侧可读流），而 R10.1 要求「上传进度」。这是选 XHR 的唯一原因。
 *
 * add-refresh-token（R9.6）：上传**不自动重放**。它非幂等，超时/断连下重放
 * 可能造成同一文件写两次。因此进 XHR 前只在"本地已知 access 临近过期"时先刷新；
 * 若仍以 401 返回，直接抛给上层提示重新登录，**不在本函数内重发**。
 */
export function uploadMaterial(
  file: File,
  onProgress?: (percent: number) => void,
): Promise<UploadResult> {
  return new Promise<UploadResult>((resolve, reject) => {
    const form = new FormData()
    // 第三个参数是**原始文件名**，服务端只用它做展示标题，
    // 磁盘存储名由服务端自己生成（R4.5）——前端无法影响落盘路径。
    form.append('file', file, file.name)

    const start = (): void => {
      const xhr = new XMLHttpRequest()
      xhr.open('POST', '/api/materials')
      for (const [k, v] of Object.entries(authHeaders())) xhr.setRequestHeader(k, v)

      xhr.upload.onprogress = (ev) => {
        if (ev.lengthComputable && onProgress) {
          onProgress(Math.round((ev.loaded / ev.total) * 100))
        }
      }

      xhr.onload = () => {
        let body: unknown = null
        try {
          body = JSON.parse(xhr.responseText) as unknown
        } catch {
          // 非 JSON 响应（例如被反代拦下的错误页）
        }
        if (xhr.status >= 200 && xhr.status < 300) {
          resolve(body as UploadResult)
          return
        }
        const code =
          body && typeof body === 'object' && 'error' in body
            ? String((body as { error: unknown }).error)
            : `HTTP ${xhr.status}`
        reject(new ApiError(xhr.status, code))
      }
      xhr.onerror = () => reject(new ApiError(0, '网络异常：无法连接后端服务'))
      xhr.onabort = () => reject(new ApiError(0, '上传已取消'))

      xhr.send(form)
    }

    // 上传前若 access 临近过期，先刷新一次再发；刷新失败也不阻断，
    // 让请求自己去拿 401（那时由界面提示重新登录）。
    if (accessExpiring()) {
      void refreshTokens().then(start, start)
    } else {
      start()
    }
  })
}

// ── 知识库检索与问答（add-class-scoped-retrieval R1/R3/R6）──

export type SearchMode = 'keyword' | 'vector' | 'hybrid'

/** 单条命中的八字段溯源块（spec R3）。 */
export interface SearchHit {
  material_id: number
  material_title: string
  chunk_id: number
  chunk_index: number
  snippet: string
  start_offset: number
  end_offset: number
  score: number
  /** 第 5 课起分三档（可选：第 4 课链路不一定下发）。 */
  score_final?: number
  score_keyword?: number
  score_vector?: number | null
}

export interface SearchResponse {
  mode: SearchMode
  hits: SearchHit[]
}

/** `POST /api/search` —— 本班检索。mode 缺省 hybrid，top_k ∈ [1,20]。 */
export function searchKnowledge(query: string, mode: SearchMode, topK = 5): Promise<SearchResponse> {
  return request<SearchResponse>('/api/search', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ query, mode, top_k: topK }),
  })
}

export interface AskResponse {
  answer: string
  citations: SearchHit[]
}

/** `POST /api/ask` —— 问答：无命中返回固定文案且 citations 为空。 */
export function askKnowledge(question: string): Promise<AskResponse> {
  return request<AskResponse>('/api/ask', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ question }),
  })
}

/**
 * 按材料详情 + rune 偏移区间构造高亮片段（R3.2：无预处理时偏移即原文字符下标）。
 * 后端切片正文存的是无预处理原文，这里按码点切分对齐（tasks 9.2）。
 */
export function highlightSlice(body: string, start: number, end: number): { before: string; mark: string; after: string } {
  const runes = Array.from(body) // 按码点拆，与后端 rune 偏移同一语义
  const s = Math.max(0, Math.min(start, runes.length))
  const e = Math.max(s, Math.min(end, runes.length))
  return {
    before: runes.slice(0, s).join(''),
    mark: runes.slice(s, e).join(''),
    after: runes.slice(e).join(''),
  }
}

// ══════════════════════════════════════════════════════════════
// 第 5 课 · 解题助手（提示词 / 技能 / 智能体 / 会话与 SSE）
// ══════════════════════════════════════════════════════════════

/** 与 `request` 相同，但容忍 204/空响应体（DELETE 用）。 */
async function requestNoContent(path: string, init: RequestInit = {}): Promise<void> {
  if (accessExpiring()) await refreshTokens()
  let res = await rawFetch(path, init)
  if (res.status === 401) {
    const ok = await refreshTokens()
    if (ok) res = await rawFetch(path, init)
  }
  if (!res.ok) throw new ApiError(res.status, await readError(res))
}

// ── 提示词（班级级，一班一行）──────────────────────────────────

/**
 * 提示词规定助手**是谁**（角色）；学生可读、仅教师可写。
 * 未设置时服务端返回空串（不是 404）—— 界面据此显示空输入框。
 */
export function getPrompt(): Promise<{ body: string }> {
  return request<{ body: string }>('/api/prompt')
}

/** `PUT /api/prompt` —— 仅教师。去掉空白后 1~2000 字符。 */
export function putPrompt(body: string): Promise<{ body: string }> {
  return request<{ body: string }>('/api/prompt', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ body }),
  })
}

// ── 技能 ──────────────────────────────────────────────────────

export interface Skill {
  id: number
  name: string
  /** 「适用条件」：词表，路由时按分隔符切词做子串匹配。 */
  trigger: string
  /** 「资料」：学科范围；非空时只保留标题含该词的材料。 */
  subject: string
  /** 本期只允许 `knowledge_search`。 */
  tools: string[]
  enabled: boolean
  /** 技能文件里空行以下的正文。 */
  description: string
  /** 技能文件原文（头部五键 + 空行 + 正文）。 */
  source: string
}

export function listSkills(): Promise<{ skills: Skill[] }> {
  return request<{ skills: Skill[] }>('/api/skills')
}

/**
 * `POST /api/skills` —— 仅教师。请求体是**技能文件原文**，
 * 服务端解析五键（名称/适用条件/工具/资料/启用）+ 空行以下正文。
 */
export function createSkill(source: string): Promise<Skill> {
  return request<Skill>('/api/skills', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ source }),
  })
}

export function updateSkill(id: number, source: string): Promise<Skill> {
  return request<Skill>(`/api/skills/${id}`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ source }),
  })
}

export function deleteSkill(id: number): Promise<void> {
  return requestNoContent(`/api/skills/${id}`, { method: 'DELETE' })
}

/**
 * `POST /api/skills/upload` —— 直接上传 `.md` / `.txt` 技能文件（multipart，字段名 `file`）。
 *
 * 与 `createSkill` 走**同一个解析器**（服务端 `parseSkillFile`）：上传只是换个投递方式，
 * 不另立规则。所以前端不需要校验格式——解析失败会以 `bad_request` + `detail` 返回，
 * 直接把 detail 显示出来即可，比前端猜规则更准。
 */
export function uploadSkillFile(file: File): Promise<Skill> {
  const form = new FormData()
  form.append('file', file, file.name)
  // 不设 Content-Type：交给浏览器带 boundary
  return request<Skill>('/api/skills/upload', { method: 'POST', body: form })
}

// ── 智能体（一个角色 = 名称 + 提示词 + 一组技能 + 一组材料）──────

export interface Agent {
  id: number
  name: string
  prompt: string
  enabled: boolean
  skill_ids: number[]
  material_ids: number[]
  /** 绑定的提示词模板 id；0 表示不绑（只用上面的 `prompt`）。 */
  template_id: number
  /** 模板 `{{变量}}` 的取值表。 */
  template_vars: Record<string, string>
}

export interface AgentBody {
  name: string
  prompt: string
  enabled: boolean
  skill_ids: number[]
  material_ids: number[]
  template_id: number
  template_vars: Record<string, string>
}

/** 教师看到本班全部；学生只看已启用的。 */
export function listAgents(): Promise<{ agents: Agent[] }> {
  return request<{ agents: Agent[] }>('/api/agents')
}

export function createAgent(body: AgentBody): Promise<Agent> {
  return request<Agent>('/api/agents', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
}

export function updateAgent(id: number, body: AgentBody): Promise<Agent> {
  return request<Agent>(`/api/agents/${id}`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
}

/**
 * `DELETE /api/agents/{id}` —— 删除智能体。
 *
 * 已被会话引用的会返回 409：这种情况不该硬删，历史会话还要能回看。
 * 服务端在 detail 里说明「停用即可」，这里原样转述给用户。
 */
export function deleteAgent(id: number): Promise<void> {
  return requestNoContent(`/api/agents/${id}`, { method: 'DELETE' })
}

// ── 会话 ──────────────────────────────────────────────────────

export interface Conversation {
  id: number
  user_id: number
  class_id: number
  agent_id: number
  title: string
  created_at: string
  updated_at: string
}

/** 会话里的一条消息；`citations` 只对助手消息有意义。 */
export interface ConversationMessage {
  id: number
  role: 'user' | 'assistant'
  content: string
  citations: SearchHit[]
  skill_name: string
  created_at: string
}

/** 只会列出**自己**的会话，按最近问过排序。 */
export function listConversations(): Promise<{ conversations: Conversation[] }> {
  return request<{ conversations: Conversation[] }>('/api/conversations')
}

/**
 * `POST /api/conversations` —— 必须选本班**已启用**的智能体。
 * 跨班 / 未启用 / 不存在 → 404（与「不存在」同形）。
 */
export function createConversation(agentId: number): Promise<Conversation> {
  return request<Conversation>('/api/conversations', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ agent_id: agentId }),
  })
}

/** `GET /api/conversations/{id}` —— 非本人的会话与不存在同为 404。 */
export function getConversation(
  id: number,
): Promise<{ conversation: Conversation; messages: ConversationMessage[] }> {
  return request<{ conversation: Conversation; messages: ConversationMessage[] }>(`/api/conversations/${id}`)
}

// ── SSE 流式提问 ──────────────────────────────────────────────

/** `meta` 事件载荷：状态恒为 `ok`，有没有依据看 `citations` 是否为空。 */
export interface StreamMeta {
  status: string
  /** 本次命中的技能名；未命中为空串。 */
  skill: string
  citations: SearchHit[]
}

export interface StreamHandlers {
  onMeta?: (meta: StreamMeta) => void
  onDelta?: (text: string) => void
  onDone?: (info: { message_id: number }) => void
  /** 流**开始后**才可能出现的错误事件（HTTP 仍是 200）。 */
  onStreamError?: (message: string) => void
}

/**
 * `POST /api/conversations/{id}/messages` —— 流式提问。
 *
 * 用 fetch + ReadableStream 而不是 EventSource：EventSource **不能带
 * Authorization 头**，而本接口要 Bearer 令牌。
 *
 * 线格式（官方契约）：`event: meta` → `event: delta`* → `event: done`；
 * 流开始前失败走 HTTP 状态码（401/400/404/503），开始后失败写 `event: error`
 * 但 HTTP 保持 200 —— 所以两条路都要处理。
 *
 * 传 `signal` 可取消（用户点「停止」）。取消时抛出的 AbortError 由调用方识别。
 */
export async function streamAsk(
  convId: number,
  content: string,
  h: StreamHandlers,
  signal?: AbortSignal,
): Promise<void> {
  const path = `/api/conversations/${convId}/messages`
  const init: RequestInit = {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ content }),
    signal,
  }

  if (accessExpiring()) await refreshTokens()
  let res = await rawFetch(path, init)
  // 流还没开始，401 可安全重放（这时还没写入任何消息）
  if (res.status === 401 && (await refreshTokens())) {
    res = await rawFetch(path, init)
  }
  if (!res.ok) throw new ApiError(res.status, await readError(res))
  if (!res.body) throw new ApiError(500, '响应没有流式主体')

  const reader = res.body.getReader()
  const decoder = new TextDecoder('utf-8')
  let buf = ''
  let curEvent = ''
  let dataLines: string[] = []

  const dispatch = (): void => {
    if (dataLines.length === 0) return
    const raw = dataLines.join('\n')
    dataLines = []
    let payload: unknown = null
    try {
      payload = JSON.parse(raw) as unknown
    } catch {
      return // 非法 JSON：忽略该事件（不应发生）
    }
    if (curEvent === 'meta') h.onMeta?.(payload as StreamMeta)
    else if (curEvent === 'delta') h.onDelta?.((payload as { text: string }).text ?? '')
    else if (curEvent === 'done') h.onDone?.(payload as { message_id: number })
    else if (curEvent === 'error') {
      h.onStreamError?.((payload as { error?: string }).error ?? '生成中断')
    }
    curEvent = ''
  }

  for (;;) {
    const { done, value } = await reader.read()
    if (done) break
    buf += decoder.decode(value, { stream: true })
    let nl: number
    while ((nl = buf.indexOf('\n')) >= 0) {
      const line = buf.slice(0, nl).replace(/\r$/, '')
      buf = buf.slice(nl + 1)
      if (line === '') dispatch()
      else if (line.startsWith('event:')) curEvent = line.slice('event:'.length).trim()
      else if (line.startsWith('data:')) {
        let v = line.slice('data:'.length)
        if (v.startsWith(' ')) v = v.slice(1)
        dataLines.push(v)
      }
    }
  }
  buf += decoder.decode()
  dispatch()
}

// ══════════════════════════════════════════════════════════════
// 平台能力补齐 · 提示词模板 / 技能上传 / MCP 工具接入
// ══════════════════════════════════════════════════════════════

// ── 提示词模板库 ──────────────────────────────────────────────

/**
 * 一份可复用的提示词骨架。正文里写 `{{变量}}`，绑定到智能体后每轮按变量表渲染。
 *
 * 与班级提示词（`getPrompt`/`putPrompt`，一班一条）不是一回事：
 * 班级提示词是兜底的那一条，模板是若干份可选骨架，可以并存多条。
 */
export interface PromptTemplate {
  id: number
  name: string
  description: string
  body: string
  /** 正文里出现过的占位符（服务端 `ExtractVars` 算出，按首次出现去重）。 */
  variables: string[]
  created_at?: string
}

export interface TemplateBody {
  name: string
  description: string
  body: string
}

export function listTemplates(): Promise<{ templates: PromptTemplate[] }> {
  return request<{ templates: PromptTemplate[] }>('/api/prompt-templates')
}

export function createTemplate(b: TemplateBody): Promise<PromptTemplate> {
  return request<PromptTemplate>('/api/prompt-templates', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(b),
  })
}

export function updateTemplate(id: number, b: TemplateBody): Promise<PromptTemplate> {
  return request<PromptTemplate>(`/api/prompt-templates/${id}`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(b),
  })
}

export function deleteTemplate(id: number): Promise<void> {
  return requestNoContent(`/api/prompt-templates/${id}`, { method: 'DELETE' })
}

/**
 * `POST /api/prompt-templates/{id}/render` —— 不调模型，先看模板渲染成什么样。
 *
 * `unfilled` 是渲染后**仍然剩下**的占位符：服务端刻意不删未提供的变量，
 * 让「漏填」在报文里看得见，而不是被悄悄抹平。
 */
export function renderTemplate(
  id: number,
  variables: Record<string, string>,
): Promise<{ rendered: string; unfilled: string[] }> {
  return request<{ rendered: string; unfilled: string[] }>(`/api/prompt-templates/${id}/render`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ variables }),
  })
}

// ── MCP 外部工具 ──────────────────────────────────────────────

export interface McpToolInfo {
  name: string
  description: string
  /** 工具入参的 JSON Schema，原样来自 MCP 服务器的 tools/list。 */
  input_schema: unknown
}

/**
 * 一台已登记的 MCP 服务器。
 *
 * **`auth_token` 永不下发**：服务端只回 `has_auth` 布尔值。
 * 所以编辑时"留空"表示不改变已存的令牌，而不是清空——清空要显式点按钮。
 */
export interface McpServer {
  id: number
  name: string
  base_url: string
  has_auth: boolean
  enabled: boolean
  tools: McpToolInfo[]
  tool_count: number
  created_at?: string
}

/** 一条工具调用审计记录（成功与失败都留痕）。 */
export interface McpCall {
  id: number
  conversation_id: number
  skill_name: string
  server_name: string
  tool_name: string
  arguments: string
  result: string
  status: 'ok' | 'error'
  error_text?: string
  duration_ms: number
  created_at: string
}

export interface McpServerBody {
  name: string
  base_url: string
  /** 不传 = 保留原有令牌；传空串 = 清空。新建时不传即"无需鉴权"。 */
  auth_token?: string
  enabled?: boolean
}

export function listMcpServers(): Promise<{ servers: McpServer[] }> {
  return request<{ servers: McpServer[] }>('/api/mcp/servers')
}

export function createMcpServer(b: McpServerBody): Promise<McpServer> {
  return request<McpServer>('/api/mcp/servers', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(b),
  })
}

export function updateMcpServer(id: number, b: McpServerBody): Promise<McpServer> {
  return request<McpServer>(`/api/mcp/servers/${id}`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(b),
  })
}

export function deleteMcpServer(id: number): Promise<void> {
  return requestNoContent(`/api/mcp/servers/${id}`, { method: 'DELETE' })
}

/**
 * `POST /api/mcp/servers/{id}/connect` —— 打 initialize + tools/list，把工具清单缓存进库。
 *
 * 失败是 502 `mcp_unreachable`（服务器没起、地址不对、令牌不对都会走这里）。
 * 技能里写的 `工具名@服务器名` 只有在**连接成功之后**才解析得到，
 * 没连接就等于这个工具不存在。
 */
export function connectMcpServer(
  id: number,
): Promise<{ server: string; protocol: string; tools: McpToolInfo[]; count: number }> {
  return request<{ server: string; protocol: string; tools: McpToolInfo[]; count: number }>(
    `/api/mcp/servers/${id}/connect`,
    { method: 'POST' },
  )
}

export function listMcpCalls(limit = 50): Promise<{ tool_calls: McpCall[] }> {
  return request<{ tool_calls: McpCall[] }>(`/api/mcp/tool-calls?limit=${limit}`)
}
