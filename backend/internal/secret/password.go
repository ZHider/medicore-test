package secret

import (
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

// bcrypt 代价因子的合理区间。低于默认值会显著削弱抗暴力破解能力。
const (
	MinBcryptCost     = 10
	DefaultBcryptCost = 10
)

// DummyPassword 是用于登录时序防护的占位口令。
//
// 登录时若账号不存在，用它的散列执行一次**代价完全相同**的口令比对，
// 使"账号不存在"与"密码错误"的响应时间无法被区分。
// 否则攻击者能靠响应时间枚举出哪些手机号已注册，
// 让"登录失败文案不区分账号是否存在"的努力失去意义。
const DummyPassword = "medicore-timing-equalizer"

// HashPassword 生成 bcrypt 口令散列。
func HashPassword(password string, cost int) (string, error) {
	if cost < MinBcryptCost {
		cost = DefaultBcryptCost
	}
	// bcrypt 输入上限 72 字节，超出部分会被静默截断；这里显式报错避免"长密码无效"的安全错觉。
	if len(password) > 72 {
		return "", fmt.Errorf("password exceeds bcrypt 72-byte limit")
	}
	hashed, err := bcrypt.GenerateFromPassword([]byte(password), cost)
	if err != nil {
		return "", fmt.Errorf("bcrypt generate: %w", err)
	}
	return string(hashed), nil
}

// VerifyPassword 校验口令。散列不匹配返回 false，不返回具体原因。
func VerifyPassword(hashed, password string) bool {
	if len(password) > 72 {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hashed), []byte(password)) == nil
}
