package conversation

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Corday-nkusky/campusclaw/backend/internal/knowledge"
)

// flushRecorder 既当 http.ResponseWriter 又实现 Flusher。
type flushRecorder struct {
	*httptest.ResponseRecorder
	flushes int
}

func (f *flushRecorder) Flush() { f.flushes++ }

// newFakeHandlers 返回注入假 save 的 Handlers（不碰真实 DB）。
func newFakeHandlers(save func(ctx context.Context, convID int64, content string, c []knowledge.Hit, s string) (int64, error)) *Handlers {
	return &Handlers{save: save}
}

func okSave(_ context.Context, _ int64, _ string, _ []knowledge.Hit, _ string) (int64, error) {
	return 42, nil
}

// TestStreamAnswer_NoEvidenceWireFormat：检索落空也要走完整 SSE，且
//   - meta.status **恒为 ok**（课件：会话链路不借 status 区分有无依据）；
//   - citations 为空数组；
//   - 正文里的 [n] 编号在下发前被剥掉。
func TestStreamAnswer_NoEvidenceWireFormat(t *testing.T) {
	rec := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
	h := newFakeHandlers(okSave)

	_, _ = h.streamAnswer(context.Background(), rec, rec, 1, []knowledge.Hit{}, "解题引导",
		func(onDelta func(string) error, emit func(string, any)) (string, error) {
			// 模拟模型输出了编号——不应原样下发
			if err := onDelta("先看[1]这一步"); err != nil {
				return "", err
			}
			return "先看[1]这一步", nil
		})

	body := rec.Body.String()
	assertOrder(t, body, "event: meta", "event: delta", "event: done")
	if !strings.Contains(body, `"status":"ok"`) {
		t.Errorf("会话链路 meta.status 应恒为 ok：%s", body)
	}
	if strings.Contains(body, `"status":"no_evidence"`) {
		t.Errorf("会话链路不应出现 no_evidence（那是 /api/ask 的口径）：%s", body)
	}
	if !strings.Contains(body, `"citations":[]`) {
		t.Errorf("落空时 citations 应为空数组：%s", body)
	}
	if strings.Contains(body, "[1]") {
		t.Errorf("落空时正文里的 [n] 编号应被剥掉：%s", body)
	}
	if !strings.Contains(body, `"message_id":42`) {
		t.Errorf("done 未带 message_id：%s", body)
	}
	if rec.flushes < 3 {
		t.Errorf("应至少 flush 3 次，实际 %d", rec.flushes)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("流已开始时 HTTP 应为 200，实际 %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q", ct)
	}
	if rec.Header().Get("X-Accel-Buffering") != "no" {
		t.Error("缺 X-Accel-Buffering: no（Nginx 会缓冲掉流）")
	}
	if rec.Header().Get("Cache-Control") != "no-cache" {
		t.Error("缺 Cache-Control: no-cache")
	}
}

func TestStreamAnswer_MultiDeltaOrder(t *testing.T) {
	rec := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
	h := newFakeHandlers(okSave)
	hits := []knowledge.Hit{{MaterialTitle: "一元一次方程", ChunkIndex: 0, Snippet: "移项"}}

	_, _ = h.streamAnswer(context.Background(), rec, rec, 1, hits, "方程解题引导",
		func(onDelta func(string) error, emit func(string, any)) (string, error) {
			for _, d := range []string{"先看", "等号两边", "。"} {
				if err := onDelta(d); err != nil {
					return "", err
				}
			}
			return "先看等号两边。", nil
		})

	body := rec.Body.String()
	assertOrder(t, body, "event: meta", `"text":"先看"`, `"text":"等号两边"`, "event: done")
	if !strings.Contains(body, `"status":"ok"`) {
		t.Errorf("有命中时 meta 应标 ok：%s", body)
	}
	if !strings.Contains(body, `"material_title":"一元一次方程"`) {
		t.Errorf("meta.citations 未含命中材料：%s", body)
	}
	// meta 应带本次命中的技能名
	if !strings.Contains(body, `"skill":"方程解题引导"`) {
		t.Errorf("meta 未带命中的技能名：%s", body)
	}
}

func TestStreamAnswer_GenerateErrorKeepsHTTP200(t *testing.T) {
	rec := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
	h := newFakeHandlers(okSave)
	hits := []knowledge.Hit{{MaterialTitle: "M", ChunkIndex: 0, Snippet: "s"}}

	// 零产物才报错
	_, _ = h.streamAnswer(context.Background(), rec, rec, 1, hits, "解题引导",
		func(func(string) error, func(string, any)) (string, error) {
			return "", context.DeadlineExceeded
		})

	body := rec.Body.String()
	assertOrder(t, body, "event: meta", "event: error")
	if rec.Code != http.StatusOK {
		t.Errorf("流中途失败 HTTP 仍应 200，实际 %d", rec.Code)
	}
	if strings.Contains(body, "event: done") {
		t.Error("零产物失败不应出现 done 事件")
	}
}

// 生成中途失败但已产出片段 → 以已产出内容收尾并落库（课件 answer 页）。
func TestStreamAnswer_PartialSuccessIsPersisted(t *testing.T) {
	rec := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
	var saved string
	h := newFakeHandlers(func(_ context.Context, _ int64, content string, _ []knowledge.Hit, _ string) (int64, error) {
		saved = content
		return 7, nil
	})
	hits := []knowledge.Hit{{MaterialTitle: "M", ChunkIndex: 0, Snippet: "s"}}

	_, _ = h.streamAnswer(context.Background(), rec, rec, 1, hits, "",
		func(onDelta func(string) error, emit func(string, any)) (string, error) {
			_ = onDelta("半句")
			return "半句", context.DeadlineExceeded
		})

	body := rec.Body.String()
	if !strings.Contains(body, "event: done") {
		t.Errorf("已产出片段时应以 done 收尾：%s", body)
	}
	if saved != "半句" {
		t.Errorf("已产出内容应落库，实际 %q", saved)
	}
}

func TestStreamAnswer_SaveErrorWritesErrorEvent(t *testing.T) {
	rec := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
	h := newFakeHandlers(func(context.Context, int64, string, []knowledge.Hit, string) (int64, error) {
		return 0, context.DeadlineExceeded
	})
	hits := []knowledge.Hit{{MaterialTitle: "M", ChunkIndex: 0, Snippet: "s"}}

	_, _ = h.streamAnswer(context.Background(), rec, rec, 1, hits, "",
		func(onDelta func(string) error, emit func(string, any)) (string, error) {
			_ = onDelta("答")
			return "答", nil
		})

	body := rec.Body.String()
	assertOrder(t, body, "event: meta", "event: delta", "event: error")
	if rec.Code != http.StatusOK {
		t.Errorf("落库失败时 HTTP 仍应 200，实际 %d", rec.Code)
	}
}

// assertOrder 断言若干子串按给定顺序出现。
func assertOrder(t *testing.T, body string, subs ...string) {
	t.Helper()
	pos := -1
	for _, s := range subs {
		i := strings.Index(body, s)
		if i < 0 {
			t.Fatalf("未找到 %q，body=\n%s", s, body)
		}
		if i < pos {
			t.Fatalf("%q 出现顺序不对，body=\n%s", s, body)
		}
		pos = i
	}
}

// ── 技能路由（课件 route 页）───────────────────────────────────

func TestRouteSkill_ScoringAndTie(t *testing.T) {
	cands := []skillCand{
		{ID: 1, Name: "语文解题引导", Trigger: "春晓,默写,阅读"},
		{ID: 2, Name: "数学解题引导", Trigger: "方程,去括号,分数"},
	}
	// 「方程」命中 2 号，1 分
	if s, ok := routeSkill(cands, "这道方程怎么做"); !ok || s.ID != 2 {
		t.Errorf("应命中 2 号数学技能，得到 %+v ok=%v", s, ok)
	}
	// 零命中 → 不选
	if _, ok := routeSkill(cands, "今天天气不错"); ok {
		t.Error("零命中不应选技能")
	}
	// 同分取小编号：两个技能都被「习题」命中 1 次
	tie := []skillCand{
		{ID: 5, Name: "A", Trigger: "习题"},
		{ID: 9, Name: "B", Trigger: "习题"},
	}
	if s, _ := routeSkill(tie, "做习题"); s.ID != 5 {
		t.Errorf("同分应取编号较小者，得到 %d", s.ID)
	}
}

// 大小写折叠 + 多种分隔符（课件 route 页）。
func TestRouteSkill_CaseFoldAndSeparators(t *testing.T) {
	cands := []skillCand{{ID: 1, Name: "S", Trigger: "Equation、移项；通分|分数"}}
	if _, ok := routeSkill(cands, "how to solve an equation"); !ok {
		t.Error("大小写折叠后 Equation 应命中 equation")
	}
	if _, ok := routeSkill(cands, "先通分"); !ok {
		t.Error("竖线分隔的关键词「通分」应能命中")
	}
}

// ── 检索结果筛除（步骤 7）─────────────────────────────────────

func TestFilterHits_DropEmptyAndSubject(t *testing.T) {
	hits := []knowledge.Hit{
		{MaterialTitle: "数学笔记", Snippet: "正文"},
		{MaterialTitle: "数学笔记2", Snippet: "   "}, // 空正文 → 去
		{MaterialTitle: "语文笔记", Snippet: "正文"},   // 学科不符 → 去
	}
	sk := skillCand{Subject: "数学"}
	out := filterHits(hits, sk, true)
	if len(out) != 1 || out[0].MaterialTitle != "数学笔记" {
		t.Errorf("应按学科与空正文筛除，得到 %+v", out)
	}
	// 无技能命中时不按学科过滤
	out2 := filterHits(hits, skillCand{}, false)
	if len(out2) != 2 {
		t.Errorf("无技能时只去空正文，得到 %d 条", len(out2))
	}
}

// ── 编号剥离器（跨 delta 切开）────────────────────────────────

func TestNumStripper_SplitAcrossDeltas(t *testing.T) {
	var s numStripper
	var got strings.Builder
	for _, d := range []string{"先看", "[", "1", "]", "这一步"} {
		got.WriteString(s.feed(d))
	}
	got.WriteString(s.flush())
	if got.String() != "先看这一步" {
		t.Errorf("跨 delta 的 [1] 未被剥掉：%q", got.String())
	}
}

func TestNumStripper_KeepsNonNumericBrackets(t *testing.T) {
	var s numStripper
	out := s.feed("[注] 见 [a]") + s.flush()
	if !strings.Contains(out, "[注]") || !strings.Contains(out, "[a]") {
		t.Errorf("非数字方括号不应被剥掉：%q", out)
	}
}

// ── 消息组装 ──────────────────────────────────────────────────

func TestBuildMessages_SystemCarriesPromptAndSkill(t *testing.T) {
	hits := []knowledge.Hit{{MaterialTitle: "一元一次方程", ChunkIndex: 0, Snippet: "移项后合并同类项"}}
	system := buildSystem("你是解题助手。", skillCand{Name: "方程解题引导", Description: "只给思路"}, true, true)
	msgs := buildMessages(system, hits, "移项怎么做", nil)

	if len(msgs) != 2 {
		t.Fatalf("期望 system + user 两条，得到 %d", len(msgs))
	}
	if msgs[0].Role != "system" {
		t.Errorf("第一条应为 system，得到 %s", msgs[0].Role)
	}
	if !strings.Contains(msgs[0].Content, "你是解题助手") {
		t.Error("system 未包含智能体提示词")
	}
	if !strings.Contains(msgs[0].Content, "方程解题引导") {
		t.Error("system 未包含技能说明")
	}
	if !strings.Contains(msgs[0].Content, "不得写出最终答案") {
		t.Error("system 未包含「不给最终答案」约束（技能名含「解题引导」时）")
	}
	// 资料不进系统消息
	if strings.Contains(msgs[0].Content, "一元一次方程") {
		t.Error("资料不应出现在系统消息里")
	}
	if msgs[1].Role != "user" {
		t.Errorf("第二条应为 user，得到 %s", msgs[1].Role)
	}
	if !strings.Contains(msgs[1].Content, "[1] 材料《一元一次方程》") {
		t.Error("user 消息未带编号资料")
	}
	if !strings.Contains(msgs[1].Content, "问题：移项怎么做") {
		t.Error("user 消息未带问题")
	}
}

// 落空时系统消息应写明「不要编号」。
func TestBuildSystem_NoEvidenceBoundary(t *testing.T) {
	sys := buildSystem("角色", skillCand{}, false, false)
	if !strings.Contains(sys, "不要写出 [n]") && !strings.Contains(sys, "没有提供资料") {
		t.Errorf("落空时系统消息应写明不要编号：%s", sys)
	}
}

func TestBuildMessages_HistoryTrimsCurrentQuestion(t *testing.T) {
	history := []message{
		{Role: "user", Content: "q1"},
		{Role: "assistant", Content: "a1"},
		{Role: "user", Content: "q2"},
		{Role: "assistant", Content: "a2"},
		{Role: "user", Content: "q3"}, // 本轮，已被写入库
	}
	hits := []knowledge.Hit{{MaterialTitle: "M", ChunkIndex: 0, Snippet: "s"}}
	msgs := buildMessages("sys", hits, "q3", history)
	// system + (q1,a1,q2,a2) + 本轮 user = 6
	if len(msgs) != 6 {
		t.Fatalf("期望 6 条，得到 %d", len(msgs))
	}
	if msgs[1].Content != "q1" || msgs[4].Content != "a2" {
		t.Errorf("历史消息顺序/内容不对: %+v", msgs)
	}
	if strings.Contains(msgs[1].Content, "q3") {
		t.Error("本轮 user 消息不应重复出现在历史里")
	}
}

// 历史每条截断到 500 字。
func TestBuildMessages_HistoryTruncated(t *testing.T) {
	long := strings.Repeat("字", 800)
	history := []message{{Role: "assistant", Content: long}}
	msgs := buildMessages("sys", nil, "q", history)
	if len([]rune(msgs[1].Content)) != histMsgMaxRunes {
		t.Errorf("历史应截断到 %d，得到 %d", histMsgMaxRunes, len([]rune(msgs[1].Content)))
	}
}

// 资料每条截断到 800 字。
func TestBuildMessages_MaterialTruncated(t *testing.T) {
	long := strings.Repeat("甲", 1000)
	msgs := buildMessages("sys", []knowledge.Hit{{MaterialTitle: "M", Snippet: long}}, "q", nil)
	user := msgs[len(msgs)-1].Content
	if strings.Count(user, "甲") != materialMaxRunes {
		t.Errorf("资料应截断到 %d 字，实际含 %d 个「甲」", materialMaxRunes, strings.Count(user, "甲"))
	}
}

func TestTitleFrom(t *testing.T) {
	if got := titleFrom("移项怎么做"); got != "移项怎么做" {
		t.Errorf("短标题 = %q", got)
	}
	long := strings.Repeat("字", 40)
	if got := titleFrom(long); len([]rune(got)) != 24 {
		t.Errorf("长标题应按 24 rune 截断，得到 %d", len([]rune(got)))
	}
}
