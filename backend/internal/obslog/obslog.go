// Package obslog 是「日志与观测规范」的唯一落地点。
//
// 本仓的日志口径（见 docs/standards/05-日志与观测规范.md）：
//
//   - **一行一条 JSON**，不写多行、不写带换行的字段值（`msg` 里的换行会被转义）；
//   - **必带请求标识** `request_id`：由 HTTP 中间件生成并放进 context，
//     同一次请求里产生的所有日志用同一个 id 串起来；
//   - **字段命名统一**：`ts` / `level` / `event` / `msg` 四个固定键，
//     其余为小写下划线命名，事件专属字段用 `event` 区分而不靠 `msg` 文本；
//   - **敏感字段不落日志**：口令、令牌、上传正文、检索到的资料正文一律不记录。
//     本包不提供"任意结构体整体打印"的能力，就是为了让误记敏感字段需要显式拼字符串。
//
// 之所以自己写而不引入第三方日志库：依赖越少越好（本仓后端只用标准库），
// 而这条规范需要的全部能力就是"加几个固定键 + 一次 json.Marshal"。
package obslog

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"sync"
	"time"
)

// 固定键名。事件专属字段不得与之重名。
const (
	KeyTime      = "ts"
	KeyLevel     = "level"
	KeyEvent     = "event"
	KeyMessage   = "msg"
	KeyRequestID = "request_id"
)

// 级别取值。只有两档：info 用于"发生了什么"，error 用于"哪里失败了"。
// 不设 debug：本仓规模不需要按级别过滤，多一档反而会让人把该留下的信息藏在 debug 里。
const (
	LevelInfo  = "info"
	LevelError = "error"
)

// logger 是唯一实现。输出目标固定为 stdout（容器里由采集器接管），
// 允许替换是为了测试能拿到字节。
type logger struct {
	mu  sync.Mutex
	out io.Writer
}

var std = &logger{out: os.Stdout}

// SetOutput 替换输出目标（仅测试使用）。
func SetOutput(w io.Writer) {
	std.mu.Lock()
	defer std.mu.Unlock()
	std.out = w
}

// Info 记一条信息级日志。kv 是交替的键与值：`Info("http.access", "", "status", 200)`。
//
// kv 长度为奇数时补一个空串，而不是 panic —— 日志不该让服务崩掉。
func Info(event, msg string, kv ...any) { std.log(LevelInfo, event, msg, context.Background(), kv...) }

// WithContext 与 Info 相同，但从 context 里取 request_id。
func WithContext(ctx context.Context, level, event, msg string, kv ...any) {
	std.log(level, event, msg, ctx, kv...)
}

// InfoCtx 带请求标识的信息日志：一次请求内的正常分支（忽略了注入参数、
// 落库成功）都走这条，便于按 request_id 串起"这次请求做了什么"。
func InfoCtx(ctx context.Context, event, msg string, kv ...any) {
	std.log(LevelInfo, event, msg, ctx, kv...)
}

// Errorf 风格的错误日志直接用 Error。
func Error(event, msg string, kv ...any) {
	std.log(LevelError, event, msg, context.Background(), kv...)
}

// ErrorCtx 带请求标识的错误日志：一次请求内失败都走这条，便于按 id 捞全链路。
func ErrorCtx(ctx context.Context, event, msg string, kv ...any) {
	std.log(LevelError, event, msg, ctx, kv...)
}

func (l *logger) log(level, event, msg string, ctx context.Context, kv ...any) {
	// map[string]any 的键在 encoding/json 里按字典序输出，字段顺序是稳定的，
	// 不用为此自己拼字节。
	rec := make(map[string]any, 5+len(kv)/2+1)
	rec[KeyTime] = time.Now().Format("2006-01-02T15:04:05.000Z07:00")
	rec[KeyLevel] = level
	rec[KeyEvent] = event
	if msg != "" {
		rec[KeyMessage] = msg
	}
	if id := RequestIDFrom(ctx); id != "" {
		rec[KeyRequestID] = id
	}
	for i := 0; i+1 < len(kv); i += 2 {
		k, ok := kv[i].(string)
		if !ok || k == "" {
			continue
		}
		// 固定键不允许被调用方覆盖：否则一条日志可能出现两个 level
		switch k {
		case KeyTime, KeyLevel, KeyEvent, KeyMessage, KeyRequestID:
			continue
		}
		rec[k] = kv[i+1]
	}
	if len(kv)%2 == 1 {
		rec["odd_kv"] = true
	}

	b, err := json.Marshal(rec)
	if err != nil {
		// 值不可序列化（如 chan）时退化成一条最小记录，保住"一行一条"不变形
		b = []byte(`{"level":"error","event":"obslog.marshal_failed","msg":"日志字段无法序列化为 JSON"}`)
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = l.out.Write(append(b, '\n'))
}

// ── 请求标识 ────────────────────────────────────────────────

type ctxKey struct{}

// WithRequestID 把请求标识放进 context。
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// RequestIDFrom 取请求标识；没有则返回空串（此时日志里不出现该字段，而不是写 "-"）。
func RequestIDFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(ctxKey{}).(string); ok {
		return v
	}
	return ""
}

// NewRequestID 生成 16 位十六进制标识。
//
// 不用自增序号：多副本部署时序号会撞，而且跨进程无法保证唯一。
// 用随机值而不是时间戳：同一毫秒内的并发请求也能区分开。
func NewRequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// 随机源不可用是极端情况；退回纳秒时间戳也仍是"足够区分"的
		return hex.EncodeToString([]byte(time.Now().Format("150405.000000000")))
	}
	return hex.EncodeToString(b[:])
}
