package jwt

import (
	"testing"
	"time"
)

const benchTTL = 2 * time.Hour

func benchManager(b *testing.B) *Manager {
	b.Helper()
	return NewManager("bench-secret-that-is-long-enough-0123456789", "medicore-bench", benchTTL)
}

// 签发发生在登录与刷新时，不是每请求都做。
func BenchmarkManager_IssueAccess(b *testing.B) {
	m := benchManager(b)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := m.IssueAccess(42, "doctor"); err != nil {
			b.Fatal(err)
		}
	}
}

// 校验在**每一个已登录请求**上执行（JWT 中间件），是真正的热路径。
//
// 关注点：应当在微秒级。若涨到几十微秒，说明引入了额外的解析或分配
// （例如把 RegisteredClaims 的校验换成了更重的实现）。
func BenchmarkManager_Parse(b *testing.B) {
	m := benchManager(b)
	token, err := m.IssueAccess(42, "doctor")
	if err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := m.Parse(token); err != nil {
			b.Fatal(err)
		}
	}
}

// 被拒路径的开销必须与通过路径同量级。
//
// 若"坏令牌"提前短路返回、而"好令牌"要走完整套校验，两者的耗时差
// 就能被用来区分令牌是否有效 —— 这是一类时序侧信道。
// 基准把它变成可观测的数字：两者不应相差一个数量级。
func BenchmarkManager_ParseInvalid(b *testing.B) {
	m := benchManager(b)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = m.Parse("not.a.valid.token")
	}
}
