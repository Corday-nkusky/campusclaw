// 示范 MCP 服务器（个人项目「外部工具」的自带示例，与主应用**分开跑**）。
//
// 为什么要单独一个进程：这正是 MCP 的意义所在——工具的实现与主应用解耦，
// 主应用只认「JSON-RPC + tools/list + tools/call」这一层协议。
// 换成别人的服务器（同协议）不需要改主应用任何代码。
//
// 接口（对齐第 6 课示范服务器）：
//
//	GET  /health  → {"status":"ok"}
//	POST /mcp     → JSON-RPC 2.0（Bearer 鉴权）
//
// 协议版本 2024-11-05，支持 initialize / notifications/initialized / tools/list / tools/call。
// 工具两个：unit_convert（单位换算）、formula_hint（只给提示不给答案）。
//
// 启动：MCP_TOKEN=dev-token PORT=8090 go run ./mcp-server
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
)

type rpcReq struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcRes struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcErr         `json:"error,omitempty"`
}

type rpcErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// JSON-RPC 错误码（第 6 课示范服务器同款）
const (
	codeParse      = -32700
	codeInvalidReq = -32600
	codeNoMethod   = -32601
	codeBadParams  = -32602
	codeToolFailed = -32000
)

type tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

var tools = []tool{
	{
		Name:        "unit_convert",
		Description: "换算长度单位，支持 m / km / cm / mm。只做纯计算，不解释。",
		InputSchema: json.RawMessage(`{
			"type":"object",
			"properties":{
				"value":{"type":"number","description":"待换算的数值"},
				"from":{"type":"string","enum":["m","km","cm","mm"]},
				"to":{"type":"string","enum":["m","km","cm","mm"]}
			},
			"required":["value","from","to"]
		}`),
	},
	{
		Name:        "formula_hint",
		Description: "查某个知识点的解题思路提示（只给下一步该看什么，不给最终答案）。",
		InputSchema: json.RawMessage(`{
			"type":"object",
			"properties":{
				"topic":{"type":"string","description":"知识点，如 一元二次方程 / 细胞周期"}
			},
			"required":["topic"]
		}`),
	},
}

// 单位换算到米的系数
var toMeter = map[string]float64{"m": 1, "km": 1000, "cm": 0.01, "mm": 0.001}

func main() {
	port := envOr("PORT", "8090")
	token := os.Getenv("MCP_TOKEN")

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /mcp", func(w http.ResponseWriter, r *http.Request) {
		// 令牌只在服务端与请求头里出现，不回显
		if token != "" && r.Header.Get("Authorization") != "Bearer "+token {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}
		var req rpcReq
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeJSON(w, 200, rpcRes{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcErr{codeParse, "解析失败"}})
			return
		}
		// 通知（无 id）不返回报文
		if len(req.ID) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		res := rpcRes{JSONRPC: "2.0", ID: req.ID}
		switch req.Method {
		case "initialize":
			res.Result = map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "campusclaw-demo-tools", "version": "1.0.0"},
			}
		case "tools/list":
			res.Result = map[string]any{"tools": tools}
		case "tools/call":
			var p struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			if err := json.Unmarshal(req.Params, &p); err != nil || p.Name == "" {
				res.Error = &rpcErr{codeBadParams, "tools/call 参数不正确"}
				break
			}
			text, isErr := dispatch(p.Name, p.Arguments)
			if isErr != "" {
				res.Result = map[string]any{
					"content": []map[string]string{{"type": "text", "text": isErr}},
					"isError": true,
				}
				break
			}
			res.Result = map[string]any{
				"content": []map[string]string{{"type": "text", "text": text}},
				"isError": false,
			}
		default:
			res.Error = &rpcErr{codeNoMethod, "未实现的方法：" + req.Method}
		}
		writeJSON(w, 200, res)
	})

	log.Printf("示范 MCP 服务器监听 :%s（鉴权=%v，工具 %d 个）", port, token != "", len(tools))
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatalf("启动失败: %v", err)
	}
}

// dispatch 执行工具，第二个返回值非空表示工具级错误（会以 isError 形式回给模型）。
func dispatch(name string, args map[string]any) (string, string) {
	switch name {
	case "unit_convert":
		v, ok := args["value"].(float64)
		if !ok {
			return "", "value 必须是数字"
		}
		from, _ := args["from"].(string)
		to, _ := args["to"].(string)
		fm, ok1 := toMeter[from]
		tm, ok2 := toMeter[to]
		if !ok1 || !ok2 {
			return "", "只支持 m / km / cm / mm"
		}
		out := v * fm / tm
		return strconv.FormatFloat(out, 'f', -1, 64) + " " + to, ""
	case "formula_hint":
		topic, _ := args["topic"].(string)
		topic = strings.TrimSpace(topic)
		if topic == "" {
			return "", "topic 不能为空"
		}
		switch {
		case strings.Contains(topic, "一元二次") || strings.Contains(topic, "二次方程"):
			return "先看它能不能整理成 ax²+bx+c=0；能的话再看判别式 Δ=b²-4ac 的符号，符号决定根的个数，这一步做完再看求根公式。", ""
		case strings.Contains(topic, "细胞周期"):
			return "先把题目里的时间点放回 G1/S/G2/M 四段中的一段，再问自己这一段在做什么事（合成 RNA 与蛋白质 / 复制 DNA / 分裂）。", ""
		default:
			return "先写出题目给的已知量和要求的量，再找一个把两者连起来的公式；找不到就先看一个更小的子问题。", ""
		}
	}
	return "", "未知工具：" + name
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
