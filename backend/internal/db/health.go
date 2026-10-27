package db

import (
	"database/sql"
	"sync/atomic"
	"time"

	"github.com/Corday-nkusky/campusclaw/backend/internal/obslog"
)

// Monitor 周期探测数据库可用性，供业务接口的 503 判定使用（R8.4）。
//
// 为什么不每请求都 Ping：那会让 DB 往返次数翻倍，而这里要判的只是
// 「DB 整体是否可用」，不是某条 SQL 的结果。
//
// 探测间隔取 2 秒，代价是 DB 刚恢复时最多有 2 秒窗口仍返回 503。
// 方向上是**宁可短暂多 503，也不要把已恢复判成不可用**。
// 另外每个 handler 自己在查询出错时也会返回 503，两层兜底。
type Monitor struct {
	pool  *sql.DB
	ready atomic.Bool
}

// NewMonitor 构造探测器。初始状态为「未就绪」。
func NewMonitor(pool *sql.DB) *Monitor {
	return &Monitor{pool: pool}
}

// Start 立即探测一次，随后按 interval 周期探测。非阻塞。
func (m *Monitor) Start(interval time.Duration) {
	m.probe("启动探测")
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for range t.C {
			m.probe("周期探测")
		}
	}()
}

// Ready 报告上一次探测的结果。
func (m *Monitor) Ready() bool {
	return m.ready.Load()
}

// probe 执行一次探测，只在状态**发生变化**时打日志，避免刷屏。
func (m *Monitor) probe(when string) {
	err := Ping(m.pool)
	wasReady := m.ready.Swap(err == nil)

	switch {
	case err == nil && !wasReady:
		obslog.Info("db.ready", "数据库可用，业务接口恢复", "when", when)
	case err != nil && wasReady:
		obslog.Error("db.unavailable", "数据库不可用，业务接口转 503", "when", when, "detail", err.Error())
	}
}
