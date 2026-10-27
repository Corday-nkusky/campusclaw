// Package db 负责连接、建表与种子数据。
package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/Corday-nkusky/campusclaw/backend/internal/config"
	"github.com/Corday-nkusky/campusclaw/backend/internal/obslog"
)

// OpenPool 创建连接池。**不建立实际连接**，因此不会因为 DB 未就绪而失败。
//
// 把「建池」与「等待就绪」拆开是刻意的：R8.4 要求 DB 不可用时进程仍存活，
// 若建池与连通性检查捆绑在一起，启动期 DB 抖动就会导致进程退出或反复重启。
func OpenPool(cfg *config.Config) (*sql.DB, error) {
	pool, err := sql.Open("mysql", cfg.DSN())
	if err != nil {
		return nil, fmt.Errorf("打开数据库连接池失败: %w", err)
	}

	pool.SetMaxOpenConns(20)
	pool.SetMaxIdleConns(10)
	pool.SetConnMaxLifetime(30 * time.Minute)
	return pool, nil
}

// WaitReady 在有界重试内等待数据库就绪，作为建表/种子前的**一次宽限**。
//
// ⚠️ 它是**短**宽限，不是长等待：固定 1 秒 × 10 次。
// 长时间等待的职责在 cmd/server 的 retryBootstrap（每 5 秒一轮，先 Ping 再建表），
// 那一条路径是不阻塞监听的；把长退避放在这里只会让「DB 迟到」场景报错更晚、
// 恢复更慢 —— 早期版本用 1+2+3+4+5×26 秒的退避（合计约 140 秒），
// 既延迟了「数据库暂不可用」这条关键日志，也让 DB 恢复后要再等一轮 140 秒。
//
// ⚠️ 它**不终止进程**。调用方应在拿到错误后记录并继续提供 `/health`，
// 同时把「建表 + 种子」放到后台重试——这正是 R8.4 的语义。
func WaitReady(pool *sql.DB) error {
	const attempts = 10
	var lastErr error

	for i := 1; i <= attempts; i++ {
		if lastErr = Ping(pool); lastErr == nil {
			if i > 1 {
				obslog.Info("db.wait_ready", "数据库已就绪", "attempt", i)
			}
			return nil
		}
		if i == 1 {
			obslog.Info("db.wait_ready_loop", "等待数据库就绪", "attempts", attempts)
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("数据库在 %d 次尝试后仍不可连: %w", attempts, lastErr)
}

// Ping 带超时的连通性探测。
//
// 必须有超时：指向黑洞地址时裸 Ping 会长时间挂住，那会让「探测」本身变成故障源。
func Ping(pool *sql.DB) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return pool.PingContext(ctx)
}
