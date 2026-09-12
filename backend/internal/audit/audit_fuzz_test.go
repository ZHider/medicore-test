package audit

import (
	"testing"
	"unicode/utf8"
)

// truncate 的输入是 User-Agent —— 完全由客户端控制。
//
// 这里守护的正是本项目修过的一个真实缺陷：按**字节**硬切会切断多字节字符，
// 切出非法 UTF-8；而目标列是 utf8mb4，MySQL 严格模式下会拒绝，
// 结果是**一条畸形 UA 让整批 100 条审计记录一起插入失败**。
//
// fuzz 而不是表驱动：哪些字节位置会落在字符中间，取决于输入内容，
// 手工列举几个中文例子只能覆盖到想得到的情况。
func FuzzTruncate(f *testing.F) {
	f.Add("Mozilla/5.0 (Windows NT 10.0; Win64; x64)", 255)
	f.Add("中", 1)
	f.Add("中文", 4)
	f.Add("🎉🎉🎉", 5) // 4 字节字符
	f.Add("", 0)
	f.Add("abc", 0)
	f.Add("abc", -1)
	f.Add("混合mélange中文🎉", 7)

	f.Fuzz(func(t *testing.T, s string, max int) {
		got := truncate(s, max)

		if max > 0 && len(got) > max {
			t.Fatalf("截断后长度 %d 超过上限 %d", len(got), max)
		}

		// 关键性质一：结果必须是合法 UTF-8
		if !utf8.ValidString(got) {
			t.Fatalf("截断切断了多字节字符，产生非法 UTF-8：\n  输入: %q (len=%d, max=%d)\n  输出: % x",
				s, len(s), max, got)
		}

		// 关键性质二：只会变短，不会变长
		if len(got) > len(s) {
			t.Fatalf("输出比输入还长：%d -> %d", len(s), len(got))
		}

		// 关键性质三：输入本来就合法且不超长时，不得"顺手"改动内容。
		// 注意要连 utf8.ValidString(s) 一起判断 —— 输入是非法 UTF-8 时，
		// 净化本身就是期望行为（这正是 FuzzTruncate 第一轮找出的那个洞）。
		if utf8.ValidString(s) && len(s) <= max && got != s {
			t.Fatalf("未超长且输入合法，却改动了内容：want %q, got %q", s, got)
		}
	})
}

// 空串转 nil 必须稳定：非空永远给指针，空串永远给 nil。
//
// 这条影响的是"数据库里到底存 NULL 还是空串"，两者在索引与查询上行为不同。
func FuzzOptionalString(f *testing.F) {
	f.Add("")
	f.Add("x")
	f.Add(" ")
	f.Add("  中文  ")

	f.Fuzz(func(t *testing.T, s string) {
		got := optionalString(s)

		if s == "" && got != nil {
			t.Fatalf("空串应转成 nil，实际得到 %q", *got)
		}
		if s != "" {
			if got == nil {
				t.Fatalf("非空输入 %q 不应转成 nil", s)
			}
			if *got != s {
				t.Fatalf("转换改动了内容：want %q, got %q", s, *got)
			}
		}
	})
}
