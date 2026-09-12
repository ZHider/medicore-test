package repository

import (
	"testing"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ParseReportID 决定一条队列消息是"可处理"还是"损坏"。
//
// 这个判断的代价是不对称的：解析成功就照常总结；解析失败会被 worker
// **确认并丢弃**（重投也不会变好）。因此返回错一个 ID，后果是
// "给 A 的预约生成了 B 的总结"，而不是一个显眼的报错。
func TestParseReportID(t *testing.T) {
	tests := []struct {
		name    string
		values  map[string]any
		want    uint64
		wantErr bool
	}{
		{"字符串形式（XADD 的默认写法）", map[string]any{reportIDField: "42"}, 42, false},
		{"int64（某些客户端反序列化后的形态）", map[string]any{reportIDField: int64(7)}, 7, false},
		{"uint64", map[string]any{reportIDField: uint64(9)}, 9, false},
		{"零值也是合法 ID", map[string]any{reportIDField: "0"}, 0, false},

		{"缺字段", map[string]any{}, 0, true},
		{"字段名写错", map[string]any{"reportId": "42"}, 0, true},
		{"不是数字", map[string]any{reportIDField: "abc"}, 0, true},
		{"负数", map[string]any{reportIDField: "-1"}, 0, true},
		{"空串", map[string]any{reportIDField: ""}, 0, true},
		{"小数", map[string]any{reportIDField: "1.5"}, 0, true},
		{"类型不支持", map[string]any{reportIDField: []byte("42")}, 0, true},
		{"nil", map[string]any{reportIDField: nil}, 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseReportID(redis.XMessage{ID: "1-1", Values: tt.values})

			if tt.wantErr {
				assert.Error(t, err, "损坏的消息必须报错，worker 才能转入丢弃分支")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// 错误信息里要带消息 ID，否则线上排查时无法定位是哪条消息坏了。
func TestParseReportID_ErrorMentionsMessageID(t *testing.T) {
	_, err := ParseReportID(redis.XMessage{
		ID:     "1700000000000-3",
		Values: map[string]any{reportIDField: "not-a-number"},
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "1700000000000-3",
		"损坏消息的错误必须能定位到具体消息")
}

// 只有真正的 NOGROUP 才算"消费者组消失"。
//
// 误判的代价是双向的：漏判 → 循环永远报同一个错、任务静默卡死；
// 误判 → 给一个无关错误反复重建消费者组。
func TestIsNoGroupError(t *testing.T) {
	assert.True(t, IsNoGroupError(redisErr("NOGROUP No such key")))

	assert.False(t, IsNoGroupError(nil))
	assert.False(t, IsNoGroupError(redis.Nil))
	// 普通 error 即使文案里恰好出现 NOGROUP 也不算：只认 redis.Error 类型
	assert.False(t, IsNoGroupError(errString("NOGROUP 只是文案里恰好出现")))
}

// redisErr 是 redis.Error 的测试实现。
//
// 注意 RedisError() 是**无返回值**的标记方法（不是返回 error）：
// go-redis 靠它区分"Redis 错误"与普通 error。签名写错的话类型断言会失败，
// IsNoGroupError 会静默返回 false —— 在这里表现为测试变红，
// 但在生产代码里这类错误只会表现为"恢复逻辑不再触发"。
type redisErr string

func (e redisErr) Error() string { return string(e) }
func (e redisErr) RedisError()   {}

type errString string

func (e errString) Error() string { return string(e) }
