package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type pageItem struct {
	ID uint64
}

func idOf(item pageItem) uint64 { return item.ID }

func TestParseCursor(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    uint64
		wantErr bool
	}{
		{"空串表示第一页", "", 0, false},
		{"正常游标", "12345", 12345, false},
		{"零", "0", 0, false},
		{"非数字", "abc", 0, true},
		{"负数", "-1", 0, true},
		{"超出 uint64", "99999999999999999999999", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseCursor(tt.raw)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// size 必须有上限：否则 size=1000000 会被当作合法请求，等价于一次全表扫描。
func TestNormalizePageSize(t *testing.T) {
	tests := []struct {
		name string
		in   int
		want int
	}{
		{"零回退默认", 0, DefaultPageSize},
		{"负数回退默认", -5, DefaultPageSize},
		{"正常值保留", 50, 50},
		{"恰好为上限", MaxPageSize, MaxPageSize},
		{"超限被截断", 1_000_000, MaxPageSize},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, NormalizePageSize(tt.in))
		})
	}
}

func TestNewCursorPage_NoMoreData(t *testing.T) {
	// 恰好等于 limit，说明没有下一页
	rows := []pageItem{{1}, {2}, {3}}

	page := NewCursorPage(rows, 3, idOf)

	assert.Len(t, page.Items, 3)
	assert.False(t, page.HasMore)
	assert.Zero(t, page.NextCursor, "无下一页时不应给出游标")
}

func TestNewCursorPage_HasMoreAndTrimsExtraRow(t *testing.T) {
	// 仓储层多取一条：limit=3 却返回 4 条，第 4 条只用于判断存在下一页
	rows := []pageItem{{10}, {9}, {8}, {7}}

	page := NewCursorPage(rows, 3, idOf)

	assert.Len(t, page.Items, 3, "多出的探测行不能返回给客户端")
	assert.True(t, page.HasMore)
	assert.Equal(t, uint64(8), page.NextCursor, "游标应指向本页最后一条")
}

func TestNewCursorPage_Empty(t *testing.T) {
	page := NewCursorPage([]pageItem{}, 20, idOf)

	assert.Empty(t, page.Items)
	assert.False(t, page.HasMore)
	assert.Zero(t, page.NextCursor)
}

// 游标必须单调递减，否则客户端翻页会出现重复或漏项。
func TestNewCursorPage_CursorIsLastItemID(t *testing.T) {
	rows := []pageItem{{100}, {99}, {98}, {97}}

	page := NewCursorPage(rows, 2, idOf)

	assert.Equal(t, uint64(99), page.NextCursor)
	assert.True(t, page.NextCursor < rows[0].ID)
}
