package auth

import (
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

// dummyHash 是一个固定的 bcrypt 哈希，仅用于「用户不存在时也做一次等价耗时的哈希比较」。
//
// 为什么需要它（spec.md R1.2）：如果用户不存在时直接返回，登录接口的耗时
// 会明显短于「用户存在但口令错误」——攻击者据此即可枚举出哪些账号存在。
// 因此用户不存在时也走一次 bcrypt 比较，把耗时拉平。
//
// 这个字符串经过 bcrypt.DefaultCost 生成，代价与真实哈希一致。
var dummyHash = []byte("$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy")

// HashPassword 计算口令哈希。
func HashPassword(plain string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("计算口令哈希失败: %w", err)
	}
	return string(h), nil
}

// VerifyPassword 校验口令。
//
// 注意：**不要**用 MD5 / SHA 这类无盐哈希——它们可被彩虹表直接命中。
func VerifyPassword(hash, plain string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}

// burnTime 在用户不存在时消费与真实校验等量的时间。
func burnTime(plain string) {
	_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(plain))
}
