package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// strongSecrets 是三个互不相同、无重复周期、无长升序串的合法密钥。
// 刻意不能用 strings.Repeat 之类的简写：那本身就是短周期重复，会被正确拒绝。
var strongSecrets = struct {
	aesKey, pepper, jwtSecret string
}{
	// 两半刻意不同，避免构成 period=32 的重复
	aesKey:    "9f2a4c7e1b8d0f3a5c6e8b1d4a7f2c9e" + "3b7d1e5a9c2f4b8d6a0e3c7b1d5f9a2c",
	pepper:    "p3pper-9f2a4c7e1b8d0f3a",
	jwtSecret: "s3cr3t-7d1f9a2c4e6b8d0f3a5c1e9b2a4f",
}

// baseConfig 返回一份密钥全部合法的配置，供各用例按需破坏其中一项。
func baseConfig() *Config {
	c := &Config{}
	c.App.Env = "dev"
	c.Security.AESKey = strongSecrets.aesKey
	c.Security.PhonePepper = strongSecrets.pepper
	c.Security.JWTSecret = strongSecrets.jwtSecret
	c.Security.AccessTokenTTL = 0
	c.Security.RefreshTokenTTL = 0
	c.Security.BcryptCost = 10
	return c
}

func TestValidate_AcceptsStrongSecrets(t *testing.T) {
	c := baseConfig()
	c.Security.AccessTokenTTL = 3600
	c.Security.RefreshTokenTTL = 86400

	assert.NoError(t, c.Validate())
}

func TestValidate_RejectsEmptySecrets(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
		field  string
	}{
		{"aes_key 为空", func(c *Config) { c.Security.AESKey = "" }, "security.aes_key"},
		{"phone_pepper 为空", func(c *Config) { c.Security.PhonePepper = "" }, "security.phone_pepper"},
		{"jwt_secret 为空", func(c *Config) { c.Security.JWTSecret = "" }, "security.jwt_secret"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := baseConfig()
			tt.mutate(c)

			err := c.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.field)
			// 报错必须给出可执行的下一步，而不是只说"未设置"。
			assert.Contains(t, err.Error(), "genlocal")
		})
	}
}

func TestValidate_RejectsShortSecrets(t *testing.T) {
	c := baseConfig()
	c.Security.AESKey = strings.Repeat("a1b2", 8) // 32 字符，不足 64

	err := c.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "长度不足")
}

// 这是本文件的核心：按特征识别可预测密钥，
// 而不是维护一份"已知坏值"黑名单——黑名单既拦不全，又会把密钥字面量带回仓库。
func TestValidate_RejectsPredictableSecrets(t *testing.T) {
	tests := []struct {
		name   string
		secret string
		reason string
	}{
		{"连续升序序列", "0123456789f3a5c6e8b1d4a7f2c9e3b7d1e5a9c2f4", "连续升序"},
		{"单一字符重复", strings.Repeat("a", 64), "单一字符"},
		{"短周期重复", strings.Repeat("ab", 32), "短周期"},
		{"含占位词 change-me", strings.Repeat("x7f2", 8) + "change-me", "占位词"},
		{"含占位词 example", "example-key-9f2a4c7e1b8d0f3a5c6e8b1d", "占位词"},
		{"含占位词 placeholder", "placeholder-7d1f9a2c4e6b8d0f3a5c", "占位词"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := baseConfig()
			c.Security.JWTSecret = tt.secret

			err := c.Validate()
			require.Error(t, err, "可预测的密钥必须被拒绝")
			assert.Contains(t, err.Error(), tt.reason)
			assert.Contains(t, err.Error(), "密码学随机值")
		})
	}
}

func TestValidate_RejectsWeakBcryptCost(t *testing.T) {
	c := baseConfig()
	c.Security.BcryptCost = 4

	err := c.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bcrypt_cost")
}

func TestLocalConfigPath(t *testing.T) {
	assert.Equal(t, "configs/config.local.yaml", localConfigPath("configs/config.yaml"))
	assert.Equal(t, "a.local.yml", localConfigPath("a.yml"))
	assert.Equal(t, "noext.local", localConfigPath("noext"))
}

// 特征检测函数本身的边界。
func TestLooksPredictable(t *testing.T) {
	weak := map[string]string{
		"0123456789abcdefgh":     "含连续升序序列",
		strings.Repeat("z", 20):  "单一字符重复",
		strings.Repeat("abc", 8): "短周期重复",
	}
	for input, wantReason := range weak {
		reason, got := looksPredictable(input)
		assert.True(t, got, "%q 应被判定为可预测", input)
		assert.Contains(t, reason, wantReason)
	}

	strong := []string{
		"9f2a4c7e1b8d0f3a5c6e8b1d4a7f2c9e",
		"a1b2c3d4e5f60718293a4b5c6d7e8f90",
	}
	for _, input := range strong {
		_, got := looksPredictable(input)
		assert.False(t, got, "%q 不应被误判", input)
	}
}
