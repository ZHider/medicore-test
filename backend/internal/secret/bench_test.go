package secret

import (
	"strings"
	"testing"
)

func benchCipher(b *testing.B) *Cipher {
	b.Helper()

	c, err := NewCipher(strings.Repeat("ab", 32))
	if err != nil {
		b.Fatal(err)
	}
	return c
}

// 加密在注册与 HIS 同步时执行，不在请求热路径上，但数量级要可控。
func BenchmarkCipher_Encrypt(b *testing.B) {
	c := benchCipher(b)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := c.Encrypt("13800001111"); err != nil {
			b.Fatal(err)
		}
	}
}

// 解密在每次读用户资料时执行（登录、/auth/me、病历里带出患者姓名）。
func BenchmarkCipher_Decrypt(b *testing.B) {
	c := benchCipher(b)
	enc, err := c.Encrypt("13800001111")
	if err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := c.Decrypt(enc); err != nil {
			b.Fatal(err)
		}
	}
}

// Digest 是确定性 HMAC，登录时用它查 uk_phone_hash 索引。
func BenchmarkDigest(b *testing.B) {
	const pepper = "pepper-for-benchmark"

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Digest("13800001111", pepper)
	}
}

// 口令校验是整个登录流程里最贵的一步 —— bcrypt 是**故意**设计成慢的。
//
// 这条基准的价值不是"想办法让它变快"，而是：
//
//  1. 固定住 cost 参数的实际开销。cost 从 10 提到 12 会让耗时 ×4，
//     而这个改动看起来只是改了一个数字；
//  2. 给"防时序枚举"提供参照：登录失败时执行的假比对必须与真实校验**同量级**，
//     否则响应时间照样能被用来枚举账号（见 TestLogin_UnknownUserTimingIsEqualized）。
//
// 注意：cost=10 时单次约几十毫秒，b.N 会被自动调小，跑起来比其它基准慢得多。
// 用 -benchtime=3x 之类的参数可以让它更快出结果。
func BenchmarkHashPassword_Verify(b *testing.B) {
	hash, err := HashPassword("Passw0rd!", MinBcryptCost)
	if err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if !VerifyPassword(hash, "Passw0rd!") {
			b.Fatal("校验失败")
		}
	}
}
