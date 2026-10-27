package knowledge

import (
	"strings"
	"testing"
)

// 覆盖 tasks 3.2：auto 边界断开、custom 参数与越界、hierarchy 标题保留、
// 空文本 / 单行超长 / 无标题 三情形（→ R9.1–R9.4）。

func TestAutoDefaultParams(t *testing.T) {
	// 构造 2000 字无断点文本（全部 'a'）→ 只能硬切，验证窗口与重叠数值
	text := strings.Repeat("a", 2000)
	chunks, err := ChunkText(text, Options{Strategy: "auto"})
	if err != nil {
		t.Fatalf("auto 切分失败: %v", err)
	}
	if len(chunks) < 3 {
		t.Fatalf("2000 字应切出 ≥3 片，实际 %d", len(chunks))
	}
	first := chunks[0]
	if first.Start != 0 || first.End != autoWindowSize {
		t.Fatalf("首片应为 [0,%d)，实际 [%d,%d)", autoWindowSize, first.Start, first.End)
	}
	if len([]rune(first.Text)) > autoWindowSize {
		t.Fatalf("首片超过 800 字")
	}
	// 相邻片必须重叠（R4.3）：chunk[i+1].start < chunk[i].end
	for i := 1; i < len(chunks); i++ {
		if chunks[i].Start >= chunks[i-1].End {
			t.Fatalf("片 %d 与 %d 无重叠: [%d,%d) → [%d,%d)",
				i-1, i, chunks[i-1].Start, chunks[i-1].End, chunks[i].Start, chunks[i].End)
		}
	}
	// 末片 end 必须到达文本末尾
	last := chunks[len(chunks)-1]
	if last.End != 2000 {
		t.Fatalf("末片 end=%d，应为 2000", last.End)
	}
}

func TestAutoPrefersBreaks(t *testing.T) {
	// 每隔 50 字放一个句号 → 断点应落在句号处，而不是把句子拦腰切断
	var b strings.Builder
	for i := 0; i < 40; i++ {
		b.WriteString(strings.Repeat("字", 50))
		b.WriteString("。")
	}
	chunks, err := ChunkText(b.String(), Options{Strategy: "auto"})
	if err != nil {
		t.Fatalf("auto 切分失败: %v", err)
	}
	for i, c := range chunks[:len(chunks)-1] { // 末片天然到结尾，不查
		text := []rune(c.Text)
		if !isBreakRune(text[len(text)-1]) {
			t.Fatalf("片 %d 未在断点结束，末字符=%q", i, string(text[len(text)-1]))
		}
	}
}

func isBreakRune(r rune) bool {
	return r == '\n' || r == '。' || r == '！' || r == '？' || r == '.' || r == '!' || r == '?'
}

func TestCustomParamsAndRange(t *testing.T) {
	text := strings.Repeat("词", 1000)
	chunks, err := ChunkText(text, Options{Strategy: "custom", Size: 300, OverlapPercent: 10})
	if err != nil {
		t.Fatalf("custom 切分失败: %v", err)
	}
	if len(chunks) < 3 {
		t.Fatalf("1000 字 / 300 窗口应 ≥3 片，实际 %d", len(chunks))
	}
	for i, c := range chunks {
		if n := len([]rune(c.Text)); n > 300 {
			t.Fatalf("片 %d 长 %d 超过 300", i, n)
		}
	}
	// 越界必须报错（R9.3）
	if _, err := ChunkText(text, Options{Strategy: "custom", Size: 50}); err == nil {
		t.Fatal("长度 50 < 100 应报错")
	}
	if _, err := ChunkText(text, Options{Strategy: "custom", Size: 5000}); err == nil {
		t.Fatal("长度 5000 > 2000 应报错")
	}
	if _, err := ChunkText(text, Options{Strategy: "custom", Size: 300, OverlapPercent: 80}); err == nil {
		t.Fatal("重叠 80% 应报错")
	}
}

func TestHierarchyKeepsHeadings(t *testing.T) {
	text := "# 第一章\n" + strings.Repeat("甲", 100) + "\n## 第一节\n" + strings.Repeat("乙", 100) +
		"\n### 小节\n" + strings.Repeat("丙", 100)
	chunks, err := ChunkText(text, Options{Strategy: "hierarchy"})
	if err != nil {
		t.Fatalf("hierarchy 切分失败: %v", err)
	}
	if len(chunks) < 3 {
		t.Fatalf("三个标题应至少分三章，实际 %d 片", len(chunks))
	}
	if !strings.Contains(chunks[0].Text, "# 第一章") {
		t.Fatal("首章未保留标题")
	}
	// 每章正文不串章：第一章不含「乙」
	if strings.Contains(chunks[0].Text, "乙") {
		t.Fatal("第一章混入第二章正文")
	}
	// 无标题文本：整篇一片（不报错）
	one, err := ChunkText("没有任何标题的长文本。"+strings.Repeat("丁", 50), Options{Strategy: "hierarchy"})
	if err != nil || len(one) != 1 {
		t.Fatalf("无标题文本应整篇一片，得到 %d 片 err=%v", len(one), err)
	}
}

func TestEmptyTextRejected(t *testing.T) {
	if _, err := ChunkText("", Options{}); err == nil {
		t.Fatal("空文本应报错")
	}
	if _, err := ChunkText("   \n\t ", Options{}); err == nil {
		t.Fatal("纯空白文本应报错")
	}
}

func TestDefaultStrategyIsAuto(t *testing.T) {
	text := strings.Repeat("x", 2500)
	chunks, err := ChunkText(text, Options{}) // 未指定策略
	if err != nil {
		t.Fatalf("缺省策略切分失败: %v", err)
	}
	// auto 800/80：第二片起点 = 800-80 = 720
	if chunks[1].Start != autoWindowSize-autoOverlapSize {
		t.Fatalf("缺省应为 auto(800/80)，片2起点=%d，期望 720", chunks[1].Start)
	}
}

func TestPreprocess(t *testing.T) {
	in := "访问 https://example.com/a?b=1 或 mail me at foo@bar.com。\n\n\n\n多空白\t\t文本"
	out := Preprocess(in, true, true)
	if strings.Contains(out, "https://") || strings.Contains(out, "foo@bar.com") {
		t.Fatalf("URL/邮箱未移除: %q", out)
	}
	if strings.Contains(out, "\n\n\n") {
		t.Fatal("连续空行未折叠")
	}
	if strings.Contains(out, "\t\t") {
		t.Fatal("连续制表符未折叠")
	}
	// 原文不受影响（R9.5 的前提：调用方传原文给 entries，传预处理文本给切片）
	if !strings.Contains(in, "https://") {
		t.Fatal("Preprocess 修改了入参")
	}
}

func TestOffsetsRelativeToGivenText(t *testing.T) {
	// R9.6：偏移相对传入的文本。传入预处理后文本时，偏移以它为准。
	raw := "前缀。看 https://x.cn 这里。后缀。"
	pre := Preprocess(raw, true, false)
	chunks, err := ChunkText(pre, Options{Strategy: "custom", Size: 200})
	if err != nil {
		t.Fatalf("切分失败: %v", err)
	}
	runes := []rune(pre)
	for _, c := range chunks {
		if c.Text != string(runes[c.Start:c.End]) {
			t.Fatalf("偏移与文本不一致: [%d,%d)", c.Start, c.End)
		}
	}
}
