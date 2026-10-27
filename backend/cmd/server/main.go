// CampusClaw 迭代 1 · API 服务入口。
//
// 启动顺序：
//
//	读配置（缺必填项即退出，R7.1）→ 建连接池 → **先起 HTTP 服务** → 后台等 DB 就绪 → 建表 → 幂等种子（失败则重试）
//
// ⚠️ 数据库这一段**不是致命路径**，而且**不得阻塞监听**：R8.4 要求 DB 不可用时
// 进程仍存活、`/health` 仍返回 200，由业务接口返回 503。
//
// 这里踩过一次真实的坑（2026-09-23 实测）：最初把 `bootstrap` 放在
// `ListenAndServe` 之前，于是 DB 不可达时 `WaitReady` 按 1+2+3+4+5×26 秒退避
// 重试共约 140 秒，这 140 秒里 8080 **连 TCP 都建立不起来** —— 实测启动 6 秒后
// `curl /health` 得到 `000`（连接被拒），而不是期望的 200。
// 编排系统会把这判成容器不健康并重启，正好放大故障，与 design.md D9 的意图相反。
// → 结论：**先监听，再在后台补建表与种子**。
//
// 判定链路（design.md 技术栈决议）：
//
//	浏览器 → Nginx → 本进程 → MySQL，信任边界落在本进程。
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Corday-nkusky/campusclaw/backend/internal/agents"
	"github.com/Corday-nkusky/campusclaw/backend/internal/auth"
	"github.com/Corday-nkusky/campusclaw/backend/internal/config"
	"github.com/Corday-nkusky/campusclaw/backend/internal/conversation"
	"github.com/Corday-nkusky/campusclaw/backend/internal/db"
	"github.com/Corday-nkusky/campusclaw/backend/internal/docs"
	"github.com/Corday-nkusky/campusclaw/backend/internal/knowledge"
	"github.com/Corday-nkusky/campusclaw/backend/internal/materials"
	"github.com/Corday-nkusky/campusclaw/backend/internal/mcp"
	"github.com/Corday-nkusky/campusclaw/backend/internal/obslog"
	"github.com/Corday-nkusky/campusclaw/backend/internal/prompt"
	"github.com/Corday-nkusky/campusclaw/backend/internal/prompttpl"
	"github.com/Corday-nkusky/campusclaw/backend/internal/skills"
)

func main() {
	// 日志从第一行起就按规范输出（internal/obslog）：一行一条 JSON、字段命名统一。
	// 这里不再设置标准库 log 的 flag —— 本进程不再往 stdout 写非 JSON 文本。

	// ── 1. 配置：缺失即失败，不落默认值 ───────────────────────
	cfg, err := config.Load()
	if err != nil {
		obslog.Error("startup.config_invalid", "配置错误，拒绝启动", "detail", err.Error())
		os.Exit(1)
	}
	obslog.Info("startup.config_loaded", "",
		"db_host", cfg.DBHost, "db_port", cfg.DBPort, "db_name", cfg.DBName,
		"max_upload_bytes", cfg.MaxUploadBytes,
		"access_ttl", cfg.AccessTTL.String(), "refresh_ttl", cfg.RefreshTTL.String())

	// ── 2. 数据库（非致命路径，见文件头说明）──────────────────
	pool, err := db.OpenPool(cfg)
	if err != nil {
		// 建池失败只可能是 DSN 形态错误，属配置问题，仍应退出
		obslog.Error("startup.db_dsn_invalid", "数据库配置错误，拒绝启动", "detail", err.Error())
		os.Exit(1)
	}
	defer func() { _ = pool.Close() }()

	// 存活探测器：/api/* 在 DB 不可用时返回 503（R8.4）
	monitor := db.NewMonitor(pool)
	monitor.Start(2 * time.Second)

	// ── 3. 处理器 ─────────────────────────────────────────────
	// 认证为「access JWT + refresh 落库」双令牌（add-refresh-token design D1/D3）：
	// access 无状态、TTL 短；refresh 存 refresh_tokens 表，可吊销。
	authH := auth.NewHandlers(pool, cfg.SessionSecret, cfg.AccessTTL, cfg.RefreshTTL,
		auth.NewLimiter(cfg.LoginFailThreshold, cfg.LoginLockFor))
	matH := materials.NewHandlers(pool, cfg.UploadDir, cfg.MaxUploadBytes)

	// 检索依赖（add-class-scoped-retrieval design D9）
	srchEmb := knowledge.NewEmbedder(cfg.EmbeddingAPIBase, cfg.EmbeddingAPIKey, cfg.EmbeddingModel, cfg.EmbeddingDim)
	srchQd := knowledge.NewQdrant(cfg.QdrantURL, cfg.QdrantCollection, cfg.EmbeddingDim)
	indexer := knowledge.NewIndexer(pool, srchEmb, srchQd)
	matH.SetIndexer(indexer)
	srchDeps := &knowledge.SearchDeps{Pool: pool, Emb: srchEmb, Qd: srchQd}
	askDeps := &knowledge.AskDeps{Pool: pool, Srch: srchDeps, Chat: knowledge.NewChatClient(cfg.ChatAPIBase, cfg.ChatAPIKey, cfg.ChatModel)}

	// 第 5 课：提示词 / 技能 / 智能体 / 会话（对外形态对齐官方 /week05/）
	promptH := prompt.NewHandlers(pool)
	skillsH := skills.NewHandlers(pool)
	agentsH := agents.NewHandlers(pool)
	tplH := prompttpl.NewHandlers(pool)
	mcpH := mcp.NewHandlers(pool)
	convH := conversation.NewHandlers(pool, srchDeps, knowledge.NewChatClient(cfg.ChatAPIBase, cfg.ChatAPIKey, cfg.ChatModel)).WithMCP(mcpH)

	mux := http.NewServeMux()

	// 认证
	mux.HandleFunc("POST /api/login", authH.Login)
	mux.HandleFunc("POST /api/logout", authH.Logout)
	// /api/refresh 刻意**不套 RequireSession**：用户来刷新时 access 多半已过期。
	mux.HandleFunc("POST /api/refresh", authH.Refresh)
	mux.Handle("GET /api/me", authH.RequireSession(http.HandlerFunc(authH.Me)))

	// 材料（步 4 班级隔离、步 5 上传入库均已实现）
	guarded := authH.RequireSession
	mux.Handle("GET /api/materials", guarded(http.HandlerFunc(matH.List)))
	mux.Handle("GET /api/materials/{id}", guarded(http.HandlerFunc(matH.Detail)))
	mux.Handle("GET /api/materials/{id}/file", guarded(http.HandlerFunc(matH.File)))
	mux.Handle("POST /api/materials", guarded(auth.RequireTeacher(http.HandlerFunc(matH.Upload))))

	// 检索与问答（add-class-scoped-retrieval R1/R6）
	mux.Handle("POST /api/search", guarded(http.HandlerFunc(srchDeps.Search)))
	mux.Handle("POST /api/ask", guarded(http.HandlerFunc(askDeps.Ask)))
	mux.Handle("POST /api/materials/{id}/reindex", guarded(auth.RequireTeacher(http.HandlerFunc(indexer.Reindex))))

	// 第 5 课：提示词（PRM-001/002）
	mux.Handle("GET /api/prompt", guarded(http.HandlerFunc(promptH.Get)))
	mux.Handle("PUT /api/prompt", guarded(auth.RequireTeacher(http.HandlerFunc(promptH.Put))))

	// 第 5 课：技能（SKL-001~004）+ 文件上传（个人项目补齐项）
	mux.Handle("GET /api/skills", guarded(http.HandlerFunc(skillsH.List)))
	mux.Handle("POST /api/skills", guarded(auth.RequireTeacher(http.HandlerFunc(skillsH.Create))))
	mux.Handle("POST /api/skills/upload", guarded(auth.RequireTeacher(http.HandlerFunc(skillsH.Upload))))
	mux.Handle("PUT /api/skills/{id}", guarded(auth.RequireTeacher(http.HandlerFunc(skillsH.Update))))
	mux.Handle("DELETE /api/skills/{id}", guarded(auth.RequireTeacher(http.HandlerFunc(skillsH.Delete))))

	// 提示词模板库（个人项目补齐项）
	mux.Handle("GET /api/prompt-templates", guarded(http.HandlerFunc(tplH.List)))
	mux.Handle("POST /api/prompt-templates", guarded(auth.RequireTeacher(http.HandlerFunc(tplH.Create))))
	mux.Handle("POST /api/prompt-templates/{id}/render", guarded(http.HandlerFunc(tplH.Render)))
	mux.Handle("PUT /api/prompt-templates/{id}", guarded(auth.RequireTeacher(http.HandlerFunc(tplH.Update))))
	mux.Handle("DELETE /api/prompt-templates/{id}", guarded(auth.RequireTeacher(http.HandlerFunc(tplH.Delete))))

	// 外部工具与 MCP 接入（个人项目补齐项，对齐第 6 课 MCP-001~008）
	mux.Handle("GET /api/mcp/servers", guarded(http.HandlerFunc(mcpH.List)))
	mux.Handle("POST /api/mcp/servers", guarded(auth.RequireTeacher(http.HandlerFunc(mcpH.Create))))
	mux.Handle("PUT /api/mcp/servers/{id}", guarded(auth.RequireTeacher(http.HandlerFunc(mcpH.Update))))
	mux.Handle("DELETE /api/mcp/servers/{id}", guarded(auth.RequireTeacher(http.HandlerFunc(mcpH.Delete))))
	mux.Handle("POST /api/mcp/servers/{id}/connect", guarded(auth.RequireTeacher(http.HandlerFunc(mcpH.Connect))))
	mux.Handle("GET /api/mcp/tool-calls", guarded(http.HandlerFunc(mcpH.ToolCalls)))

	// 第 5 课：智能体（AGT-001~004）
	mux.Handle("GET /api/agents", guarded(http.HandlerFunc(agentsH.List)))
	mux.Handle("POST /api/agents", guarded(auth.RequireTeacher(http.HandlerFunc(agentsH.Create))))
	mux.Handle("GET /api/agents/{id}", guarded(http.HandlerFunc(agentsH.Get)))
	mux.Handle("PUT /api/agents/{id}", guarded(auth.RequireTeacher(http.HandlerFunc(agentsH.Update))))
	mux.Handle("DELETE /api/agents/{id}", guarded(auth.RequireTeacher(http.HandlerFunc(agentsH.Delete))))

	// 第 5 课：会话与流式问答（CONV-001~004）
	mux.Handle("GET /api/conversations", guarded(http.HandlerFunc(convH.List)))
	mux.Handle("POST /api/conversations", guarded(http.HandlerFunc(convH.Create)))
	mux.Handle("GET /api/conversations/{id}", guarded(http.HandlerFunc(convH.Get)))
	mux.Handle("POST /api/conversations/{id}/messages", guarded(http.HandlerFunc(convH.Ask)))

	// 第 5 课：OpenAPI 文档与 Swagger UI（DOC-001）
	mux.HandleFunc("GET /openapi.json", docs.ServeOpenAPI)
	mux.HandleFunc("GET /swagger/index.html", docs.ServeSwaggerUI)
	// 课件 apply 页要求「打开 /swagger 能对照文档试调」——两条都重定向到 UI 页。
	mux.HandleFunc("GET /swagger", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/swagger/index.html", http.StatusFound)
	})
	mux.HandleFunc("GET /swagger/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/swagger/index.html", http.StatusFound)
	})

	// 存活判定：**不查库**（design.md D9）。
	//
	// 存活与就绪混在一个端点时，DB 短暂抖动会被编排系统误判为容器故障并重启，
	// 反而放大故障。DB 不可用时由业务接口返回 503，而本端点仍应 200。
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	// ── 4. 起服务 ─────────────────────────────────────────────
	srv := &http.Server{
		Addr: ":" + cfg.APIPort,
		// 请求标识中间件放最外层：这样连 requireDB 拒绝的请求也有 request_id
		Handler:           requestID(logRequests(requireDB(monitor, mux))),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		obslog.Info("server.listen", "", "addr", ":"+cfg.APIPort)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			obslog.Error("server.listen_failed", "HTTP 服务异常退出", "detail", err.Error())
			os.Exit(1)
		}
	}()

	// ── 5. 建表与种子：放后台，**不得阻塞监听** ───────────────
	//
	// 顺序很关键：HTTP 服务必须先起来。DB 不可达时 WaitReady 会退避重试
	// 约 140 秒，若把这段放在 ListenAndServe 之前，这 140 秒里连 /health
	// 都连不上，编排系统会判容器不健康并重启（实测得到 curl 000）。
	go func() {
		if err := bootstrap(pool, cfg); err != nil {
			obslog.Error("db.bootstrap_deferred", "数据库暂不可用，/health 已可应答 200，业务接口转 503，转后台重试",
				"detail", err.Error(), "retry_every", "5s")
			retryBootstrap(pool, cfg)
			return
		}
		obslog.Info("db.bootstrap_done", "")

		// ── 检索依赖的后置初始化（不阻塞监听，失败不致命）──────────
		// 1.3 的维度校验：集合已存在且维度 ≠ EMBEDDING_DIM → 启动失败并指明
		// （这是配置错误，不是依赖抖动）。Qdrant 暂不可达只告警，走 R7.1 降级。
		if err := indexer.EnsureCollection(context.Background()); err != nil {
			if errors.Is(err, knowledge.ErrDimMismatch) {
				obslog.Error("startup.embedding_dim_mismatch",
					"嵌入维度与 Qdrant 集合不一致，拒绝启动",
					"detail", err.Error(), "hint", "更换嵌入模型后请重建 Qdrant 集合或修改 EMBEDDING_DIM")
				os.Exit(1)
			}
			obslog.Error("qdrant.unreachable", "Qdrant 不可达，vector/hybrid 将返回 503，keyword 不受影响",
				"detail", err.Error())
		}
		// tasks 2.6：种子/历史材料补索引（幂等：只处理还没有切片行的材料）。
		// INDEX_ON_STARTUP=0 关掉这一步：补索引会对每条切片调用嵌入接口，
		// 课程网关按天限 token，额度耗尽时启动就白打一串 429。
		// 本地只想登录/看界面时设 0，需要检索时再逐条补（POST /api/materials/{id}/reindex）。
		if os.Getenv("INDEX_ON_STARTUP") == "0" {
			obslog.Info("index.skipped", "INDEX_ON_STARTUP=0，跳过启动补索引")
		} else if err := indexer.IndexPendingMaterials(context.Background()); err != nil {
			obslog.Error("index.pending_scan_failed", "补索引查询失败（不影响服务）", "detail", err.Error())
		}

		// add-refresh-token：清理已过期的 refresh 行（启动时一次即可，课程场景量小）。
		if n, err := auth.PurgeExpired(context.Background(), pool, time.Now()); err != nil {
			obslog.Error("auth.purge_refresh_failed", "清理过期 refresh 失败（不影响服务）", "detail", err.Error())
		} else if n > 0 {
			obslog.Info("auth.purged_refresh", "", "count", n)
		}
	}()

	// 优雅关闭
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		obslog.Error("server.shutdown_failed", "关闭 HTTP 服务时出错", "detail", err.Error())
	}
	obslog.Info("server.stopped", "")
}

// bootstrap 等数据库就绪，然后建表与写种子。
func bootstrap(pool *sql.DB, cfg *config.Config) error {
	if err := db.WaitReady(pool); err != nil {
		return err
	}
	return migrateAndSeed(pool, cfg)
}

// migrateAndSeed 建表 + 写种子。幂等（CREATE TABLE IF NOT EXISTS / 种子逐项判存在）。
func migrateAndSeed(pool *sql.DB, cfg *config.Config) error {
	if err := db.Migrate(pool); err != nil {
		return fmt.Errorf("建表失败: %w", err)
	}
	if err := db.Seed(pool, cfg); err != nil {
		return fmt.Errorf("种子初始化失败: %w", err)
	}
	return nil
}

// retryBootstrap 在后台持续重试，直到建表与种子完成。
//
// 这条路径服务于「DB 比 api 起得慢」：进程不必等到 DB 就绪才对外服务，
// /health 可以先给出存活信号，业务接口在 DB 就绪前返回 503。
//
// ⚠️ 本循环**不再调 WaitReady**：那是建表前的一次性宽限（10 秒），
// 套在这里会让每一轮都白等 10 秒。改成先 Ping 再直接建表/种子，
// 每轮固定 5 秒 —— DB 一变可用，最多 5 秒内完成初始化。
func retryBootstrap(pool *sql.DB, cfg *config.Config) {
	for attempt := 1; ; attempt++ {
		time.Sleep(5 * time.Second)
		if err := db.Ping(pool); err != nil {
			// 每 12 轮（约 1 分钟）记一条，避免日志刷屏
			if attempt%12 == 1 {
				obslog.Error("db.retry_waiting", "数据库仍不可用", "attempt", attempt, "detail", err.Error())
			}
			continue
		}
		if err := migrateAndSeed(pool, cfg); err != nil {
			obslog.Error("db.retry_failed", "后台重试建表/种子失败", "attempt", attempt, "detail", err.Error())
			continue
		}
		obslog.Info("db.retry_succeeded", "", "attempt", attempt)
		return
	}
}

// requireDB 让 /api/* 在数据库不可用时返回 503（R8.4）。
//
// 判定只覆盖 /api/*：`/health` 必须**不查库**仍然 200，否则 DB 抖动会被
// 编排系统误判成容器故障并重启，反而放大故障（design.md D9）。
func requireDB(m *db.Monitor, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") && !m.Ready() {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"db_unavailable"}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requestID 给每个请求分配标识，放进 context 并回写响应头。
//
// 为什么放在最外层而不是业务处理器里：`requireDB` 拒绝的 503、路由未命中的 404
// 也需要能被同一条 id 串起来——否则"用户说报错了"时，最该看的那批拒绝日志反而没有 id。
//
// 复用客户端带来的 `X-Request-ID`（长度限制 64、只留字母数字与 `-_`）：
// 这样反代/前端链路可以端到端串同一个 id；但必须清洗，否则它会被当成自由文本写进日志。
func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := sanitizeRequestID(r.Header.Get("X-Request-ID"))
		if id == "" {
			id = obslog.NewRequestID()
		}
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(obslog.WithRequestID(r.Context(), id)))
	})
}

func sanitizeRequestID(s string) string {
	if s == "" || len(s) > 64 {
		return ""
	}
	for _, c := range s {
		ok := c == '-' || c == '_' ||
			(c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
		if !ok {
			return ""
		}
	}
	return s
}

// logRequests 记录访问日志（一行一条 JSON）。
//
// 只记**路径**不记 query：检索接口的 query 里就是用户问的问题，
// 落进访问日志等于把提问内容复制一份到日志系统。
// 刻意**不记录请求体**：登录请求体含明文口令，上传请求体含材料正文。
func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		obslog.WithContext(r.Context(), obslog.LevelInfo, "http.access", "",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"dur_ms", time.Since(start).Milliseconds(),
			"bytes", rec.written,
			"ip", clientIP(r),
		)
	})
}

// clientIP 取直连对端地址。
//
// 刻意**不解析 X-Forwarded-For**：该头可由客户端任意伪造，记录它等于允许
// 攻击者往审计日志里写任意地址。真实来源地址由反代层负责，本进程只报自己看到的。
func clientIP(r *http.Request) string {
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	return host
}

type statusRecorder struct {
	http.ResponseWriter
	status  int
	written int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	n, err := r.ResponseWriter.Write(b)
	r.written += n
	return n, err
}

// Flush 透传 http.Flusher。
//
// 必要性：logRequests 用 statusRecorder 包住 ResponseWriter 才能拿到状态码，
// 但嵌入 http.ResponseWriter 接口**只**带来 Header/Write/WriteHeader 三个方法，
// 包装之后 http.Flusher 就丢了。第 5 课 SSE 端点（POST /api/conversations/{id}/messages）
// 正是靠 w.(http.Flusher) 逐段冲刷的——少了这个透传，断言失败会直接 500
// （实测错误体 {"error":"stream_unsupported"}）。
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap 让 http.ResponseController（Go 1.20+）能穿透本包装器找到底层 writer。
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }
