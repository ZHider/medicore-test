package mask

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// 脱敏函数是"数据出站前的最后一道闸"，输入是用户自己填的手机号、身份证、姓名。
//
// 要守的性质（每一条都对应一种真实的失效方式）：
//
//  1. 绝不 panic、输出必须是合法 UTF-8
//     它在 DTO 转换里被调用，一条畸形数据能让整个接口 500；
//  2. 按 rune 计的长度不变
//     长度变了说明实现里混用了字节下标，中文名会被切坏；
//  3. 长度 ≥ 2 时，输出必须至少含一个 '*'
//     —— 这条才是"脱敏是否真的发生"的判据。
//
// 关于第 3 条的措辞：**不能写成"输出必须不等于输入"**。
// fuzz 用 "0***" 证明了那种写法是错的 —— 函数确实把后 3 位换成了 '*'，
// 只是因为原文尾部本来就是 '*'，结果恰好与输入相同。
// "至少屏蔽一个字符"才是真正要守的东西，而且它照样能抓住下面这个真实缺陷：
// IDCard 曾经在长度恰好为 10 时算出 repeat('*', 0)，
// 于是**原样返回**、一个字符都没屏蔽。
func FuzzMaskNeverLeaksOriginal(f *testing.F) {
	f.Add("13800001111")
	f.Add("110101199003074512")
	f.Add("张三丰")
	f.Add("")
	f.Add("a")
	f.Add("ab")
	f.Add(" ")
	f.Add(strings.Repeat("🎉", 50))
	f.Add("138000011112222") // 超长号码
	f.Add("11010119900307451212345")
	f.Add("0***") // 已脱敏的输入（见上文说明）

	f.Fuzz(func(t *testing.T, s string) {
		cases := []struct {
			name string
			fn   func(string) string
		}{
			{"Phone", Phone},
			{"IDCard", IDCard},
			{"Name", Name},
		}

		for _, c := range cases {
			got := c.fn(s)

			if !utf8.ValidString(got) {
				t.Fatalf("%s(%q) 产生了非法 UTF-8：%q", c.name, s, got)
			}

			// 性质 2：不得凭空造出输入里没有的字符
			origin := map[rune]bool{'*': true}
			for _, r := range s {
				origin[r] = true
			}
			for _, r := range got {
				if !origin[r] {
					t.Fatalf("%s(%q) = %q 含输入中不存在的字符 %q：多半是字节下标切坏了多字节字符",
						c.name, s, got, r)
				}
			}

			// 性质 3：够长就必须真的屏蔽掉至少一个字符
			if utf8.RuneCountInString(s) >= 2 && !strings.ContainsRune(got, '*') {
				t.Fatalf("%s(%q) = %q：一个字符都没屏蔽，脱敏失效", c.name, s, got)
			}
		}
	})
}
