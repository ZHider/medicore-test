package config

import (
	"errors"
	"fmt"
	"strings"
)

// 密钥强度要求。aes_key 必须是 32 字节的十六进制（64 字符），
// 长度同时由 secret.NewCipher 强制；这里提前给出更可读的报错。
const (
	minAESKeyLen      = 64
	minPhonePepperLen = 16
	minJWTSecretLen   = 32
)

// 密钥中不应出现的占位词。
var placeholderHints = []string{
	"change-me", "changeme", "placeholder", "example",
	"your-", "yoursecret", "todo", "dummy", "sample",
}

// Validate 做启动前的配置校验。
//
// 密钥检查对**所有环境**生效：仓库中不含任何密钥（见 configs/config.yaml 说明），
// 开发环境也必须先运行 `go run ./cmd/genlocal` 生成本地密钥文件。
func (c *Config) Validate() error {
	var errs []error

	errs = append(errs, checkSecret("security.aes_key", c.Security.AESKey, minAESKeyLen)...)
	errs = append(errs, checkSecret("security.phone_pepper", c.Security.PhonePepper, minPhonePepperLen)...)
	errs = append(errs, checkSecret("security.jwt_secret", c.Security.JWTSecret, minJWTSecretLen)...)

	if c.Security.AccessTokenTTL <= 0 {
		errs = append(errs, errors.New("security.access_token_ttl 必须为正"))
	}
	if c.Security.RefreshTokenTTL <= 0 {
		errs = append(errs, errors.New("security.refresh_token_ttl 必须为正"))
	}
	if c.Security.BcryptCost < 10 {
		errs = append(errs, fmt.Errorf("security.bcrypt_cost 不得低于 10，当前 %d", c.Security.BcryptCost))
	}

	return errors.Join(errs...)
}

func checkSecret(name, value string, minLen int) []error {
	if value == "" {
		return []error{fmt.Errorf(
			"%s 未设置：开发环境请运行 `go run ./cmd/genlocal`，生产环境注入环境变量", name)}
	}
	if len(value) < minLen {
		return []error{fmt.Errorf("%s 长度不足（要求 ≥%d，当前 %d）", name, minLen, len(value))}
	}
	if reason, weak := looksPredictable(value); weak {
		return []error{fmt.Errorf("%s %s，必须使用密码学随机值", name, reason)}
	}
	return nil
}

// looksPredictable 按特征判断密钥是否可预测。
//
// 刻意不维护"已知坏值"黑名单：黑名单只能拦住预先想到的那几个值，
// 换一个占位串就绕过去了，而且黑名单本身会把密钥字面量带进仓库。
// 按特征判断可以拦住一整类问题。
func looksPredictable(s string) (reason string, weak bool) {
	lower := strings.ToLower(s)

	for _, hint := range placeholderHints {
		if strings.Contains(lower, hint) {
			return fmt.Sprintf("包含占位词 %q", hint), true
		}
	}
	if isSingleRepeatedChar(s) {
		return "由单一字符重复构成", true
	}
	if hasRepeatingPeriod(s) {
		return "由短周期重复构成", true
	}
	if containsAscendingRun(s, 8) {
		return "包含连续升序序列", true
	}
	return "", false
}

func isSingleRepeatedChar(s string) bool {
	for i := 1; i < len(s); i++ {
		if s[i] != s[0] {
			return false
		}
	}
	return len(s) > 0
}

// hasRepeatingPeriod 识别 "abcabcabc" 这类由短周期平铺而成的串。
func hasRepeatingPeriod(s string) bool {
	n := len(s)
	for period := 1; period <= n/2; period++ {
		if n%period != 0 {
			continue
		}
		repeats := true
		for i := period; i < n; i++ {
			if s[i] != s[i%period] {
				repeats = false
				break
			}
		}
		if repeats {
			return true
		}
	}
	return false
}

// containsAscendingRun 识别 "0123456789"、"abcdefgh" 这类连续序列。
func containsAscendingRun(s string, minRun int) bool {
	run := 1
	for i := 1; i < len(s); i++ {
		if s[i] == s[i-1]+1 {
			run++
			if run >= minRun {
				return true
			}
			continue
		}
		run = 1
	}
	return false
}
