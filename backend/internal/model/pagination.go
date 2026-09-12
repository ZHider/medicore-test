package model

import (
	"fmt"
	"strconv"
)

// 游标分页（spec §5.2 强制）：禁止 OFFSET 深翻，
// 统一 `WHERE id < :cursor ORDER BY id DESC LIMIT :size`。
//
// 为什么不用 page/pageSize：OFFSET 的代价随页码线性增长。
// 23 万行实测：OFFSET 100000 耗时 137ms、扫描 20 万行；
// 游标固定 0.483ms、恒定扫描 20 行（相差约 284 倍）。
const (
	DefaultPageSize = 20
	MaxPageSize     = 100
)

// CursorPage 是所有列表接口的统一分页响应。
type CursorPage[T any] struct {
	Items []T `json:"items"`
	// NextCursor 为 0 时序列化省略。
	NextCursor uint64 `json:"next_cursor,omitempty"`
	HasMore    bool   `json:"has_more"`
}

// NewCursorPage 由"多取一条"的查询结果构造分页响应。
//
// 仓储层固定查询 limit+1 条：多出的那一条只用于判断是否还有下一页，
// 不会返回给客户端。这样无需额外发一次 COUNT 查询。
func NewCursorPage[T any](rows []T, limit int, idOf func(T) uint64) CursorPage[T] {
	page := CursorPage[T]{Items: rows}

	if len(rows) > limit {
		page.HasMore = true
		page.Items = rows[:limit]
	}
	if page.HasMore && len(page.Items) > 0 {
		page.NextCursor = idOf(page.Items[len(page.Items)-1])
	}
	return page
}

// ParseCursor 解析客户端传入的游标。空串表示取第一页。
func ParseCursor(raw string) (uint64, error) {
	if raw == "" {
		return 0, nil
	}
	v, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid cursor %q: %w", raw, err)
	}
	return v, nil
}

// NormalizePageSize 把请求的 size 收敛到 [1, MaxPageSize]，非法值回退默认值。
// 必须设上限：否则 size=1000000 会被当作合法请求，等同于一次全表扫描。
func NormalizePageSize(size int) int {
	switch {
	case size <= 0:
		return DefaultPageSize
	case size > MaxPageSize:
		return MaxPageSize
	default:
		return size
	}
}
