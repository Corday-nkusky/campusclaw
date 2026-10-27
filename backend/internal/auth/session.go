package auth

import (
	"context"
	"errors"
)

// Session 是「服务端认定的当前身份」。
//
// 与迭代 1 的区别（add-token-auth design.md D1）：身份来自 JWT 验签得到的
// user_id 查库，Role / ClassID / ClassName **每次请求都从 users / classes 表读**，
// 不来自 token 声明、更不来自客户端 —— 改角色或调班，下一次请求即生效。
type Session struct {
	UserID    int64
	Username  string
	Role      string
	ClassID   int64
	ClassName string
}

// ErrNoSession 表示 token 无效、过期，或其指向的用户已不存在。
var ErrNoSession = errors.New("token 无效或已失效")

// ── 请求上下文 ────────────────────────────────────────────────

type ctxKey int

const sessionCtxKey ctxKey = iota

// FromContext 取出中间件放入的会话。
func FromContext(ctx context.Context) (*Session, bool) {
	sess, ok := ctx.Value(sessionCtxKey).(*Session)
	return sess, ok
}

func withSession(ctx context.Context, sess *Session) context.Context {
	return context.WithValue(ctx, sessionCtxKey, sess)
}
