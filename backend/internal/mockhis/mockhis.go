// Package mockhis 是模拟的 HIS（医院信息系统）数据源。
//
// spec §1.3 明确：不接真实 HIS，用内置数据源模拟外部系统，
// 以便聚焦于中台自身的同步逻辑（upsert、并发、失败统计）。
//
// 数据内嵌在二进制里而不是运行时读文件：与 prompts 同理，
// 避免"部署时漏拷数据目录"，也让演示环境开箱即用。
package mockhis

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"time"
)

//go:embed data/dataset.json
var raw []byte

// Department 是 HIS 侧科室记录。
type Department struct {
	ExternalID  string `json:"external_id"`
	Code        string `json:"code"`
	Name        string `json:"name"`
	Description string `json:"description"`
	SortOrder   int    `json:"sort_order"`
}

// Doctor 是 HIS 侧医生记录。
type Doctor struct {
	ExternalID     string `json:"external_id"`
	Name           string `json:"name"`
	Phone          string `json:"phone"`
	Title          string `json:"title"`
	Specialty      string `json:"specialty"`
	DepartmentCode string `json:"department_code"`
	ConsultFee     string `json:"consult_fee"`
	IsPrimary      bool   `json:"is_primary"`
}

// Schedule 是 HIS 侧排班记录。
//
// DateOffsetDays 用"相对今天的天数"而非绝对日期：
// 写死的日期过不了几天就全部变成历史数据，演示时什么也看不到。
type Schedule struct {
	ExternalID     string `json:"external_id"`
	DoctorExternal string `json:"doctor_external_id"`
	DepartmentCode string `json:"department_code"`
	DateOffsetDays int    `json:"date_offset_days"`
	TimeSlot       string `json:"time_slot"`
	Total          int    `json:"total"`
}

// Dataset 是一次同步拉取到的全部数据。
type Dataset struct {
	Departments []Department `json:"departments"`
	Doctors     []Doctor     `json:"doctors"`
	Schedules   []Schedule   `json:"schedules"`
}

// Load 读取内置数据集。
func Load() (*Dataset, error) {
	var ds Dataset
	if err := json.Unmarshal(raw, &ds); err != nil {
		return nil, fmt.Errorf("解析 mock HIS 数据: %w", err)
	}
	return &ds, nil
}

// ScheduleDate 返回排班的实际日期。
func (s Schedule) ScheduleDate(now time.Time) time.Time {
	return now.AddDate(0, 0, s.DateOffsetDays)
}

// Summary 返回数据量摘要，供日志与健康检查使用。
func (d *Dataset) Summary() string {
	return fmt.Sprintf("departments=%d doctors=%d schedules=%d",
		len(d.Departments), len(d.Doctors), len(d.Schedules))
}
