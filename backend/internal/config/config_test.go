package config

import (
	"strings"
	"testing"
)

// baseEnv 把 requiredKeys 里的每一项都填上「值里带 SECRET-MARKER 的假数据」。
//
// 两用：
//   - 「九项齐全」用例直接 Load，应当成功；
//   - 「缺任一项 / 泄漏」用例在此基础上把某一键置空或改成非法值。
//
// 值统一编成 SECRET-VALUE-<键名>：泄漏断言只要在错误文本里 grep 这个前缀即可，
// 不需要逐键硬编码。t.Setenv 会覆盖继承自外层进程的真实环境，测试互不污染。
func baseEnv(t *testing.T) map[string]string {
	t.Helper()
	vals := map[string]string{}
	for _, k := range requiredKeys {
		v := "SECRET-VALUE-" + k
		if k == "EMBEDDING_DIM" {
			v = "2048"
		}
		if k == "SESSION_TTL_HOURS" {
			v = "24"
		}
		if k == "MAX_UPLOAD_BYTES" {
			v = "10485760"
		}
		if k == "LOGIN_FAIL_THRESHOLD" {
			v = "5"
		}
		if k == "LOGIN_LOCK_MINUTES" {
			v = "15"
		}
		vals[k] = v
		t.Setenv(k, v)
	}
	// 网关 base 带尾部斜杠：顺带验证 trimBase 的归一化（design D9）
	t.Setenv("EMBEDDING_API_BASE", "http://gateway.example/api/")
	t.Setenv("CHAT_API_BASE", "http://gateway.example/v1")
	t.Setenv("API_PORT", "18080")
	return vals
}

// 九项（连同原有各键）齐全 → Load 成功，且检索九项逐字段落位正确（tasks 1.4）。
func TestLoadAllPresent(t *testing.T) {
	baseEnv(t)
	c, err := Load()
	if err != nil {
		t.Fatalf("配置齐全时 Load 不应失败，得到：%v", err)
	}
	if c.EmbeddingAPIKey != "SECRET-VALUE-EMBEDDING_API_KEY" {
		t.Errorf("EmbeddingAPIKey 未落位：%q", c.EmbeddingAPIKey)
	}
	if c.ChatModel != "SECRET-VALUE-CHAT_MODEL" {
		t.Errorf("ChatModel 未落位：%q", c.ChatModel)
	}
	if c.EmbeddingDim != 2048 {
		t.Errorf("EmbeddingDim = %d，想要 2048", c.EmbeddingDim)
	}
	// trimBase：尾部斜杠去掉、无斜杠的保持原样
	if c.EmbeddingAPIBase != "http://gateway.example/api" {
		t.Errorf("EmbeddingAPIBase 尾部斜杠未去除：%q", c.EmbeddingAPIBase)
	}
	if c.ChatAPIBase != "http://gateway.example/v1" {
		t.Errorf("ChatAPIBase 被误改：%q", c.ChatAPIBase)
	}
	if c.QdrantURL != "SECRET-VALUE-QDRANT_URL" {
		t.Errorf("QdrantURL 未落位：%q", c.QdrantURL)
	}
	if c.QdrantCollection != "SECRET-VALUE-QDRANT_COLLECTION" {
		t.Errorf("QdrantCollection 未落位：%q", c.QdrantCollection)
	}
	if c.APIPort != "18080" {
		t.Errorf("APIPort = %q，想要 18080", c.APIPort)
	}
}

// 缺任一项 → Load 失败，且错误文本指明缺的是哪个键（R8.1 / tasks 1.4）。
// 逐键穷举：requiredKeys 里每一个都置空试一遍，一个都不能漏。
func TestLoadFailsOnEachMissingKey(t *testing.T) {
	for _, k := range requiredKeys {
		t.Run(k, func(t *testing.T) {
			baseEnv(t)
			t.Setenv(k, "") // 置空 = 缺失（missingKeys 按 TrimSpace 后为空判定）
			c, err := Load()
			if err == nil {
				t.Fatalf("缺 %s 时 Load 应失败，却得到了配置（%+v）", k, c)
			}
			if !strings.Contains(err.Error(), k) {
				t.Errorf("错误文本应指明缺失键 %s，实际：%v", k, err)
			}
		})
	}
}

// EMBEDDING_DIM 非正整数（0 / 负数 / 非数字 / 纯空白）→ 启动失败并指明（tasks 1.2/1.4）。
func TestLoadRejectsBadEmbeddingDim(t *testing.T) {
	for _, bad := range []string{"0", "-4", "abc", "12.5", "  "} {
		t.Run(bad, func(t *testing.T) {
			baseEnv(t)
			t.Setenv("EMBEDDING_DIM", bad)
			if _, err := Load(); err == nil {
				t.Fatalf("EMBEDDING_DIM=%q 应导致 Load 失败", bad)
			} else if !strings.Contains(err.Error(), "EMBEDDING_DIM") {
				t.Errorf("错误应指明 EMBEDDING_DIM，实际：%v", err)
			}
		})
	}
}

// 密钥不泄漏（R8.2 / tasks 1.5）：Load 失败时错误文本里只允许出现键名，
// 不允许出现任何已填键的「值」。错误会一路打到启动日志，值一旦进去就是泄漏。
func TestLoadErrorDoesNotLeakSecretValues(t *testing.T) {
	// 情形一：缺一项（最常见的启动失败形态）
	vals := baseEnv(t)
	t.Setenv("QDRANT_URL", "")
	_, err := Load()
	if err == nil {
		t.Fatal("预置缺项失败，用例自身坏了")
	}
	for k, v := range vals {
		if k == "QDRANT_URL" || strings.HasPrefix(v, "http://") {
			continue // 被置空/被覆盖的键没有「值」可言；base URL 非密钥
		}
		if strings.Contains(err.Error(), v) {
			t.Errorf("错误文本泄漏了 %s 的值：%v", k, err)
		}
	}

	// 情形二：EMBEDDING_DIM 非法（校验类失败的另一条路径）
	vals = baseEnv(t)
	t.Setenv("EMBEDDING_DIM", "abc")
	_, err = Load()
	if err == nil {
		t.Fatal("预置非法维度失败，用例自身坏了")
	}
	for k, v := range vals {
		if strings.HasPrefix(v, "http://") {
			continue
		}
		if strings.Contains(err.Error(), v) {
			t.Errorf("错误文本泄漏了 %s 的值：%v", k, err)
		}
	}
}
