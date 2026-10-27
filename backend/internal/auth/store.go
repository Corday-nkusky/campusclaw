package auth

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// 本文件是 refresh token 的持久化（add-refresh-token design D3/D4）。
//
// 设计要点：
//   - 一切判定以库里为准：是否存在、是否过期、是否已轮换、是否已吊销。
//   - 轮换（RotateRefresh）把「标记旧行 + 插入新行」放在**同一事务**里，
//     避免"旧行已废、新行没写"的中间态导致用户凭空掉线。
//   - 重放检测：收到的 refresh 命中一行且其 replaced_by 非空 → 该值被用过，
//     判定泄露，吊销该用户全部 refresh（RevokeAllForUser）。

// ErrRefreshInvalid 是 refresh 无效的**唯一**错误（不存在 / 过期 / 已吊销 / 已轮换）。
//
// 对外一律映射成 401，不区分具体原因 —— 与 R1.2 的"失败同形"同一思路：
// 区分原因等于告诉攻击者"这个值存在但已过期"，反而不利。
var ErrRefreshInvalid = errors.New("refresh 无效")

// refreshRecord 是查询到的一行 refresh（仅取判定所需字段）。
type refreshRecord struct {
	id        int64
	userID    int64
	expiresAt time.Time
	replaced  sql.NullString // 非空 = 已被轮换过
	revokedAt sql.NullTime   // 非空 = 已吊销
}

// InsertRefresh 写入一条新的 refresh 记录。
//
// token 为原文，函数内部只存其摘要；调用方不应把原文再交给存储层。
func InsertRefresh(ctx context.Context, pool *sql.DB, userID int64, token string, ttl time.Duration, now time.Time) error {
	const q = `INSERT INTO refresh_tokens (user_id, token_hash, expires_at) VALUES (?, ?, ?)`
	_, err := pool.ExecContext(ctx, q, userID, HashRefresh(token), now.Add(ttl))
	return err
}

// findRefresh 按摘要取一行。不存在返回 ErrRefreshInvalid。
func findRefresh(ctx context.Context, q queryRower, hash string) (*refreshRecord, error) {
	const sqlText = `
		SELECT id, user_id, expires_at, replaced_by, revoked_at
		FROM refresh_tokens
		WHERE token_hash = ?`
	var r refreshRecord
	err := q.QueryRowContext(ctx, sqlText, hash).Scan(&r.id, &r.userID, &r.expiresAt, &r.replaced, &r.revokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrRefreshInvalid
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// queryRower 抽象 *sql.DB 与 *sql.Tx 的公共查询能力。
type queryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// ConsumeRefresh 校验并**消费**一个 refresh：成功则轮换出新值并返回新值。
//
// 流程（design D4）：
//  1. 按摘要查行；查不到 → ErrRefreshInvalid。
//  2. 过期 / 已吊销 / 已被轮换 → 视为无效。
//     其中「已被轮换」（replaced_by 非空）是**重放**信号 → 额外吊销该用户全部 refresh。
//  3. 通过则在同一事务里：旧行写 replaced_by=新摘要；插入新行。
//
// 返回的新 refresh 原文只在此处生成并返回，库里仍只有摘要。
func ConsumeRefresh(ctx context.Context, pool *sql.DB, token string, ttl time.Duration, now time.Time) (userID int64, newToken string, err error) {
	rec, err := findRefresh(ctx, pool, HashRefresh(token))
	if err != nil {
		return 0, "", err
	}

	// 重放检测：这条已经被换过 → 疑似泄露，吊销该用户全部 refresh。
	if rec.replaced.Valid {
		_ = RevokeAllForUser(ctx, pool, rec.userID)
		return 0, "", ErrRefreshInvalid
	}
	if rec.revokedAt.Valid || !now.Before(rec.expiresAt) {
		return 0, "", ErrRefreshInvalid
	}

	newToken, err = NewRefresh()
	if err != nil {
		return 0, "", err
	}

	tx, err := pool.BeginTx(ctx, nil)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = tx.Rollback() }()

	// 旧行标记为已轮换（条件带 replaced_by IS NULL：并发下只有一方能成功）。
	res, err := tx.ExecContext(ctx,
		`UPDATE refresh_tokens SET replaced_by = ? WHERE id = ? AND replaced_by IS NULL`,
		HashRefresh(newToken), rec.id)
	if err != nil {
		return 0, "", err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// 竞态：期间已被别的请求换走 → 按重放处理
		_ = tx.Rollback()
		_ = RevokeAllForUser(ctx, pool, rec.userID)
		return 0, "", ErrRefreshInvalid
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO refresh_tokens (user_id, token_hash, expires_at) VALUES (?, ?, ?)`,
		rec.userID, HashRefresh(newToken), now.Add(ttl)); err != nil {
		return 0, "", err
	}

	if err := tx.Commit(); err != nil {
		return 0, "", err
	}
	return rec.userID, newToken, nil
}

// RefreshUserID 只校验 refresh 是否有效并返回其 user_id，**不消费**。
//
// 用途：登出时按 refresh 定位要删除的行（登出要删行，但不需要轮换）。
func RefreshUserID(ctx context.Context, pool *sql.DB, token string, now time.Time) (int64, error) {
	rec, err := findRefresh(ctx, pool, HashRefresh(token))
	if err != nil {
		return 0, err
	}
	if rec.revokedAt.Valid || rec.replaced.Valid || !now.Before(rec.expiresAt) {
		return 0, ErrRefreshInvalid
	}
	return rec.userID, nil
}

// DeleteRefresh 删除单个 refresh 行（登出用）。删不到不报错——登出幂等。
func DeleteRefresh(ctx context.Context, pool *sql.DB, token string) error {
	_, err := pool.ExecContext(ctx, `DELETE FROM refresh_tokens WHERE token_hash = ?`, HashRefresh(token))
	return err
}

// RevokeAllForUser 吊销某用户的全部 refresh（改密 / 禁用 / 重放止损）。
//
// 删除而非置 revoked_at：这些令牌已无保留价值，删除更干净，
// 且随后若有人拿旧值重试，会落到"查不到"分支，同样是 401。
func RevokeAllForUser(ctx context.Context, pool *sql.DB, userID int64) error {
	_, err := pool.ExecContext(ctx, `DELETE FROM refresh_tokens WHERE user_id = ?`, userID)
	return err
}

// PurgeExpired 清理过期行。返回删除条数。
//
// 可在启动时或按需调用；本迭代在启动后台任务里顺带执行一次即可
// （课程场景量小，不引入定时器）。
func PurgeExpired(ctx context.Context, pool *sql.DB, now time.Time) (int64, error) {
	res, err := pool.ExecContext(ctx, `DELETE FROM refresh_tokens WHERE expires_at <= ?`, now)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
