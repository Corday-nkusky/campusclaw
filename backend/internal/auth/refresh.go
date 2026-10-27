package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
)

// 本文件是 refresh token 的生成与摘要（add-refresh-token design D3）。
//
// 与 access token 的区别：
//   - access 是 HS256 JWT，**无状态**，验签即用，服务端不查库；
//   - refresh 是**不透明随机串**，服务端必须查库才能判定有效性 —— 这正是
//     「可吊销」的来源，也是本 change 相对 add-token-auth 的核心增量。
//
// 三条约定：
//  1. 生成用 crypto/rand（**不是** math/rand）——后者可预测，令牌可被推断。
//  2. 库里**只存 SHA-256 摘要**，不存原文：库被读也拿不到可直接使用的令牌
//     （与第 3 课课件「保存会话标识的摘要值，而非标识原文」一致）。
//  3. refresh 不签名：它的可信度来自"能在库里按摘要查到且未过期/未吊销"，
//     而非密码学签名 —— 因此不引入第二个密钥。

// refreshBytes 是 refresh 原文的随机字节数。
//
// 32 字节 = 256 bit 熵，暴力枚举不可行（design D5 的限流只是纵深防御）。
const refreshBytes = 32

// NewRefresh 生成一个新的 refresh token 原文，返回形如 base64url 的字符串。
//
// 返回值**只在签发这一刻**可见：调用方须立即把它返回给客户端、或写入响应，
// 并只把 HashRefresh 的结果落库。
func NewRefresh() (string, error) {
	raw := make([]byte, refreshBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// HashRefresh 返回 refresh 原文的 SHA-256 十六进制摘要（64 字符）。
//
// 用 SHA-256 而非 bcrypt：refresh 是高熵随机串，不存在"口令被字典穷举"的问题，
// 无需慢哈希；而每个受保护请求路径上不做 refresh 校验，慢哈希也没有必要。
func HashRefresh(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
