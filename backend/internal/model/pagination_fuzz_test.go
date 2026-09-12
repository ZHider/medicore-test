package model

import (
	"strconv"
	"testing"
)

// 游标来自查询参数，是攻击者可直接控制的值。
//
// 这里的性质比"不 panic"更具体：**游标必须单调可用于"取更旧的数据"**。
// 若游标被解析成一个巨大的值，列表会从表头开始返回 —— 对患者来说
// 就是"翻页翻出了别人的数据序列"，而对管理员接口则是把全表暴露出来。
func FuzzParseCursor(f *testing.F) {
	f.Add("")
	f.Add("0")
	f.Add("1")
	f.Add("18446744073709551615") // uint64 上界
	f.Add("18446744073709551616") // 溢出
	f.Add("-1")
	f.Add("1e10")
	f.Add(" 42 ")
	f.Add("0x10")
	f.Add("١٢٣") // 阿拉伯-印度数字，strconv 不应接受

	f.Fuzz(func(t *testing.T, raw string) {
		cursor, err := ParseCursor(raw)
		if err != nil {
			return // 绝大多数畸形输入走这里
		}

		// 解析成功时必须是"合法的游标语义"：
		//   空串  → 0（第一页）
		//   其余  → 能稳定重放的值
		if raw == "" {
			if cursor != 0 {
				t.Fatalf("空游标应为 0（第一页），实际 %d", cursor)
			}
			return
		}

		// 游标 0 本身是合法的（等价于"不传游标"= 第一页），不必拒绝。
		// 真正要守的是**稳定性**：解析出来的值再喂回去必须得到同一个值。
		// 否则分页会在某些游标格式下漂移 —— 表现为翻页时重复或跳过记录，
		// 这类问题在人工测试里几乎不可能发现。
		again, againErr := ParseCursor(strconv.FormatUint(cursor, 10))
		if againErr != nil {
			t.Fatalf("游标解析不稳定：%q -> %d，但再解析报错 %v", raw, cursor, againErr)
		}
		if again != cursor {
			t.Fatalf("游标解析不稳定：%q -> %d -> %d", raw, cursor, again)
		}
	})
}

// page size 来自查询参数，必须被夹在合法区间内。
//
// 若上限失守，一次请求就能把整张表拉进内存 —— 这是最容易被利用的
// 资源耗尽入口，而且不需要任何权限。
func FuzzNormalizePageSize(f *testing.F) {
	f.Add(0)
	f.Add(-1)
	f.Add(1)
	f.Add(DefaultPageSize)
	f.Add(MaxPageSize)
	f.Add(MaxPageSize + 1)
	f.Add(int(^uint(0) >> 1)) // int 上界
	f.Add(-int(^uint(0) >> 1))

	f.Fuzz(func(t *testing.T, size int) {
		got := NormalizePageSize(size)

		if got <= 0 {
			t.Fatalf("NormalizePageSize(%d) = %d，必须为正数，否则 SQL 的 LIMIT 行为未定义", size, got)
		}
		if got > MaxPageSize {
			t.Fatalf("NormalizePageSize(%d) = %d，超过上限 %d：一次请求就能拉爆内存", size, got, MaxPageSize)
		}
	})
}
