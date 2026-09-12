package mockhis

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 相对日期必须在适配器里换算成绝对日期。
//
// 这是 mockhis 唯一的实质逻辑：数据文件的日期写法（相对今天 N 天）
// 是为了让演示数据永不过期，而契约（his.Schedule）要求绝对日期。
// 换算放错位置（比如留给 service 做）会把"演示数据的写法"泄漏进业务逻辑。
func TestLoad_ConvertsDateOffsetToAbsoluteDate(t *testing.T) {
	now := time.Date(2026, 9, 12, 15, 30, 0, 0, time.Local)

	ds, err := load([]byte(`{
		"schedules": [
			{"external_id": "S1", "doctor_external_id": "D1", "department_code": "CARD",
			 "date_offset_days": 0, "time_slot": "am", "total": 10},
			{"external_id": "S2", "doctor_external_id": "D1", "department_code": "CARD",
			 "date_offset_days": 3, "time_slot": "pm", "total": 5},
			{"external_id": "S3", "doctor_external_id": "D1", "department_code": "CARD",
			 "date_offset_days": -2, "time_slot": "am", "total": 8}
		]
	}`), now)
	require.NoError(t, err)
	require.Len(t, ds.Schedules, 3)

	assert.Equal(t, time.Date(2026, 9, 12, 15, 30, 0, 0, time.Local), ds.Schedules[0].SchedDate)
	assert.Equal(t, time.Date(2026, 9, 15, 15, 30, 0, 0, time.Local), ds.Schedules[1].SchedDate)
	// 负数偏移（过去的排班）同样要支持：演示"已过期的号"时需要它
	assert.Equal(t, time.Date(2026, 9, 10, 15, 30, 0, 0, time.Local), ds.Schedules[2].SchedDate)

	// 其余字段原样透传，不做任何加工
	assert.Equal(t, "S2", ds.Schedules[1].ExternalID)
	assert.Equal(t, 5, ds.Schedules[1].Total)
	assert.Equal(t, "pm", ds.Schedules[1].TimeSlot)
}

// 内嵌的演示数据必须能被解析，且换算后的日期落在今天之后——
// 否则演示时挂不到号，症状是"看起来一切正常但列表是空的"。
func TestEmbeddedDataset_IsLoadableAndInTheFuture(t *testing.T) {
	now := time.Now()
	ds, err := load(raw, now)
	require.NoError(t, err)

	require.NotEmpty(t, ds.Departments, "演示数据至少要有科室")
	require.NotEmpty(t, ds.Doctors, "演示数据至少要有医生")
	require.NotEmpty(t, ds.Schedules, "演示数据至少要有排班")

	for _, s := range ds.Schedules {
		assert.False(t, s.SchedDate.IsZero(), "排班 %s 的日期不应为零值", s.ExternalID)
		assert.False(t, s.SchedDate.Before(now.Truncate(24*time.Hour).AddDate(0, 0, -1)),
			"排班 %s 的日期过于久远，演示时会看不到", s.ExternalID)
	}
}

// Source 必须满足 service 声明的数据源契约（Load(ctx) (*his.Dataset, error)）。
// 这里用编译期断言把"签名被改坏"变成构建失败，而不是等到装配时才炸。
func TestSource_LoadIsUsable(t *testing.T) {
	src := Source{Now: func() time.Time { return time.Date(2026, 9, 12, 0, 0, 0, 0, time.Local) }}

	ds, err := src.Load(t.Context())
	require.NoError(t, err)
	require.NotEmpty(t, ds.Schedules)
}
