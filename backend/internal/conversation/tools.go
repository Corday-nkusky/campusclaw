package conversation

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Corday-nkusky/campusclaw/backend/internal/knowledge"
	"github.com/Corday-nkusky/campusclaw/backend/internal/mcp"
	"github.com/Corday-nkusky/campusclaw/backend/internal/obslog"
)

// 本文件是第 6 课的「调用循环」：模型决定调什么，宿主负责连线、执行、回填、留痕。
//
// 两个闸门都设（课件 bridge 页）：轮次上限 + 累计调用次数上限。
// 只设一个是不够的——模型可以在单轮里要求并行调 5 个工具，轮次闸门拦不住。
const (
	maxToolRounds = 4
	maxToolCalls  = 8
)

// toolRef 记住某个函数名回指哪台服务器的哪个工具。
type toolRef struct {
	Server string
	Tool   string
}

// runCtx 是一轮问答里工具链路的上下文。
type runCtx struct {
	convID    int64
	classID   int64
	skillName string
	specs     []knowledge.ToolSpec
	index     map[string]toolRef
	log       *[]map[string]any
}

// fnName 把「工具名@服务器名」映射成合法函数名。
//
// 必须映射：function.name 只允许字母数字下划线连字符，`@` 会被网关拒掉；
// 连字符也统一换成下划线，避免不同模型对 `-` 的处理差异。
func fnName(server, tool string) string {
	return tool + "__" + strings.ReplaceAll(server, "-", "_")
}

// prepareTools 解析技能里的工具引用并组装请求的 tools 数组。
//
// 返回：规格、函数名到（服务器,工具）的索引、不可用引用（只用于日志）。
// 不可用的引用**不阻断本轮问答**，只是这个工具不出现。
func (h *Handlers) prepareTools(ctx context.Context, classID int64, skill skillCand, hasSkill bool) ([]knowledge.ToolSpec, map[string]toolRef, []string) {
	if !hasSkill || h.MCP == nil || strings.TrimSpace(skill.Tools) == "" {
		return nil, nil, nil
	}
	resolved, bad := mcp.ResolveTools(ctx, h.Pool, classID, splitTools(skill.Tools))
	if len(resolved) == 0 {
		return nil, nil, bad
	}
	specs := make([]knowledge.ToolSpec, 0, len(resolved))
	index := map[string]toolRef{}
	for _, r := range resolved {
		schema := r.Def.InputSchema
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		name := fnName(r.ServerName, r.Def.Name)
		specs = append(specs, knowledge.ToolSpec{
			Type: "function",
			Function: knowledge.FunctionSpec{
				Name:        name,
				Description: r.Def.Description,
				Parameters:  schema,
			},
		})
		index[name] = toolRef{Server: r.ServerName, Tool: r.Def.Name}
	}
	return specs, index, bad
}

// runWithTools 执行「模型出场多次」的循环，最后一段正文照常流式下发。
//
// 终止条件（四种）：模型不再要求调用工具 / 轮次用完 / 累计调用次数用完 / 出错。
// 任何一次工具失败都**把错误当结果回填给模型**，由模型决定怎么向用户交代——
// 宿主不替模型写"工具坏了"这类话。
func (h *Handlers) runWithTools(
	ctx context.Context,
	rc runCtx,
	msgs []knowledge.ChatMessage,
	onDelta func(string) error,
	emit func(string, any),
) (string, error) {
	calls := 0
	for round := 1; round <= maxToolRounds; round++ {
		res, err := h.Chat.CompleteStreamTools(ctx, msgs, rc.specs, onDelta)
		if err != nil {
			return res.Content, err
		}
		if len(res.ToolCalls) == 0 {
			return res.Content, nil
		}

		// 助手这一轮的话（可能为空）+ 它要求的调用，一起入上下文
		msgs = append(msgs, knowledge.ChatMessage{
			Role:      "assistant",
			Content:   res.Content,
			ToolCalls: res.ToolCalls,
		})

		for _, tc := range res.ToolCalls {
			if calls >= maxToolCalls {
				msgs = append(msgs, knowledge.ChatMessage{
					Role:       "tool",
					ToolCallID: tc.ID,
					Name:       tc.Function.Name,
					Content:    "本轮工具调用次数已达上限，不再执行。请基于已有信息作答。",
				})
				rc.appendLog(map[string]any{
					"name": tc.Function.Name, "arguments": tc.Function.Arguments, "status": "skipped",
				})
				continue
			}
			ref, ok := rc.index[tc.Function.Name]
			if !ok {
				msgs = append(msgs, knowledge.ChatMessage{
					Role:       "tool",
					ToolCallID: tc.ID,
					Name:       tc.Function.Name,
					Content:    "未登记的工具名：" + tc.Function.Name,
				})
				continue
			}
			calls++

			emit("tool_call", map[string]any{
				"name": ref.Tool, "server": ref.Server, "arguments": tc.Function.Arguments,
			})

			start := time.Now()
			out, callErr := h.MCP.Execute(ctx, rc.classID, rc.convID, rc.skillName, ref.Server, ref.Tool, tc.Function.Arguments)
			ms := int(time.Since(start).Milliseconds())
			status := "ok"
			if callErr != nil {
				status = "error"
				// 失败必须回传模型（课件硬约束），措辞保持中性
				out = "工具执行失败：" + callErr.Error()
				obslog.ErrorCtx(ctx, "tool.execute_failed", "外部工具执行失败",
					"tool", ref.Tool, "server", ref.Server, "detail", callErr.Error())
			}

			emit("tool_result", map[string]any{
				"name": ref.Tool, "server": ref.Server, "status": status,
				"duration_ms": ms, "result": clipRunes(out, 500),
			})
			rc.appendLog(map[string]any{
				"name": fnName(ref.Server, ref.Tool), "server": ref.Server, "tool": ref.Tool,
				"arguments": tc.Function.Arguments, "status": status, "duration_ms": ms,
			})

			msgs = append(msgs, knowledge.ChatMessage{
				Role:       "tool",
				ToolCallID: tc.ID,
				Name:       tc.Function.Name,
				Content:    out,
			})
		}
	}
	// 轮次用完：不带 tools 再问一次，逼它用现有信息收尾
	res, err := h.Chat.CompleteStream(ctx, msgs, onDelta)
	if err != nil {
		return res, err
	}
	return res, nil
}

func (rc runCtx) appendLog(entry map[string]any) {
	if rc.log == nil {
		return
	}
	*rc.log = append(*rc.log, entry)
}

// splitTools 把技能里的工具列切词（英文/中文逗号都认）。
func splitTools(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		switch r {
		case ',', '，', '、', ';', '；':
			return true
		}
		return false
	})
}

func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + fmt.Sprintf("…（共 %d 字）", len(r))
}
