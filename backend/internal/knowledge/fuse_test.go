package knowledge

import "testing"

// fuseRRF 是 hybrid 检索的融合核心：两路（关键字 / 向量）各按名次贡献
// 1/(60+rank+1)，同名 chunk 合分，最后按 score 降序、chunk_id 升序取 topK。
//
// 这组测试存在的原因：2026-10-27 发现 HybridPath 误调 HybridPathIn，
// 把「不限材料」当成「没绑材料」直接返回空，而 /api/search、/api/ask 的
// hybrid 模式全走这条路 —— 整条混合检索静默失效，没有任何测试能红。

func TestFuseRRFEmptyInputs(t *testing.T) {
	if got := fuseRRF(nil, nil, 5); len(got) != 0 {
		t.Fatalf("两路都空应返回空，得到 %d 条", len(got))
	}
}

func TestFuseRRFSinglePathKeepsOrder(t *testing.T) {
	kw := []Hit{{ChunkID: 1}, {ChunkID: 2}}
	got := fuseRRF(kw, nil, 5)
	if len(got) != 2 || got[0].ChunkID != 1 || got[1].ChunkID != 2 {
		t.Fatalf("单路结果应按名次保持顺序，得到 %+v", got)
	}
	// rank 0 → 1/61，rank 1 → 1/62
	if got[0].Score <= got[1].Score {
		t.Fatalf("名次靠前的分数应更高：%v vs %v", got[0].Score, got[1].Score)
	}
	if got[0].Score != got[0].ScoreFinal {
		t.Fatalf("score 是 score_final 的别名，应相等")
	}
}

func TestFuseRRFMergesSameChunkAcrossPaths(t *testing.T) {
	v := 0.91
	kw := []Hit{{ChunkID: 7, ScoreKeyword: 3.2}}
	vec := []Hit{{ChunkID: 7, ScoreVector: &v}}
	got := fuseRRF(kw, vec, 5)
	if len(got) != 1 {
		t.Fatalf("同一 chunk 出现在两路时只应有一条，得到 %d 条", len(got))
	}
	h := got[0]
	// 两路都命中 → 两份贡献相加，必然高于任一路单独命中
	if h.Score <= round4(1.0/61) {
		t.Fatalf("两路命中应累加两份 RRF 贡献，得到 %v", h.Score)
	}
	if h.ScoreVector == nil || *h.ScoreVector != v {
		t.Fatalf("向量路原始分应被并入同一条，得到 %+v", h.ScoreVector)
	}
	if h.ScoreKeyword != 3.2 {
		t.Fatalf("关键字路原始分应保留，得到 %v", h.ScoreKeyword)
	}
}

func TestFuseRRFInterleavesBothPathsByRank(t *testing.T) {
	// kw: 1,2   vec: 2,1 → 1 和 2 各得 rank0 + rank1 的贡献，分数相同
	kw := []Hit{{ChunkID: 1}, {ChunkID: 2}}
	vec := []Hit{{ChunkID: 2}, {ChunkID: 1}}
	got := fuseRRF(kw, vec, 5)
	if len(got) != 2 {
		t.Fatalf("应融合出 2 条，得到 %d 条", len(got))
	}
	if got[0].Score != got[1].Score {
		t.Fatalf("两路互为逆序时分数应相同：%v vs %v", got[0].Score, got[1].Score)
	}
	// 并列时 chunk_id 升序
	if got[0].ChunkID != 1 || got[1].ChunkID != 2 {
		t.Fatalf("并列应按 chunk_id 升序，得到 %d,%d", got[0].ChunkID, got[1].ChunkID)
	}
}

func TestFuseRRFOnlyVectorPathHit(t *testing.T) {
	v := 0.8
	vec := []Hit{{ChunkID: 9, ScoreVector: &v}}
	got := fuseRRF(nil, vec, 5)
	if len(got) != 1 || got[0].ChunkID != 9 {
		t.Fatalf("仅向量路命中时也应出现在结果里，得到 %+v", got)
	}
	if got[0].ScoreKeyword != 0 {
		t.Fatalf("关键字路未命中时 score_keyword 应为 0，得到 %v", got[0].ScoreKeyword)
	}
}

func TestFuseRRFTruncatesToTopK(t *testing.T) {
	kw := []Hit{{ChunkID: 1}, {ChunkID: 2}, {ChunkID: 3}}
	got := fuseRRF(kw, nil, 2)
	if len(got) != 2 {
		t.Fatalf("应截断到 topK=2，得到 %d 条", len(got))
	}
	if got[0].ChunkID != 1 || got[1].ChunkID != 2 {
		t.Fatalf("截断应保留名次靠前的两条，得到 %d,%d", got[0].ChunkID, got[1].ChunkID)
	}
}

func TestFuseRRFTopKZeroDropsAll(t *testing.T) {
	kw := []Hit{{ChunkID: 1}}
	if got := fuseRRF(kw, nil, 0); len(got) != 0 {
		t.Fatalf("topK=0 应返回空，得到 %d 条", len(got))
	}
}
