package secret

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// randomKeyHex 每次生成随机密钥，而非使用固定常量：
// 固定密钥字面量会被密钥扫描工具误报，也让测试无谓地依赖某个特定值。
func randomKeyHex(t *testing.T) string {
	t.Helper()

	b := make([]byte, aesKeyBytes)
	_, err := rand.Read(b)
	require.NoError(t, err)
	return hex.EncodeToString(b)
}

func newTestCipher(t *testing.T) *Cipher {
	t.Helper()

	c, err := NewCipher(randomKeyHex(t))
	require.NoError(t, err)
	return c
}

func TestNewCipher_RejectsBadKey(t *testing.T) {
	tests := []struct {
		name string
		key  string
	}{
		{"非十六进制", strings.Repeat("zz", aesKeyBytes)},
		{"长度不足", strings.Repeat("ab", aesKeyBytes/2)},
		{"长度超出", strings.Repeat("ab", aesKeyBytes*2)},
		{"空值", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewCipher(tt.key)
			assert.Error(t, err)
		})
	}
}

func TestCipher_RoundTrip(t *testing.T) {
	c := newTestCipher(t)

	for _, plain := range []string{"13812345678", "110101199001011234", "中文也要能加密"} {
		enc, err := c.Encrypt(plain)
		require.NoError(t, err)
		assert.NotContains(t, string(enc), plain, "密文不应包含明文")

		got, err := c.Decrypt(enc)
		require.NoError(t, err)
		assert.Equal(t, plain, got)
	}
}

// 相同明文两次加密必须产生不同密文，否则等于泄露"两条记录是同一个手机号"。
func TestCipher_NonceIsRandom(t *testing.T) {
	c := newTestCipher(t)

	first, err := c.Encrypt("13812345678")
	require.NoError(t, err)
	second, err := c.Encrypt("13812345678")
	require.NoError(t, err)

	assert.NotEqual(t, first, second)
}

func TestCipher_RejectsTamperedCiphertext(t *testing.T) {
	c := newTestCipher(t)
	enc, err := c.Encrypt("13812345678")
	require.NoError(t, err)

	t.Run("篡改密文", func(t *testing.T) {
		tampered := make([]byte, len(enc))
		copy(tampered, enc)
		tampered[len(tampered)-1] ^= 0xFF // 翻转 tag 的一个 bit
		_, err := c.Decrypt(tampered)
		assert.ErrorIs(t, err, ErrCiphertextInvalid)
	})

	t.Run("密文过短", func(t *testing.T) {
		_, err := c.Decrypt([]byte{1, 2, 3})
		assert.ErrorIs(t, err, ErrCiphertextInvalid)
	})

	t.Run("换密钥无法解密", func(t *testing.T) {
		other, err := NewCipher(randomKeyHex(t))
		require.NoError(t, err)
		_, err = other.Decrypt(enc)
		assert.ErrorIs(t, err, ErrCiphertextInvalid)
	})
}

// 摘要必须确定性：登录要靠它反查用户，随机盐会让登录彻底不可用。
func TestDigest_IsDeterministic(t *testing.T) {
	a := Digest("13812345678", "pepper")
	b := Digest("13812345678", "pepper")
	assert.Equal(t, a, b)
	assert.Len(t, a, 64, "需匹配 users.phone_hash CHAR(64)")

	assert.NotEqual(t, a, Digest("13812345679", "pepper"), "不同手机号摘要必须不同")
	assert.NotEqual(t, a, Digest("13812345678", "other-pepper"), "换 pepper 摘要必须不同")
}

func TestPassword_HashAndVerify(t *testing.T) {
	hash, err := HashPassword("correct-horse", MinBcryptCost)
	require.NoError(t, err)
	assert.NotEqual(t, "correct-horse", hash)

	assert.True(t, VerifyPassword(hash, "correct-horse"))
	assert.False(t, VerifyPassword(hash, "wrong-password"))
}

func TestPassword_RejectsOver72Bytes(t *testing.T) {
	// bcrypt 会静默截断超过 72 字节的输入，必须显式拒绝，否则长密码给出虚假安全感。
	long := strings.Repeat("a", 73)
	_, err := HashPassword(long, MinBcryptCost)
	assert.Error(t, err)
	assert.False(t, VerifyPassword("whatever", long))
}

func TestHashPassword_UpgradesWeakCost(t *testing.T) {
	hash, err := HashPassword("secret", 4) // 低于下限
	require.NoError(t, err)
	// cost 应被提升到默认值 10，前缀形如 $2a$10$
	assert.Contains(t, hash, "$10$")
}

func TestRandomToken_IsUnique(t *testing.T) {
	seen := make(map[string]struct{}, 100)
	for i := 0; i < 100; i++ {
		tok, err := RandomToken()
		require.NoError(t, err)
		assert.Len(t, tok, RefreshTokenBytes*2)
		_, dup := seen[tok]
		assert.False(t, dup, "刷新令牌不可重复")
		seen[tok] = struct{}{}
	}
}
