package knowledge

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/Corday-nkusky/campusclaw/backend/internal/obslog"
)

// Indexer：上传/重建索引共用的编排器（tasks 5.1 / spec R4）。
//
// 失败语义（design D7 / R4.2）：
//   - 切片行**先以 failed 落库**（保证任何时刻库里的行数与切分结果一致）；
//   - 嵌入或写 Qdrant 失败 → 切片保持 failed，材料与正文**保留**，不回滚；
//   - 只有「嵌入全部成功 + Qdrant upsert（wait=true）成功」后才统一置 ready。
//   - Qdrant upsert 单请求原子，失败时不产生半份向量；即使残留，
//     回表时 `index_status='ready'` 条件也会把它们挡住（R2.5 的兜底）。
type Indexer struct {
	pool *sql.DB
	emb  *Embedder
	qd   *Qdrant
}

// NewIndexer 构造。
func NewIndexer(pool *sql.DB, emb *Embedder, qd *Qdrant) *Indexer {
	return &Indexer{pool: pool, emb: emb, qd: qd}
}

// IndexMaterial 按指定策略对材料全文重切并重建向量（上传与 reindex 同一条链路，R4.3/tasks 5.3）。
//
// 步骤：
//  1. 删旧切片行 + 删 Qdrant 里该材料的旧点（R4.4：先删后写，不残留旧主键）
//  2. 切分（entryBody 保持原样，本函数不改 knowledge_entries）
//  3. 切片行以 failed 落库
//  4. 批量嵌入 → 单请求 upsert（点 ID = 切片主键）
//  5. 全部成功 → 置 ready
func (ix *Indexer) IndexMaterial(ctx context.Context, materialID, classID int64, body string, opts Options) (int, error) {
	opts, err := opts.normalize()
	if err != nil {
		return 0, err
	}

	// ── 1. 删旧 ──────────────────────────────────────────────
	if _, err := ix.pool.ExecContext(ctx, `DELETE FROM knowledge_chunks WHERE material_id = ?`, materialID); err != nil {
		return 0, fmt.Errorf("删除旧切片失败: %w", err)
	}
	if err := ix.qd.DeleteByMaterial(ctx, materialID); err != nil {
		// 旧点删不掉不阻断：upsert 用同一主键覆盖，旧点要么被覆盖要么随后被 delete 收敛。
		// 但若 Qdrant 不可达，后面 upsert 也必然失败，切片自然留在 failed。
		obslog.ErrorCtx(ctx, "index.vectors_delete_failed", "删除旧向量失败，继续重建", "material_id", materialID, "detail", err.Error())
	}

	// ── 2. 切分 ──────────────────────────────────────────────
	chunks, err := ChunkText(body, opts)
	if err != nil {
		return 0, fmt.Errorf("切分失败: %w", err)
	}

	// ── 3. 切片行落库（failed）────────────────────────────────
	ids := make([]int64, len(chunks))
	for i, c := range chunks {
		res, err := ix.pool.ExecContext(ctx,
			`INSERT INTO knowledge_chunks
			   (material_id, class_id, chunk_index, chunk_text, start_offset, end_offset, index_status, strategy)
			 VALUES (?, ?, ?, ?, ?, ?, 'failed', ?)`,
			materialID, classID, c.Index, c.Text, c.Start, c.End, opts.Strategy)
		if err != nil {
			return 0, fmt.Errorf("写入切片行失败: %w", err)
		}
		if ids[i], err = res.LastInsertId(); err != nil {
			return 0, fmt.Errorf("读取切片自增 ID 失败: %w", err)
		}
	}

	// ── 4. 嵌入 + 写 Qdrant ─────────────────────────────────
	texts := make([]string, len(chunks))
	for i, c := range chunks {
		texts[i] = c.Text
	}
	vectors, err := ix.emb.Embed(ctx, texts)
	if err != nil {
		return len(chunks), fmt.Errorf("嵌入失败（切片保持 failed）: %w", err)
	}

	// payload 的 knowledge_entry_id：材料对应的正文行
	var entryID int64
	if err := ix.pool.QueryRowContext(ctx,
		`SELECT id FROM knowledge_entries WHERE material_id = ? ORDER BY id DESC LIMIT 1`,
		materialID).Scan(&entryID); err != nil {
		return len(chunks), fmt.Errorf("读取 knowledge_entries id 失败: %w", err)
	}

	points := make([]Point, len(chunks))
	for i, c := range chunks {
		points[i] = Point{
			ID:     ids[i], // R4.1：点 ID = knowledge_chunks.id
			Vector: vectors[i],
			Payload: map[string]any{
				"class_id":           classID,
				"material_id":        materialID,
				"knowledge_entry_id": entryID,
				"chunk_id":           ids[i],
				"chunk_index":        c.Index,
			},
		}
	}
	if err := ix.qd.Upsert(ctx, points); err != nil {
		return len(chunks), fmt.Errorf("写 Qdrant 失败（切片保持 failed）: %w", err)
	}

	// ── 5. 置 ready ─────────────────────────────────────────
	if _, err := ix.pool.ExecContext(ctx,
		`UPDATE knowledge_chunks SET index_status = 'ready' WHERE material_id = ?`, materialID); err != nil {
		return len(chunks), fmt.Errorf("置 ready 失败: %w", err)
	}
	return len(chunks), nil
}

// IndexPendingMaterials 启动时补齐索引（tasks 2.6）：处理**没有任何 ready 切片**的材料，
// 幂等——已全部 ready 的不重复处理。
//
// 条件刻意不是「从没切过」：切片行先以 failed 落库是 D7 的失败语义，启动时 Qdrant
// 或嵌入服务还没就绪就会留下一批 failed 切片。若只按「无切片行」筛选，这些材料
// 此后永远等不到补齐（检索侧把 failed 全排除，表现为“检索永久无命中且不报错”），
// 手动 reindex 之前没有任何自愈路径。按「无 ready 切片」筛选后，重启即重试。
// 部分 ready 的异常中间态也会被重切——IndexMaterial 先删后写，这是安全的。
func (ix *Indexer) IndexPendingMaterials(ctx context.Context) error {
	rows, err := ix.pool.QueryContext(ctx, `
		SELECT m.id, m.class_id, ke.body
		FROM materials m
		JOIN knowledge_entries ke ON ke.material_id = m.id
		WHERE NOT EXISTS (
			SELECT 1 FROM knowledge_chunks kc
			WHERE kc.material_id = m.id AND kc.index_status = 'ready'
		)`)
	if err != nil {
		return fmt.Errorf("查询待索引材料失败: %w", err)
	}
	defer rows.Close()

	type item struct {
		id, classID int64
		body        string
	}
	pending := []item{}
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.id, &it.classID, &it.body); err != nil {
			return err
		}
		pending = append(pending, it)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, it := range pending {
		n, err := ix.IndexMaterial(ctx, it.id, it.classID, it.body, Options{Strategy: "auto"})
		if err != nil {
			// 失败不阻断启动：切片已标 failed，检索侧自然排除（R2.4）
			obslog.ErrorCtx(ctx, "index.pending_failed", "补索引失败，切片保持 failed", "material_id", it.id, "detail", err.Error())
			continue
		}
		obslog.InfoCtx(ctx, "index.pending_done", "补索引完成", "material_id", it.id, "chunks", n)
	}
	return nil
}

// EnsureCollection 校验/创建 Qdrant 集合（tasks 1.3 的调用入口）。
// 维度不一致返回 ErrDimMismatch，由调用方决定是否终止启动。
func (ix *Indexer) EnsureCollection(ctx context.Context) error {
	return ix.qd.EnsureCollection(ctx)
}
