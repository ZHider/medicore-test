package service

import (
	"encoding/json"
	"strings"
	"testing"
)

// extractJSON 处理的是 **LLM 的输出** —— 本项目里最不可控的一段输入。
//
// 它的返回值直接决定后续走向：解析成功就按结构化结果入库，
// 失败就降级成原始对话。所以这里要守住两条：
//
//  1. 绝不 panic（worker 里 panic 会带走整个进程，不是 500 那么轻）
//  2. 声称成功时，返回的**必须**是合法 JSON
//
// 第 2 条是真正值钱的：extractJSON 说 true、调用方就直接把它写进
// ai_pre_reports.structured 列。若它偶尔返回一段非法内容，
// 故障会推迟到"某个前端读到这份报告时"才暴露，排查代价极高。
func FuzzExtractJSON(f *testing.F) {
	f.Add(`{"a":1}`)
	f.Add("```json\n{\"a\":1}\n```")
	f.Add("以下是总结：\n{\"a\":1}\n希望有帮助。")
	f.Add("{}")
	f.Add("")
	f.Add("no json here")
	f.Add(`{"a":`)
	f.Add(`}{`)
	f.Add("```json")
	f.Add("中文字符串 {\"急\":\"紧急\"} 中文")
	f.Add(`{"nested":{"deep":{"deeper":[1,2,3]}}}`)

	f.Fuzz(func(t *testing.T, raw string) {
		got, ok := extractJSON(raw)

		if !ok {
			// 失败路径允许返回空串；非空也不该 panic，统一在这里放行
			return
		}

		if !json.Valid([]byte(got)) {
			t.Fatalf("extractJSON 声称成功却返回非法 JSON：\n  输入: %q\n  输出: %q", raw, got)
		}
	})
}

// 剥围栏不允许 panic，且结果必须是原串的子串（只能"剥"，不能无中生有）。
func FuzzStripCodeFence(f *testing.F) {
	f.Add("```json\n{\"a\":1}\n```")
	f.Add("```\n{}\n```")
	f.Add("``````")
	f.Add("```")
	f.Add("")
	f.Add("no fence")

	f.Fuzz(func(t *testing.T, s string) {
		got, ok := stripCodeFence(s)
		if !ok {
			return
		}
		if len(got) > len(s) {
			t.Fatalf("剥围栏后反而变长了：输入 %d 字节，输出 %d 字节", len(s), len(got))
		}
		// 剥围栏只能去掉首尾，中间内容必须原样保留
		if got != "" && !strings.Contains(s, got) {
			t.Fatalf("剥围栏的结果不是原串的子串：%q 不在 %q 中", got, s)
		}
	})
}
