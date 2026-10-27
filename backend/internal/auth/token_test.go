package auth

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

var testNow = time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)

func mustSign(t *testing.T, secret []byte, uid int64, ttl time.Duration) string {
	t.Helper()
	tok, err := SignToken(secret, uid, ttl, testNow)
	if err != nil {
		t.Fatalf("SignToken 失败: %v", err)
	}
	return tok
}

func TestSignAndParseRoundTrip(t *testing.T) {
	secret := []byte("test-secret")
	tok := mustSign(t, secret, 42, time.Hour)

	uid, err := ParseToken(secret, tok, testNow.Add(time.Minute))
	if err != nil {
		t.Fatalf("有效 token 被拒: %v", err)
	}
	if uid != 42 {
		t.Fatalf("user_id = %d, 期望 42", uid)
	}
}

func TestStructureIsThreeSegments(t *testing.T) {
	tok := mustSign(t, []byte("k"), 1, time.Hour)
	if got := strings.Count(tok, "."); got != 2 {
		t.Fatalf("token 段数 = %d（点 %d 个），JWT 应为三段", got+1, got)
	}
	// header 必须声明 HS256
	raw, err := base64.RawURLEncoding.DecodeString(strings.Split(tok, ".")[0])
	if err != nil {
		t.Fatalf("header 非法 base64url: %v", err)
	}
	var h map[string]string
	if err := json.Unmarshal(raw, &h); err != nil || h["alg"] != "HS256" {
		t.Fatalf("header alg = %v, 期望 HS256（err=%v）", h["alg"], err)
	}
}

func TestTamperedPayloadRejected(t *testing.T) {
	secret := []byte("test-secret")
	tok := mustSign(t, secret, 1, time.Hour)
	parts := strings.Split(tok, ".")

	// 把 payload 里的 user_id 从 1 改成 2（重新编码但不动签名）
	payload, _ := json.Marshal(map[string]int64{"user_id": 2, "exp": testNow.Add(time.Hour).Unix()})
	parts[1] = base64.RawURLEncoding.EncodeToString(payload)
	tampered := strings.Join(parts, ".")

	if _, err := ParseToken(secret, tampered, testNow); err == nil {
		t.Fatal("篡改 payload 的 token 被接受 —— 验签未生效（R1.3）")
	}
}

func TestTamperedSignatureRejected(t *testing.T) {
	secret := []byte("test-secret")
	tok := mustSign(t, secret, 1, time.Hour)
	parts := strings.Split(tok, ".")
	sig := []byte(parts[2])
	// 翻转签名末字符（base64url 字母表内必有一个不同的合法字符）
	replacement := byte('A')
	if sig[len(sig)-1] == 'A' {
		replacement = 'B'
	}
	sig[len(sig)-1] = replacement
	parts[2] = string(sig)

	if _, err := ParseToken(secret, strings.Join(parts, "."), testNow); err == nil {
		t.Fatal("篡改签名的 token 被接受 —— 验签未生效（R1.3）")
	}
}

func TestWrongSecretRejected(t *testing.T) {
	tok := mustSign(t, []byte("secret-a"), 1, time.Hour)
	if _, err := ParseToken([]byte("secret-b"), tok, testNow); err == nil {
		t.Fatal("错误密钥签发的 token 被接受")
	}
}

func TestAlgNoneRejected(t *testing.T) {
	// 构造 alg=none 的 token：payload 由真实密钥可读编码，但签名置空
	payload, _ := json.Marshal(map[string]int64{"user_id": 1, "exp": testNow.Add(time.Hour).Unix()})
	evil := "eyJhbGciOiJub25lIiwidHlwIjoiSldUIn0." +
		base64.RawURLEncoding.EncodeToString(payload) + "."
	if _, err := ParseToken([]byte("test-secret"), evil, testNow); err == nil {
		t.Fatal("alg=none 的 token 被接受 —— 算法白名单未生效（R1.3 / design D3）")
	}
}

func TestExpiredRejected(t *testing.T) {
	secret := []byte("test-secret")
	tok := mustSign(t, secret, 1, time.Hour)
	// 在签发 2 小时后验证 → 已过期
	if _, err := ParseToken(secret, tok, testNow.Add(2*time.Hour)); err == nil {
		t.Fatal("过期 token 被接受（exp 校验未生效）")
	}
}

func TestMalformedRejected(t *testing.T) {
	secret := []byte("k")
	cases := []string{
		"",
		"abc",
		"a.b",
		"a.b.c.d",
		".eyJ1c2VyX2lkIjoxfQ.abc",
		"eyJhbGciOiJIUzI1NiJ9..abc",
	}
	for _, c := range cases {
		if _, err := ParseToken(secret, c, testNow); err == nil {
			t.Fatalf("畸形 token %q 被接受", c)
		}
	}
}
