package traceid

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// New 必须始终产出"能直接写进 audit_logs.trace_id CHAR(32) ascii"的值。
// 这是它与数据库列之间唯一的契约，不能靠调用方自觉。
func TestNew_IsWritableAsTraceIDColumn(t *testing.T) {
	for i := 0; i < 100; i++ {
		id := New()

		assert.Len(t, id, TraceIDLen, "trace_id 必须恰好 32 字符，否则 CHAR(32) 列写不进去")
		assert.True(t, IsValid(id), "New 的输出必须自洽：%q", id)
		assert.Equal(t, strings.ToLower(id), id, "规范形式是小写，ascii_bin 逐字节比较要求大小写唯一")
	}
}

func TestIsValid(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"小写完整", "ee33b8045a4cfb7a6f0ba98062ab25c6", true},
		{"大写完整", "EE33B8045A4CFB7A6F0BA98062AB25C6", true},
		{"数字和字母混合", "0123456789abcdef0123456789abcdef", true},
		{"空串", "", false},
		{"31 位少一个字符", "0123456789abcdef0123456789abcde", false},
		// 这条是最要紧的：多一个字符就写不进 CHAR(32)，会让整批审计失败
		{"33 位多一个字符", "0123456789abcdef0123456789abcdef0", false},
		{"含非十六进制字母", "0123456789abcdefg123456789abcdef", false},
		{"含连字符（UUID 形式）", "550e8400-e29b-41d4-a716-446655440000", false},
		{"含空格", " 123456789abcdef0123456789abcdef", false},
		{"非 ASCII（多字节）", "中文中文中文中文中文中文中文中文中文中文中文中文中文中文中文中", false},
		{"只有一个字节的非法 UTF-8", "\xf0", false},
		{"全零占位", "00000000000000000000000000000000", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsValid(tt.in))
		})
	}
}

// 合法的上游值必须沿用小写形式 —— 跨服务链路连续的前提。
func TestSanitize_KeepsValidUpstreamValue(t *testing.T) {
	const lower = "ee33b8045a4cfb7a6f0ba98062ab25c6"

	t.Run("小写原样沿用", func(t *testing.T) {
		assert.Equal(t, lower, Sanitize(lower))
	})

	// 列排序规则是 ascii_bin，大写与小写是两个不同的值。
	// 归一而不是拒绝，是为了不打断上游的链路（我们会在响应头里回显归一后的值）。
	t.Run("大写归一为小写", func(t *testing.T) {
		assert.Equal(t, lower, Sanitize(strings.ToUpper(lower)))
	})
}

// 不合法的值必须被**替换**而不是截断。
//
// 截断会造出一个"看起来合法"的假追踪号：用户拿到的与实际落库的不是同一个值，
// 而那正是排障时最误导人的情况。
func TestSanitize_ReplacesInvalidUpstreamValue(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{"空串（客户端没带这个头）", ""},
		{"31 位", "0123456789abcdef0123456789abcde"},
		{"33 位", "0123456789abcdef0123456789abcdef0"},
		{"超长（审计旁路的攻击载荷）", strings.Repeat("a", 4000)},
		{"UUID 形式", "550e8400-e29b-41d4-a716-446655440000"},
		{"含非十六进制字符", "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"},
		{"非 ASCII", "中文中文中文中文中文中文中文中文中文中文中文中文中文中文中文中"},
		{"非法 UTF-8 字节", "\xf0\x28\x8c\x28"},
		{"换行注入", "0123456789abcdef0123456789abcde\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Sanitize(tt.in)

			assert.True(t, IsValid(got), "替换结果必须合法：%q -> %q", tt.in, got)
			assert.Len(t, got, TraceIDLen)
			assert.NotEqual(t, tt.in, got, "不合法的输入不应被原样沿用")

			// 关键：绝不能把输入的一部分留下来。
			// 若实现改成"截断到 32"，33 位那条会恰好留下前 32 位 —— 那就是假追踪号。
			if len(tt.in) >= TraceIDLen {
				assert.NotEqual(t, tt.in[:TraceIDLen], got,
					"这是截断的特征：长输入被切短后当成了合法值")
			}
		})
	}
}

// 替换要产生不同的值：如果不是，冲掉追踪号就等于把多条链路混成一个。
func TestSanitize_ReplacementIsDistinct(t *testing.T) {
	seen := make(map[string]struct{}, 50)
	for i := 0; i < 50; i++ {
		got := Sanitize("not-a-trace-id")
		if _, dup := seen[got]; dup {
			t.Fatalf("替换值重复：%q —— 多个请求会被混成同一条链路", got)
		}
		seen[got] = struct{}{}
	}
}
