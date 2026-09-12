package jwt

import (
	"testing"
	"time"
	"unicode/utf8"
)

// fuzzTTL 是 fuzz 用例里签发令牌的有效期。
//
// 必须显式乘 time.Second：NewManager 的第三个参数是 time.Duration，
// 直接写 3600 会被当成 3600 纳秒 —— 令牌刚签发就已过期，
// 表现为"自己签发的令牌解析失败：token expired"。
// （这个坑是被 fuzz 的种子语料当场炸出来的。）
const fuzzTTL = time.Hour

// Parse 的输入是 HTTP 头里的任意字符串 —— 完全由攻击者控制。
//
// 它是最不该 panic 的函数之一：`Authorization: Bearer <垃圾>` 是最常见的
// 探测流量，一个 panic 就能把服务打成 500（有 Recovery 时）甚至打挂进程。
//
// 同时要守住一条安全性质：**除自己签发的令牌外，一律不得解析成功**。
// 这比"某个伪造令牌被拒"强得多，因为它覆盖了整片攻击面。
func FuzzParse(f *testing.F) {
	m := NewManager("fuzz-secret-that-is-long-enough-0123456789", "medicore-test", fuzzTTL)

	valid, err := m.IssueAccess(42, "doctor")
	if err != nil {
		f.Fatal(err)
	}

	f.Add(valid)
	f.Add("")
	f.Add("abc")
	f.Add("a.b.c")
	f.Add("Bearer " + valid) // 常见误用：连前缀一起传进来
	// alg=none 伪造（经典 JWT 攻击）
	f.Add("eyJhbGciOiJub25lIiwidHlwIjoiSldUIn0.eyJzdWIiOiIxIiwicm9sZSI6ImFkbWluIn0.")
	f.Add("eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.e30.")

	f.Fuzz(func(t *testing.T, token string) {
		claims, err := m.Parse(token)

		if err != nil {
			// 解析失败是预期路径；只要不是 panic 就算通过
			if claims != nil {
				t.Fatalf("返回错误却同时给出 claims：%+v", claims)
			}
			return
		}

		// 解析成功时：必须确实是本签发方签发的，且内容是可信的
		if claims.Issuer != "medicore-test" {
			t.Fatalf("接受了他方签发的令牌：issuer=%q", claims.Issuer)
		}
		if claims.UserID == 0 {
			t.Fatalf("解析成功但 UserID 为 0，后续鉴权会把它当成合法用户：%+v", claims)
		}
		if claims.Role == "" {
			t.Fatalf("解析成功但角色为空，RBAC 行为未定义：%+v", claims)
		}
	})
}

// 往返性质：任何 (userID, role) 都必须能签发再解回原值。
//
// 覆盖了 ID 边界（0、极大值）与角色字符串的各种形态。
func FuzzIssueParseRoundTrip(f *testing.F) {
	m := NewManager("fuzz-secret-that-is-long-enough-0123456789", "medicore-test", fuzzTTL)

	f.Add(uint64(1), "patient")
	f.Add(uint64(0), "doctor")
	f.Add(^uint64(0), "admin")
	f.Add(uint64(42), "")

	f.Fuzz(func(t *testing.T, userID uint64, role string) {
		// 角色在库里是枚举值，不可能是非法 UTF-8；
		// 而 JWT 的 payload 是 JSON，JSON 字符串**无法承载非法 UTF-8**
		// （编码时会被替换成 U+FFFD），所以"字节级往返一致"对非法输入本就不成立。
		//
		// 这是 fuzz 找出来的第一条：问题不在代码，而在断言提了一个 JSON 做不到的要求。
		// 能区分这两者，是 fuzz 结果解读里最关键的一步。
		if !utf8.ValidString(role) {
			t.Skip()
		}

		token, err := m.IssueAccess(userID, role)
		if err != nil {
			t.Skip()
		}

		claims, err := m.Parse(token)
		if err != nil {
			t.Fatalf("自己签发的令牌解析失败：%v", err)
		}
		if claims.UserID != userID {
			t.Fatalf("UserID 往返不一致：want %d, got %d", userID, claims.UserID)
		}
		if claims.Role != role {
			t.Fatalf("Role 往返不一致：want %q, got %q", role, claims.Role)
		}
	})
}
