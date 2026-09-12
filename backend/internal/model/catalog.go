package model

import (
	"time"

	"gorm.io/gorm"
)

// 科室 / 医生状态。
const (
	StatusDisabled int8 = 0
	StatusActive   int8 = 1
)

// 排班状态，与 schedules.status ENUM 对齐。
const (
	ScheduleStatusOpen      = "open"
	ScheduleStatusClosed    = "closed"
	ScheduleStatusCancelled = "cancelled"
)

// 午别，与 schedules.time_slot ENUM 对齐。
const (
	TimeSlotAM = "am"
	TimeSlotPM = "pm"
)

// Department 对应 departments 表。
type Department struct {
	ID          uint64         `gorm:"column:id;primaryKey"`
	ExternalID  *string        `gorm:"column:external_id"`
	Code        string         `gorm:"column:code"`
	Name        string         `gorm:"column:name"`
	Description *string        `gorm:"column:description"`
	SortOrder   int            `gorm:"column:sort_order"`
	Status      int8           `gorm:"column:status"`
	DeletedAt   gorm.DeletedAt `gorm:"column:deleted_at;index"`
	CreatedAt   time.Time      `gorm:"column:created_at"`
	UpdatedAt   time.Time      `gorm:"column:updated_at"`
}

// TableName 指定表名。
func (Department) TableName() string { return "departments" }

// Doctor 对应 doctors 表。
//
// 注意：科室归属存放在 doctor_departments（多对多），本表不含 department_id。
// ConsultFee 用 string 承载 DECIMAL(10,2)：金额用浮点会产生舍入误差，
// 而 GORM 可以把 DECIMAL 直接扫进 string，JSON 也输出字符串，两端都不会丢精度。
type Doctor struct {
	ID         uint64         `gorm:"column:id;primaryKey"`
	UserID     uint64         `gorm:"column:user_id"`
	ExternalID *string        `gorm:"column:external_id"`
	Title      string         `gorm:"column:title"`
	Specialty  *string        `gorm:"column:specialty"`
	Intro      *string        `gorm:"column:intro"`
	ConsultFee string         `gorm:"column:consult_fee"`
	Status     int8           `gorm:"column:status"`
	DeletedAt  gorm.DeletedAt `gorm:"column:deleted_at;index"`
	CreatedAt  time.Time      `gorm:"column:created_at"`
	UpdatedAt  time.Time      `gorm:"column:updated_at"`
}

// TableName 指定表名。
func (Doctor) TableName() string { return "doctors" }

// DoctorDepartment 对应 doctor_departments 表（医生与科室多对多）。
type DoctorDepartment struct {
	ID           uint64         `gorm:"column:id;primaryKey"`
	DoctorID     uint64         `gorm:"column:doctor_id"`
	DepartmentID uint64         `gorm:"column:department_id"`
	IsPrimary    bool           `gorm:"column:is_primary"`
	SortOrder    int            `gorm:"column:sort_order"`
	DeletedAt    gorm.DeletedAt `gorm:"column:deleted_at;index"`
	CreatedAt    time.Time      `gorm:"column:created_at"`
	UpdatedAt    time.Time      `gorm:"column:updated_at"`
}

// TableName 指定表名。
func (DoctorDepartment) TableName() string { return "doctor_departments" }

// Schedule 对应 schedules 表（号源载体）。
type Schedule struct {
	ID           uint64         `gorm:"column:id;primaryKey"`
	DoctorID     uint64         `gorm:"column:doctor_id"`
	DepartmentID uint64         `gorm:"column:department_id"`
	ExternalID   *string        `gorm:"column:external_id"`
	SchedDate    time.Time      `gorm:"column:sched_date"`
	TimeSlot     string         `gorm:"column:time_slot"`
	Total        int            `gorm:"column:total"`
	Remaining    int            `gorm:"column:remaining"`
	Version      int            `gorm:"column:version"`
	Status       string         `gorm:"column:status"`
	DeletedAt    gorm.DeletedAt `gorm:"column:deleted_at;index"`
	CreatedAt    time.Time      `gorm:"column:created_at"`
	UpdatedAt    time.Time      `gorm:"column:updated_at"`
}

// TableName 指定表名。
func (Schedule) TableName() string { return "schedules" }

// ---------------------------------------------------------------------------
// 只读查询模型
//
// 这些结构同时带 gorm 与 json tag，直接作为 JOIN 查询的扫描目标，
// 省掉一层"查询结果 -> DTO"的机械映射。它们只用于读取，不含写入路径。
// ---------------------------------------------------------------------------

// DepartmentBrief 是科室的对外表示。
type DepartmentBrief struct {
	ID          uint64 `gorm:"column:id" json:"id"`
	Code        string `gorm:"column:code" json:"code"`
	Name        string `gorm:"column:name" json:"name"`
	Description string `gorm:"column:description" json:"description,omitempty"`
	SortOrder   int    `gorm:"column:sort_order" json:"-"`
}

// DoctorListItem 是"科室下的医生"列表项，由 doctors JOIN users JOIN doctor_departments 得到。
type DoctorListItem struct {
	ID           uint64 `gorm:"column:id" json:"id"`
	Name         string `gorm:"column:name" json:"name"`
	Title        string `gorm:"column:title" json:"title"`
	Specialty    string `gorm:"column:specialty" json:"specialty,omitempty"`
	ConsultFee   string `gorm:"column:consult_fee" json:"consult_fee"`
	DepartmentID uint64 `gorm:"column:department_id" json:"department_id"`
	IsPrimary    bool   `gorm:"column:is_primary" json:"is_primary"`
}

// DoctorDetail 是医生详情，多一个所属科室列表（多对多）。
type DoctorDetail struct {
	ID          uint64            `gorm:"column:id" json:"id"`
	Name        string            `gorm:"column:name" json:"name"`
	Title       string            `gorm:"column:title" json:"title"`
	Specialty   string            `gorm:"column:specialty" json:"specialty,omitempty"`
	Intro       string            `gorm:"column:intro" json:"intro,omitempty"`
	ConsultFee  string            `gorm:"column:consult_fee" json:"consult_fee"`
	Departments []DepartmentBrief `gorm:"-" json:"departments"`
}

// ScheduleListItem 是排班列表项。
type ScheduleListItem struct {
	ID           uint64 `gorm:"column:id" json:"id"`
	DoctorID     uint64 `gorm:"column:doctor_id" json:"doctor_id"`
	DepartmentID uint64 `gorm:"column:department_id" json:"department_id"`
	SchedDate    string `gorm:"column:sched_date" json:"sched_date"`
	TimeSlot     string `gorm:"column:time_slot" json:"time_slot"`
	Total        int    `gorm:"column:total" json:"total"`
	Remaining    int    `gorm:"column:remaining" json:"remaining"`
	Status       string `gorm:"column:status" json:"status"`
}

// GetID 供游标分页取 ID。
func (s ScheduleListItem) GetID() uint64 { return s.ID }
