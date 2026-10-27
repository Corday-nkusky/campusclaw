package auth

import (
	"sync"
	"time"
)

// Limiter 是「用户名 + IP」维度的登录失败限流器。
//
// 放在进程内存里是有意为之：本迭代声明**单实例部署**（design.md 未决与风险）。
// 多副本下每个副本各有一份计数，限流就不再准确——多副本需要共享存储（如 Redis），
// 属后续迭代，不在本迭代范围内。
type Limiter struct {
	mu        sync.Mutex
	attempts  map[string]*attemptState
	threshold int
	lockFor   time.Duration
	now       func() time.Time // 便于测试注入时钟
}

type attemptState struct {
	failures  int
	lockUntil time.Time
}

// NewLimiter 构造限流器。threshold 次失败后锁定 lockFor。
func NewLimiter(threshold int, lockFor time.Duration) *Limiter {
	return &Limiter{
		attempts:  make(map[string]*attemptState),
		threshold: threshold,
		lockFor:   lockFor,
		now:       time.Now,
	}
}

// Locked 报告该键当前是否处于锁定状态。
//
// 被限流的请求**不能**用 429 响应：429 会暴露「该账号正处于锁定状态」，
// 本身就是账号枚举的依据（spec.md R1.2）。因此调用方只把结果当作
// 「本次失败」处理，对外响应与凭据错误完全同形。
func (l *Limiter) Locked(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	st, ok := l.attempts[key]
	if !ok {
		return false
	}
	if !st.lockUntil.IsZero() && l.now().Before(st.lockUntil) {
		return true
	}
	if !st.lockUntil.IsZero() {
		// 锁定期已过，清零重新计数
		delete(l.attempts, key)
	}
	return false
}

// Fail 记录一次失败；达到阈值则进入锁定期。
func (l *Limiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	st, ok := l.attempts[key]
	if !ok {
		st = &attemptState{}
		l.attempts[key] = st
	}
	st.failures++
	if st.failures >= l.threshold {
		st.lockUntil = l.now().Add(l.lockFor)
		st.failures = 0
	}
}

// Succeed 清空该键的失败计数。
func (l *Limiter) Succeed(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.attempts, key)
}
