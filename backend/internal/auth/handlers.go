package auth

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/Corday-nkusky/campusclaw/backend/internal/obslog"
)

// uniformFailure 是所有登录失败**唯一**的响应体。
//
// 账号不存在、口令错误、被限流三种情况都返回它，且状态码统一为 401。
// 刻意**不使用 429**：429 等于告诉攻击者「这个账号存在且已被锁定」（spec.md R1.2）。
const uniformFailure = `{"error":"invalid_credentials"}`

// BearerPrefix 是 Authorization 头要求的方案前缀（spec.md R1 / design.md D4）。
const BearerPrefix = "Bearer "

// uniformUnauthorized 让所有 401 的状态码、响应体、challenge 头完全同形。
// WWW-Authenticate: Bearer 关闭了迭代 1 记录的 RFC 9110 §15.5.2 偏差（design.md D8）。
func uniformUnauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = io.WriteString(w, `{"error":"unauthorized"}`)
}

// Handlers 汇总认证相关的 HTTP 处理器。
type Handlers struct {
	pool       *sql.DB
	secret     []byte
	accessTTL  time.Duration // access token 有效期（add-refresh-token）
	refreshTTL time.Duration // refresh token 有效期
	limiter    *Limiter
	now        func() time.Time // 可注入的时钟（测试用）
}

// NewHandlers 构造认证处理器。
//
// secret 即 SESSION_SECRET（JWT HMAC 签名密钥），
// accessTTL 即 ACCESS_TTL_MINUTES（或 SESSION_TTL_HOURS 的回退值），
// refreshTTL 即 REFRESH_TTL_DAYS。
func NewHandlers(pool *sql.DB, secret []byte, accessTTL, refreshTTL time.Duration, limiter *Limiter) *Handlers {
	return &Handlers{
		pool:       pool,
		secret:     secret,
		accessTTL:  accessTTL,
		refreshTTL: refreshTTL,
		limiter:    limiter,
		now:        time.Now,
	}
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// Login 处理 POST /api/login。
//
// 成功后**无状态签发** JWT 放进响应体（design.md D2）：服务端不写任何会话行，
// 后续请求靠验签 + 查用户表识别身份。
func (h *Handlers) Login(w http.ResponseWriter, r *http.Request) {
	// 登录请求体很小，限制读取量，避免被超大 body 拖住
	body := http.MaxBytesReader(w, r.Body, 4<<10)
	defer func() { _ = body.Close() }()

	var req loginRequest
	if err := json.NewDecoder(body).Decode(&req); err != nil {
		h.failLogin(w, nil, "", "") // 解析失败也走同形响应，不透露细节
		return
	}

	username := strings.TrimSpace(req.Username)
	rateKey := username + "|" + clientIP(r)

	// 限流命中：仍走一次等价耗时，并返回与凭据错误完全相同的响应
	if h.limiter.Locked(rateKey) {
		burnTime(req.Password)
		h.failLogin(w, nil, rateKey, "")
		return
	}

	var (
		userID   int64
		hash     string
		roleName string
	)
	err := h.pool.QueryRow(
		`SELECT id, password_hash, role FROM users WHERE username = ?`, username,
	).Scan(&userID, &hash, &roleName)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		// 用户不存在：**也做一次等价耗时的哈希比较**，让耗时与口令错误不可区分
		burnTime(req.Password)
		h.failLogin(w, nil, rateKey, "")
		return
	case err != nil:
		obslog.ErrorCtx(r.Context(), "auth.login_query_failed", "登录查询失败", "detail", err.Error())
		h.failLogin(w, nil, rateKey, "")
		return
	}

	if !VerifyPassword(hash, req.Password) {
		h.failLogin(w, nil, rateKey, "")
		return
	}

	// ── 登录成功：签发 access + refresh（add-refresh-token design D1/D3）──
	h.limiter.Succeed(rateKey)

	access, err := SignToken(h.secret, userID, h.accessTTL, h.now())
	if err != nil {
		obslog.ErrorCtx(r.Context(), "auth.access_sign_failed", "签发 access token 失败", "detail", err.Error())
		http.Error(w, `{"error":"internal_error"}`, http.StatusInternalServerError)
		return
	}

	refresh, err := NewRefresh()
	if err != nil {
		obslog.ErrorCtx(r.Context(), "auth.refresh_create_failed", "生成 refresh token 失败", "detail", err.Error())
		http.Error(w, `{"error":"internal_error"}`, http.StatusInternalServerError)
		return
	}
	if err := InsertRefresh(r.Context(), h.pool, userID, refresh, h.refreshTTL, h.now()); err != nil {
		obslog.ErrorCtx(r.Context(), "auth.refresh_store_failed", "写入 refresh token 失败", "detail", err.Error())
		http.Error(w, `{"error":"internal_error"}`, http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":            true,
		"access_token":  access,
		"refresh_token": refresh,
		"token_type":    "bearer",
		"expires_in":    int64(h.accessTTL.Seconds()),
	})
}

// failLogin 记录一次失败并返回统一响应。rateKey 为空时不计数（如请求体解析失败）。
func (h *Handlers) failLogin(w http.ResponseWriter, _ *http.Request, rateKey, _ string) {
	if rateKey != "" {
		h.limiter.Fail(rateKey)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = io.WriteString(w, uniformFailure)
}

// Logout 处理 POST /api/logout。
//
// add-refresh-token 起**语义反转**：不再是"无状态 200"（旧 add-token-auth design D2），
// 而是**真的删库** —— 删除请求体里 refresh 对应的记录，使该 refresh 再也换不出新 access。
//
// 边界（design D2，写实不粉饰）：已签发的 access 在其余下 exp 内**仍然有效**，
// 最长再存活一个 access TTL（默认 15 分钟）。要做到 access 也即时失效，
// 必须让每个业务请求查吊销表，那等于放弃 access 的无状态性 —— 本轮不做。
//
// 幂等：refresh 缺失或删不到，仍返回 200。
func (h *Handlers) Logout(w http.ResponseWriter, r *http.Request) {
	body := http.MaxBytesReader(w, r.Body, 4<<10)
	defer func() { _ = body.Close() }()

	var req refreshRequest
	if err := json.NewDecoder(body).Decode(&req); err != nil {
		// 请求体不可解析：无可删的 refresh，按幂等登出处理
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	if req.RefreshToken != "" {
		if err := DeleteRefresh(r.Context(), h.pool, req.RefreshToken); err != nil {
			obslog.ErrorCtx(r.Context(), "auth.refresh_delete_failed", "登出时删除 refresh token 失败", "detail", err.Error())
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// refreshRequest 是 /api/refresh 与 /api/logout 的请求体。
type refreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

// Refresh 处理 POST /api/refresh（add-refresh-token design D5）。
//
// 凭有效 refresh 换发新的 access + **轮换后的新 refresh**。
// **不要求** Authorization 头 —— 用户来刷新时 access 大概率已过期。
//
// 失败（不存在 / 过期 / 已吊销 / 重放）统一 401 + WWW-Authenticate: Bearer。
// 其中"重放"由 ConsumeRefresh 内部完成止损（吊销该用户全部 refresh）。
func (h *Handlers) Refresh(w http.ResponseWriter, r *http.Request) {
	body := http.MaxBytesReader(w, r.Body, 4<<10)
	defer func() { _ = body.Close() }()

	var req refreshRequest
	if err := json.NewDecoder(body).Decode(&req); err != nil || req.RefreshToken == "" {
		h.refreshUnauthorized(w)
		return
	}

	userID, newRefresh, err := ConsumeRefresh(r.Context(), h.pool, req.RefreshToken, h.refreshTTL, h.now())
	if err != nil {
		if !errors.Is(err, ErrRefreshInvalid) {
			obslog.ErrorCtx(r.Context(), "auth.refresh_failed", "刷新令牌失败", "detail", err.Error())
		}
		h.refreshUnauthorized(w)
		return
	}

	access, err := SignToken(h.secret, userID, h.accessTTL, h.now())
	if err != nil {
		obslog.ErrorCtx(r.Context(), "auth.refresh_access_sign_failed", "刷新时签发 access token 失败", "detail", err.Error())
		http.Error(w, `{"error":"internal_error"}`, http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"access_token":  access,
		"refresh_token": newRefresh,
		"token_type":    "bearer",
		"expires_in":    int64(h.accessTTL.Seconds()),
	})
}

// refreshUnauthorized 让刷新失败与业务接口 401 同形（含 challenge 头）。
func (h *Handlers) refreshUnauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = io.WriteString(w, `{"error":"invalid_refresh"}`)
}

// Me 处理 GET /api/me：返回当前角色与班级，供前端决定是否渲染上传入口。
//
// 必须经 RequireSession 包裹后使用。
func (h *Handlers) Me(w http.ResponseWriter, r *http.Request) {
	sess, ok := FromContext(r.Context())
	if !ok {
		uniformUnauthorized(w)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user_id":    sess.UserID,
		"username":   sess.Username,
		"role":       sess.Role,
		"class_id":   sess.ClassID,
		"class_name": sess.ClassName,
	})
}

// RequireSession 是鉴权中间件：未登录一律 401，且响应体不含任何业务数据。
//
// 流程（design.md D1/D3）：取 Authorization: Bearer → 验签与 exp →
// 拿 payload 里的 user_id 查 users JOIN classes —— role / class_id **每次现读**，
// token 与请求里的其他任何值都不采信；用户已被删除则 401（spec.md R2.2）。
//
// 「仅重定向到登录页」不构成访问控制（spec.md R9.2）：受保护接口必须 401，
// 且响应体不得出现材料标题、正文或磁盘路径。
func (h *Handlers) RequireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearerToken(r)
		if !ok {
			uniformUnauthorized(w)
			return
		}
		userID, err := ParseToken(h.secret, token, h.now())
		if err != nil {
			uniformUnauthorized(w)
			return
		}
		sess, err := loadIdentity(h.pool, userID)
		if err != nil {
			// 用户不存在 / 已删除，与验签失败对外同形
			uniformUnauthorized(w)
			return
		}
		next.ServeHTTP(w, r.WithContext(withSession(r.Context(), sess)))
	})
}

// bearerToken 从 Authorization 头解析 Bearer 凭证。
//
// 头缺失、方案不是 Bearer、token 为空或内含空白都视为未提供。
func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, BearerPrefix) {
		return "", false
	}
	token := strings.TrimSpace(strings.TrimPrefix(h, BearerPrefix))
	if token == "" || strings.ContainsAny(token, " \t\r\n") {
		return "", false
	}
	return token, true
}

// loadIdentity 按 user_id 现读身份（role / class_id 不来自 token，design.md D1）。
func loadIdentity(pool *sql.DB, userID int64) (*Session, error) {
	const q = `
		SELECT u.id, u.username, u.role, u.class_id, c.name
		FROM users u
		JOIN classes c ON c.id = u.class_id
		WHERE u.id = ?`

	var sess Session
	err := pool.QueryRow(q, userID).Scan(
		&sess.UserID, &sess.Username, &sess.Role, &sess.ClassID, &sess.ClassName,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoSession
	}
	if err != nil {
		return nil, err
	}
	return &sess, nil
}

// RequireTeacher 是授权中间件：仅教师可通过（学生调用返回 403）。
//
// 垂直越权（BFLA）的防线在这里：前端隐藏按钮只降低可发现性，不构成访问控制。
func RequireTeacher(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess, ok := FromContext(r.Context())
		if !ok {
			uniformUnauthorized(w)
			return
		}
		if sess.Role != "teacher" {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "forbidden"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// clientIP 取客户端地址，供限流键使用。
//
// 容器部署时请求经 Nginx 转发，RemoteAddr 会是反向代理地址，
// 因此优先采信 X-Forwarded-For 的第一个值。
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if first, _, found := strings.Cut(xff, ","); found {
			return strings.TrimSpace(first)
		}
		return strings.TrimSpace(xff)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
