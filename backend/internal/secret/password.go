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
	// bcrypt 的输入上限是 72 字节（Blowfish 密钥调度只用得到这么多）。
	//
	// 这里显式拒绝，理由**不是**"库会静默截断"—— 实测当前依赖
	// （x/crypto v0.48.0）的 GenerateFromPassword 对 >72 字节会返回
	// ErrPasswordTooLong，并不会截断。保留这道检查是为了：
	//   1. 让"超长口令"报出与库版本无关的明确错误，
	//      而不是依赖某个具体版本的库行为（早期实现确实是静默截断的）；
	//   2. 与下面 VerifyPassword 的检查**成对**存在 ——
	//      比对侧库**不做**长度检查（CompareHashAndPassword 直接算），
	//      没有这道守卫就会白算一次 Blowfish 扩展。
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
//
// 超长输入直接返回 false 而不是交给库：bcrypt 的比对路径不检查长度
// （只有 GenerateFromPassword 检查），会照常跑完一整轮 Blowfish 扩展。
// 而 HashPassword 拒绝生成 >72 字节的散列，所以这种输入**不可能**匹配成功 ——
// 提前返回只是短路掉一次白算。
func VerifyPassword(hashed, password string) bool {
	if len(password) > 72 {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hashed), []byte(password)) == nil
}
