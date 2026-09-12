// Package mockhis 是模拟的 HIS（医院信息系统）数据源。
//
// spec §1.3 明确：不接真实 HIS，用内置数据源模拟外部系统，
// 以便聚焦于中台自身的同步逻辑（upsert、并发、失败统计）。
//
// 本包是 his 契约的一个**适配器**：把内嵌 JSON 的写法（相对天数）
// 翻译成契约要求的形状（绝对日期）。同步编排在 service/his_sync.go，
// 两者之间只通过 his.Dataset 通信。
//
// 数据内嵌在二进制里而不是运行时读文件：与 prompts 同理，
// 避免"部署时漏拷数据目录"，也让演示环境开箱即用。
package mockhis

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"time"

	"github.com/medicore/backend/internal/his"
)

//go:embed data/dataset.json
var raw []byte

// Source 是内置演示数据源。它实现了 service 声明的 SyncSource 接口。
//
// 用类型而不是裸函数，是为了能直接作为接口实现被装配进去，
// 省掉一层"把函数包成类型"的适配器。
type Source struct {
	// Now 可注入，便于测试固定"今天"。为零值时用 time.Now。
	Now func() time.Time
}

// Load 读取内置数据集，并把相对日期换算成绝对日期。
//
// ctx 是为将来的真实数据源准备的（HTTP 调用需要超时与取消）；
// 内置数据在内存里，无需取消，这里只做接口对齐。
func (s Source) Load(context.Context) (*his.Dataset, error) {
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	return load(raw, now())
}

// rawDataset 是 dataset.json 的原始结构。
//
// 与 his.Dataset 分开是刻意的：文件里排班用 date_offset_days（相对今天的天数），
// 而契约里是绝对日期。转换发生在本适配器内部，
// service 层因此完全不需要知道"演示数据用的是相对日期"这件事。
type rawDataset struct {
	Departments []his.Department `json:"departments"`
	Doctors     []his.Doctor     `json:"doctors"`
	Schedules   []rawSchedule    `json:"schedules"`
}

type rawSchedule struct {
	ExternalID     string `json:"external_id"`
	DoctorExternal string `json:"doctor_external_id"`
	DepartmentCode string `json:"department_code"`
	// DateOffsetDays 相对今天的天数。写死的绝对日期过不了几天就变成历史数据，
	// 演示时什么也看不到，所以数据文件用相对写法。
	DateOffsetDays int    `json:"date_offset_days"`
	TimeSlot       string `json:"time_slot"`
	Total          int    `json:"total"`
}

// load 解析数据并做日期换算。抽出来是为了能针对固定日期做单元测试。
func load(data []byte, now time.Time) (*his.Dataset, error) {
	var rawDS rawDataset
	if err := json.Unmarshal(data, &rawDS); err != nil {
		return nil, fmt.Errorf("解析 mock HIS 数据: %w", err)
	}

	ds := &his.Dataset{
		Departments: rawDS.Departments,
		Doctors:     rawDS.Doctors,
		Schedules:   make([]his.Schedule, 0, len(rawDS.Schedules)),
	}
	for _, s := range rawDS.Schedules {
		ds.Schedules = append(ds.Schedules, his.Schedule{
			ExternalID:     s.ExternalID,
			DoctorExternal: s.DoctorExternal,
			DepartmentCode: s.DepartmentCode,
			SchedDate:      now.AddDate(0, 0, s.DateOffsetDays),
			TimeSlot:       s.TimeSlot,
			Total:          s.Total,
		})
	}
	return ds, nil
}
