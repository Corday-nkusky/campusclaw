package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// 本文件是无状态 JWT（HS256）的签发与验证，取代迭代 1 的服务端会话存储。
//
// 为什么不引第三方库（design.md D3）：HS256 = HMAC-SHA256 后 base64url，
// stdlib 直接覆盖；显式实现让 JWT 的三段结构在课程里可逐行讲解。
//
// 三条安全红线：
//  1. 算法白名单：header.alg 必须恰为 "HS256"，其余（含 "none"）一律拒绝
//     —— 防 alg:none / 算法混淆攻击（design.md D7）。
//  2. 常数时间比较：签名比对用 hmac.Equal，不用 ==（防时序侧信道）。
//  3. 签名覆盖完整两段：HMAC 的输入是 "header.payload" 整体，payload 改一字节即失效。
//
// payload 只放 user_id / iat / exp 三个声明（design.md D1）：
// role 与 class_id **不进 token**，验签后由中间件查 users / classes 表取得
// —— 改角色或调班下一次请求即生效，沿用迭代 1 design D1/D2 的性质。

const algHS256 = "HS256"

// tokenTTL 与签发密钥由调用方注入（来自 SESSION_SECRET / SESSION_TTL_HOURS）。
// 签发与验证共用同一密钥，HS256 是对称算法。

var (
	// ErrTokenMalformed 表示不是三段式结构 / base64 / JSON 不合法。
	ErrTokenMalformed = errors.New("token 格式非法")
	// ErrTokenBadAlg 表示 header.alg 不是 HS256。
	ErrTokenBadAlg = errors.New("token 算法不被接受")
	// ErrTokenBadSig 表示签名校验失败（payload 或签名被篡改、密钥不符）。
	ErrTokenBadSig = errors.New("token 签名校验失败")
	// ErrTokenExpired 表示 exp 已过。
	ErrTokenExpired = errors.New("token 已过期")
)

// SignToken 为 userID 签发一个 HS256 JWT，返回形如 `header.payload.signature` 的字符串。
func SignToken(secret []byte, userID int64, ttl time.Duration, now time.Time) (string, error) {
	header, err := b64json(map[string]string{"alg": algHS256, "typ": "JWT"})
	if err != nil {
		return "", err
	}
	payload, err := b64json(map[string]int64{
		"user_id": userID,
		"iat":     now.Unix(),
		"exp":     now.Add(ttl).Unix(),
	})
	if err != nil {
		return "", err
	}
	signingInput := header + "." + payload
	return signingInput + "." + sign(secret, signingInput), nil
}

// ParseToken 验证 token 并返回其中的 user_id。
//
// 任何一步失败都返回错误，调用方对外的响应必须同形（401，R1.3）。
func ParseToken(secret []byte, token string, now time.Time) (int64, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return 0, ErrTokenMalformed
	}

	// ① header：必须声明 alg = HS256
	var h struct {
		Alg string `json:"alg"`
		Typ string `json:"typ"`
	}
	if err := unb64json(parts[0], &h); err != nil {
		return 0, ErrTokenMalformed
	}
	if h.Alg != algHS256 {
		return 0, ErrTokenBadAlg
	}

	// ② 签名：对 "header.payload" 整体重算 HMAC，常数时间比较
	signingInput := parts[0] + "." + parts[1]
	if !hmac.Equal([]byte(sign(secret, signingInput)), []byte(parts[2])) {
		return 0, ErrTokenBadSig
	}

	// ③ payload：取 user_id，校验 exp
	var p struct {
		UserID int64 `json:"user_id"`
		Issued int64 `json:"iat"`
		Exp    int64 `json:"exp"`
	}
	if err := unb64json(parts[1], &p); err != nil {
		return 0, ErrTokenMalformed
	}
	if p.Exp <= now.Unix() {
		return 0, ErrTokenExpired
	}
	if p.UserID <= 0 {
		return 0, ErrTokenMalformed
	}
	return p.UserID, nil
}

// ── 内部工具 ──────────────────────────────────────────────────

// sign 计算 HMAC-SHA256 并做 base64url（无填充）。
func sign(secret []byte, signingInput string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(signingInput))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// b64json 序列化为 JSON 后做 base64url（无填充）。
func b64json(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("序列化 JWT 段失败: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// unb64json 解 base64url（容忍带填充的写法）后反序列化 JSON。
func unb64json(seg string, v any) error {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(seg, "="))
	if err != nil {
		return fmt.Errorf("解码 JWT 段失败: %w", err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("解析 JWT 段失败: %w", err)
	}
	return nil
}
