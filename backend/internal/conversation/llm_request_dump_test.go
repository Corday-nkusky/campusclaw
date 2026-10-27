package conversation

// 本文件**不是**回归断言，是**取证工具**：把「一次真实对话里到底发给了 LLM 什么」原样打印出来。
//
// 用法：
//
//	cd backend && go test ./internal/conversation/ -run TestDumpLLMRequest -v
//
// 为什么不用真网关：本机 MySQL / Docker 常不在跑，起不了完整后端；
// 而这条路径（buildSystem → buildMessages → ChatClient.CompleteStream）与
// conversation.Ask 里用的**是同一批函数**，所以打印出来的报文就是真实报文，
// 差别只在于「智能体提示词 / 技能 / 命中切片 / 历史」这四项输入是夹具值。
//
// 每次运行都会把请求体 JSON 全量打到 stdout（含 role/content），便于逐字核对。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Corday-nkusky/campusclaw/backend/internal/knowledge"
)

// captureServer 起一个假的 OpenAI 兼容网关：记录请求体，回一段两分片的 SSE。
func captureServer(t *testing.T, captured *string, capturedHeader *http.Header) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		*captured = string(raw)
		*capturedHeader = r.Header.Clone()
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"先把等号两边\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"看成同一个整体 [1]。\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
}

// TestDumpLLMRequest 打印三种场景下发给 LLM 的完整请求体。
func TestDumpLLMRequest(t *testing.T) {
	// ── 夹具：与种子数据同形，但不依赖 DB ──
	agentPrompt := "你是本班（1 班）生物学课程的解题助手，用简体中文作答。\n" +
		"语气温和，先确认学生对题意的理解，再给下一步提示。\n" +
		"不要直接给出最终答案。"

	skill := skillCand{
		ID:      7,
		Name:    "解题引导",
		Trigger: "不会,怎么做,卡住,思路",
		Subject: "生物",
		Description: "先让学生自己说出已知条件和要求的量。\n" +
			"若学生说不出，就换一个更小的子问题问他。\n" +
			"每一步只给一个提示，不给完整解法。",
	}

	long := strings.Repeat("有丝分裂是染色体复制后平均分配到两个子细胞的分裂方式。", 120) // 远超 800 字
	hits := []knowledge.Hit{
		{MaterialID: 11, MaterialTitle: "生物｜细胞周期（1/4）", ChunkIndex: 3, Snippet: long},
		{MaterialID: 11, MaterialTitle: "生物｜细胞周期（1/4）", ChunkIndex: 4, Snippet: "分裂间期完成 DNA 复制。"},
		{MaterialID: 12, MaterialTitle: "生物｜减数分裂（2/4）", ChunkIndex: 1, Snippet: "减数分裂产生四个单倍体细胞。"},
		{MaterialID: 13, MaterialTitle: "化学｜化学键（3/4）", ChunkIndex: 2, Snippet: "共价键是原子间共用电子对。"},
	}

	// 历史：上一轮问答（模拟已在库的 2 条消息）
	history := []message{
		{ID: 1, Role: "user", Content: "什么是细胞周期？"},
		{ID: 2, Role: "assistant", Content: "细胞周期指细胞从一次分裂结束到下一次分裂结束的全过程 [1]。"},
		// 本轮提问（handler 会先落库，再读历史；buildMessages 会把它剔除后以「资料+问题」形态重加）
		{ID: 3, Role: "user", Content: "这道题我不会，下一步该检查什么？"},
	}
	question := "这道题我不会，下一步该检查什么？"

	scenarios := []struct {
		name     string
		hits     []knowledge.Hit
		skill    skillCand
		hasSkill bool
	}{
		{"A 有资料 + 技能命中", hits, skill, true},
		{"B 有资料 + 未命中技能", hits, skillCand{}, false},
		{"C 检索落空（无资料、无技能）", nil, skillCand{}, false},
	}

	for _, sc := range scenarios {
		var body string
		var hdr http.Header
		srv := captureServer(t, &body, &hdr)
		chat := knowledge.NewChatClient(srv.URL, "test-key-should-not-leak", "course-chat")

		system := buildSystem(agentPrompt, sc.skill, sc.hasSkill, len(sc.hits) > 0)
		msgs := buildMessages(system, sc.hits, question, history)

		var deltas []string
		ans, err := chat.CompleteStream(context.Background(), msgs, func(d string) error {
			deltas = append(deltas, d)
			return nil
		})
		srv.Close()
		if err != nil {
			t.Fatalf("%s: 调用失败: %v", sc.name, err)
		}

		fmt.Printf("\n===== 场景 %s =====\n", sc.name)
		fmt.Printf("--- 请求行 ---\nPOST /v1/chat/completions\n")
		fmt.Printf("--- 请求头（节选）---\nContent-Type: %s\nAccept: %s\nAuthorization: %s\n",
			hdr.Get("Content-Type"), hdr.Get("Accept"), maskKey(hdr.Get("Authorization")))
		var pretty bytes.Buffer
		_ = json.Indent(&pretty, []byte(body), "", "  ")
		fmt.Printf("--- 请求体（逐字）---\n%s\n", pretty.String())
		fmt.Printf("--- 请求体字节数 ---\n%d\n", len(body))
		fmt.Printf("--- 响应分片 ---\n%q\n", deltas)
		fmt.Printf("--- 拼接结果 ---\n%s\n", ans)

		// 定量核对：system 长度、是否含技能正文、资料条数与截断
		fmt.Printf("--- 定量 ---\n")
		fmt.Printf("messages 条数=%d\n", len(msgs))
		fmt.Printf("system 字符数=%d\n", len([]rune(system)))
		fmt.Printf("system 含技能正文=%v\n", strings.Contains(system, "每一步只给一个提示"))
		fmt.Printf("user 末条字符数=%d\n", len([]rune(msgs[len(msgs)-1].Content)))
		fmt.Printf("user 末条含资料条数=%d\n", strings.Count(msgs[len(msgs)-1].Content, "切片 #"))
	}

	// 截断的定量证明：800 字上限
	fmt.Printf("\n===== 截断核对 =====\n")
	fmt.Printf("原始切片字符数=%d → 送入模型=%d（上限 %d）\n",
		len([]rune(long)), len([]rune(truncateRunes(long, materialMaxRunes))), materialMaxRunes)
}

// maskKey 只显示令牌长度，避免把测试密钥写进日志。
func maskKey(v string) string {
	if v == "" {
		return "(未设置)"
	}
	return fmt.Sprintf("Bearer <已省略，长度 %d>", len(v))
}
