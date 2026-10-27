package knowledge

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// 切分模块（add-class-scoped-retrieval tasks 3.x / spec R9）。
//
// 边界纪律（tasks 3.4）：本文件只做「文本 → 切片」，
// 不调用嵌入服务、不写 Qdrant、不碰数据库。
//
// 偏移语义（design D4）：
//   - 偏移一律按 **rune（字符）** 计，不是字节；
//   - 未启用预处理时，偏移相对上传原文（R3.2 的「按偏移可回原文」成立）；
//   - 启用预处理时，偏移相对**预处理后的文本**（R9.6），不再承诺对得回原文。

// Chunk 是一个切片。Start/End 是相对**本次切分源文本**的 rune 区间 [Start, End)。
type Chunk struct {
	Index int
	Text  string
	Start int
	End   int
}

// auto 常量（课件 chunk.html §1：≤800 字、重叠 80 字）。
const (
	autoWindowSize  = 800
	autoOverlapSize = 80
)

// custom 参数合法域（R9.3：长度 [100,2000]、重叠 [0,50]%）。
const (
	customMinSize    = 100
	customMaxSize    = 2000
	customMaxOverlap = 50 // 百分比
)

// Options 是切分参数。Strategy ∈ {"auto","custom","hierarchy"}。
type Options struct {
	Strategy string
	// custom 专用
	Size           int  // 目标片长（rune）
	OverlapPercent int  // 重叠百分比
	StripLinks     bool // 移除 URL 与邮箱
	CollapseSpace  bool // 折叠连续空白
}

// normalize 校验并补默认值。返回给调用方一个可直接使用的 Options。
func (o Options) normalize() (Options, error) {
	switch o.Strategy {
	case "":
		o.Strategy = "auto" // R9.1 缺省 auto
	case "auto", "hierarchy":
	case "custom":
		if o.Size == 0 {
			o.Size = 500
		}
		if o.Size < customMinSize || o.Size > customMaxSize {
			return o, fmt.Errorf("custom 长度 %d 越界（允许 [%d,%d]）", o.Size, customMinSize, customMaxSize)
		}
		if o.OverlapPercent < 0 || o.OverlapPercent > customMaxOverlap {
			return o, fmt.Errorf("custom 重叠 %d%% 越界（允许 [0,%d]）", o.OverlapPercent, customMaxOverlap)
		}
	default:
		return o, fmt.Errorf("未知切分策略 %q", o.Strategy)
	}
	return o, nil
}

// var urlRe / emailRe：预处理用。编译一次。
var (
	urlRe   = regexp.MustCompile(`(?i)\b(?:https?|ftp)://[^\s）)】\]]+`)
	emailRe = regexp.MustCompile(`[^\s@]+@[^\s@]+\.[^\s@]+`)
)

// Preprocess 返回预处理后的文本（R9.5：只作用于待切分与待嵌入的文本，
// knowledge_entries.body_text 由调用方保证保持原样）。
func Preprocess(text string, stripLinks, collapseSpace bool) string {
	if stripLinks {
		text = urlRe.ReplaceAllString(text, "")
		text = emailRe.ReplaceAllString(text, "")
	}
	if collapseSpace {
		// 连续空白折叠：制表符与连续空格 → 单个空格；3 个以上换行 → 2 个换行。
		// 折叠粒度是【我方定义】：课件只说「折叠连续空白」，未规定换行如何处理。
		var b strings.Builder
		b.Grow(len(text))
		lines := strings.Split(text, "\n")
		for i, line := range lines {
			b.WriteString(collapseInLine(line))
			if i < len(lines)-1 {
				b.WriteByte('\n')
			}
		}
		out := b.String()
		for strings.Contains(out, "\n\n\n") {
			out = strings.ReplaceAll(out, "\n\n\n", "\n\n")
		}
		text = out
	}
	return text
}

func collapseInLine(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := false
	for _, r := range s {
		if r == '\t' || (r == ' ') {
			if !prevSpace {
				b.WriteByte(' ')
			}
			prevSpace = true
			continue
		}
		// 其余字符原样保留（不折叠 unicode 空白，避免破坏全角排版）
		b.WriteRune(r)
		prevSpace = false
	}
	return b.String()
}

// ChunkText 按策略切分。offsets 相对传入的 text（调用方决定传原文还是预处理后文本）。
func ChunkText(text string, opts Options) ([]Chunk, error) {
	opts, err := opts.normalize()
	if err != nil {
		return nil, err
	}
	runes := []rune(text)
	if len(runes) == 0 {
		return nil, fmt.Errorf("切分源文本为空")
	}
	if isBlankish(text) {
		return nil, fmt.Errorf("切分源文本不含任何有效字符")
	}

	var chunks []Chunk
	switch opts.Strategy {
	case "auto":
		chunks = chunkAuto(runes, autoWindowSize, autoOverlapSize)
	case "custom":
		overlap := opts.Size * opts.OverlapPercent / 100
		chunks = chunkAuto(runes, opts.Size, overlap)
	case "hierarchy":
		chunks = chunkHierarchy(runes)
	}
	for i := range chunks {
		chunks[i].Index = i
	}
	return chunks, nil
}

// chunkAuto 滑动窗口切分：每片 ≤ window，相邻片重叠 overlap（rune）。
// 断点优先级（R9.2）：段空行（\n\n）> 换行 > 句号（。！？.!?）。
// 全程找不到断点才硬切——「不在句子中间硬切」是尽力而为，≤window 是硬约束。
func chunkAuto(runes []rune, window, overlap int) []Chunk {
	isBreak := func(r rune) bool {
		return r == '\n' || r == '。' || r == '！' || r == '？' || r == '.' || r == '!' || r == '?'
	}

	chunks := []Chunk{}
	start := 0
	for start < len(runes) {
		end := start + window
		if end >= len(runes) {
			chunks = append(chunks, Chunk{Text: string(runes[start:]), Start: start, End: len(runes)})
			break
		}
		// 从 window 尽头往回找断点；回看幅度为 window 的一半——
		// 再往前找会让切片远短于 window，片数暴涨。
		cut := -1
		for i := end; i > start+window/2; i-- {
			if isBreak(runes[i-1]) {
				cut = i
				break
			}
		}
		if cut < 0 {
			cut = end // 硬切
		}
		chunks = append(chunks, Chunk{Text: string(runes[start:cut]), Start: start, End: cut})
		next := cut - overlap
		if next <= start {
			next = start + 1 // 防呆：重叠不能吃掉推进量
		}
		start = next
	}
	return chunks
}

// headingLevel 判断一行是否 Markdown 标题（#/##/###），返回级别（0=非标题）。
func headingLevel(line string) int {
	n := 0
	for n < len(line) && line[n] == '#' {
		n++
	}
	if n >= 1 && n <= 3 && n < len(line) && line[n] == ' ' {
		return n
	}
	return 0
}

// chunkHierarchy 按标题分章（R9.4）：# / ## / ### 各自成章，
// 标题行保留在章内；超过 window 的章再按 auto 规则细切（偏移保持全局）。
func chunkHierarchy(runes []rune) []Chunk {
	const window = autoWindowSize
	text := string(runes)
	lines := strings.SplitAfter(text, "\n") // SplitAfter 保留换行符，偏移拼接无损

	type section struct {
		start int // rune 偏移（含）
		end   int // rune 偏移（不含）
	}
	sections := []section{}
	pos := 0
	curStart := 0
	for _, line := range lines {
		ll := len([]rune(line))
		if headingLevel(strings.TrimRight(line, "\n")) > 0 && pos > curStart {
			sections = append(sections, section{curStart, pos})
			curStart = pos
		}
		pos += ll
	}
	if pos > curStart {
		sections = append(sections, section{curStart, pos})
	}

	chunks := []Chunk{}
	for _, sec := range sections {
		secRunes := runes[sec.start:sec.end]
		if len(secRunes) <= window {
			chunks = append(chunks, Chunk{Text: string(secRunes), Start: sec.start, End: sec.end})
			continue
		}
		for _, c := range chunkAuto(secRunes, window, autoOverlapSize) {
			c.Start += sec.start
			c.End += sec.start
			chunks = append(chunks, c)
		}
	}
	return chunks
}

// isBlankish 供测试与调试使用：判断文本是否只含空白。
func isBlankish(s string) bool {
	for _, r := range s {
		if !unicode.IsSpace(r) {
			return false
		}
	}
	return true
}
