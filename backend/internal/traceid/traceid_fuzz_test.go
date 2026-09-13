package traceid

import (
	"strings"
	"testing"
)

// Sanitize 的输入是 HTTP 头（`X-Trace-Id`）—— 完全由客户端控制，
// 而它的输出会被写进 `audit_logs.trace_id CHAR(32) CHARACTER SET ascii NOT NULL`。
//
// 这里守护的正是本项目修过的一个真实缺陷：这个头没有过信任边界时，
// 一个超长值在 MySQL 严格模式下会让整条 INSERT 失败，而审计是**批量**写入的 ——
// 一个构造出来的请求就能带走整批 100 条记录（含其他用户的）。实测：
//
//	ERROR 1406 (22001): Data too long for column 'trace_id' at row 2
//	rows before = 132   →   rows after = 132   （合法的兄弟行也没进去）
//
// 与 FuzzTruncate 守的是同一个失败模式（那边是 User-Agent），所以同样用 fuzz：
// 哪些字节组合会落到"看起来像 32 位"或"超出 32 字节"，取决于输入内容。
func FuzzSanitize(f *testing.F) {
	f.Add("")
	f.Add("ee33b8045a4cfb7a6f0ba98062ab25c6")
	f.Add("EE33B8045A4CFB7A6F0BA98062AB25C6")
	f.Add("0123456789abcdef0123456789abcde")   // 31 位
	f.Add("0123456789abcdef0123456789abcdef0") // 33 位
	f.Add(strings.Repeat("a", 4096))
	f.Add("550e8400-e29b-41d4-a716-446655440000")
	f.Add("中文中文中文中文中文中文中文中文中文中文中文中文中文中文中文中")
	f.Add("\xf0\x28\x8c\x28")
	f.Add("0123456789abcdef0123456789abcde\n")
	f.Add("00000000000000000000000000000000")

	f.Fuzz(func(t *testing.T, s string) {
		got := Sanitize(s)

		// 性质一（最重要）：输出必须永远能写进目标列。
		// 长度错或含非 ascii 字节，都会让整批审计记录一起失败。
		if len(got) != TraceIDLen {
			t.Fatalf("输出长度 %d ≠ %d，写不进 CHAR(32)：输入 %q",
				len(got), TraceIDLen, s)
		}
		if !IsValid(got) {
			t.Fatalf("输出不是合法的 trace_id：%q（输入 %q）", got, s)
		}
		for i := 0; i < len(got); i++ {
			c := got[i]
			isLowerHex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')
			if !isLowerHex {
				t.Fatalf("输出含非小写十六进制字节 %q：%q（输入 %q）", c, got, s)
			}
		}

		// 性质二：输入本身合法时只能做大小写归一，不得换成别的值 ——
		// 换了就打断跨服务链路。
		if IsValid(s) && got != strings.ToLower(s) {
			t.Fatalf("合法输入被替换了链路：want %q, got %q", strings.ToLower(s), got)
		}

		// 性质三：**绝不能出现"截断"行为**。
		// 截断会留下输入的前缀，造出一个看起来合法的假追踪号。
		if !IsValid(s) && len(s) >= TraceIDLen && got == s[:TraceIDLen] {
			t.Fatalf("输出是输入的前缀，说明发生了截断而不是替换：输入 %q", s)
		}
	})
}

// IsValid 是信任边界的判据本身，它自己出错会让上面所有性质失去意义：
// 放行一个 33 字节的值，或者误杀一个合法的值。
func FuzzIsValid(f *testing.F) {
	f.Add("ee33b8045a4cfb7a6f0ba98062ab25c6")
	f.Add("")
	f.Add(strings.Repeat("f", 32))

	f.Fuzz(func(t *testing.T, s string) {
		if !IsValid(s) {
			return
		}

		// 被接受的值必须真的符合列的约束。
		if len(s) != TraceIDLen {
			t.Fatalf("IsValid 放行了长度为 %d 的值：%q", len(s), s)
		}
		for i := 0; i < len(s); i++ {
			if !isHexDigit(s[i]) {
				t.Fatalf("IsValid 放行了非十六进制字节 %q：%q", s[i], s)
			}
			if s[i] > 0x7f {
				t.Fatalf("IsValid 放行了非 ASCII 字节 %q：%q", s[i], s)
			}
		}
	})
}
