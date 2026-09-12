package model

import (
	"time"

	"gorm.io/gorm"
)

// 预约状态，与 appointments.status ENUM 对齐。
const (
	AppointmentStatusPending   = "pending"
	AppointmentStatusConfirmed = "confirmed"
	AppointmentStatusCancelled = "cancelled"
	AppointmentStatusExpired   = "expired"
)

// DefaultConfirmWindow 是抢号后的确认时限（spec §7.1）。
const DefaultConfirmWindow = 15 * time.Minute

// Appointment 对应 appointments 表。
//
// 注意：表中还有生成列 active_uk（进行中预约的唯一键），
// 它由 MySQL 自行维护，因此不在本结构体中——出现反而会干扰 INSERT。
type Appointment struct {
	ID             uint64         `gorm:"column:id;primaryKey"`
	UserID         uint64         `gorm:"column:user_id"`
	ScheduleID     uint64         `gorm:"column:schedule_id"`
	DoctorID       uint64         `gorm:"column:doctor_id"`
	DepartmentID   uint64         `gorm:"column:department_id"`
	SchedDate      time.Time      `gorm:"column:sched_date"`
	TimeSlot       string         `gorm:"column:time_slot"`
	QueueNo        *int           `gorm:"column:queue_no"`
	CheckedInAt    *time.Time     `gorm:"column:checked_in_at"`
	Status         string         `gorm:"column:status"`
	IdempotencyKey string         `gorm:"column:idempotency_key"`
	ExpireAt       time.Time      `gorm:"column:expire_at"`
	ConfirmedAt    *time.Time     `gorm:"column:confirmed_at"`
	CancelledAt    *time.Time     `gorm:"column:cancelled_at"`
	CancelReason   *string        `gorm:"column:cancel_reason"`
	DeletedAt      gorm.DeletedAt `gorm:"column:deleted_at;index"`
	CreatedAt      time.Time      `gorm:"column:created_at"`
	UpdatedAt      time.Time      `gorm:"column:updated_at"`
}

// TableName 指定表名。
func (Appointment) TableName() string { return "appointments" }

// IsActive 判断预约是否仍占用号源。
func (a *Appointment) IsActive() bool {
	return a.Status == AppointmentStatusPending || a.Status == AppointmentStatusConfirmed
}

// GrabRequest 是抢号请求。
// IdempotencyKey 允许客户端自带（spec §8.2：前端 axios 注入 uuid）；
// 未提供时由服务端生成，仍可防住"同一次提交被重放"。
type GrabRequest struct {
	ScheduleID     uint64 `json:"schedule_id" binding:"required"`
	IdempotencyKey string `json:"idempotency_key" binding:"omitempty,min=8,max=64"`
}

// CancelRequest 是取消预约请求。
type CancelRequest struct {
	Reason string `json:"reason" binding:"omitempty,max=200"`
}

// AppointmentItem 是预约列表项，附带医生与科室名称（避免前端二次查询）。
type AppointmentItem struct {
	ID             uint64 `gorm:"column:id" json:"id"`
	ScheduleID     uint64 `gorm:"column:schedule_id" json:"schedule_id"`
	DoctorID       uint64 `gorm:"column:doctor_id" json:"doctor_id"`
	DoctorName     string `gorm:"column:doctor_name" json:"doctor_name"`
	DepartmentID   uint64 `gorm:"column:department_id" json:"department_id"`
	DepartmentName string `gorm:"column:department_name" json:"department_name"`
	SchedDate      string `gorm:"column:sched_date" json:"sched_date"`
	TimeSlot       string `gorm:"column:time_slot" json:"time_slot"`
	QueueNo        *int   `gorm:"column:queue_no" json:"queue_no,omitempty"`
	Status         string `gorm:"column:status" json:"status"`
	ExpireAt       string `gorm:"column:expire_at" json:"expire_at,omitempty"`
	CreatedAt      string `gorm:"column:created_at" json:"created_at"`
}

// GetID 供游标分页取 ID。
func (a AppointmentItem) GetID() uint64 { return a.ID }

// DoctorAppointmentItem 是医生视角的预约列表项。
//
// 与患者视角的 AppointmentItem 形态不同：患者关心"我约了哪位医生"，
// 医生关心"这位患者是谁、病历写了没有"。因此不复用同一个结构——
// 硬塞在一起会让两个端点都返回一半无用的字段。
type DoctorAppointmentItem struct {
	ID           uint64 `gorm:"column:id" json:"id"`
	ScheduleID   uint64 `gorm:"column:schedule_id" json:"schedule_id"`
	PatientID    uint64 `gorm:"column:user_id" json:"patient_id"`
	PatientName  string `gorm:"column:patient_name" json:"patient_name"`
	DepartmentID uint64 `gorm:"column:department_id" json:"department_id"`
	SchedDate    string `gorm:"column:sched_date" json:"sched_date"`
	TimeSlot     string `gorm:"column:time_slot" json:"time_slot"`
	QueueNo      *int   `gorm:"column:queue_no" json:"queue_no,omitempty"`
	Status       string `gorm:"column:status" json:"status"`
	// HasRecord 标记是否已写病历：医生最需要"还有谁没处理"一眼可见，
	// 不必为每个预约单独再查一次病历。
	HasRecord bool   `gorm:"column:has_record" json:"has_record"`
	CreatedAt string `gorm:"column:created_at" json:"created_at"`
}

// GetID 供游标分页取 ID。
func (a DoctorAppointmentItem) GetID() uint64 { return a.ID }

// DoctorAppointmentFilter 是医生预约列表的筛选条件，空值表示不限制该维度。
type DoctorAppointmentFilter struct {
	// Date 按就诊日筛选，格式 YYYY-MM-DD。
	Date string
	// Status 按预约状态筛选。
	Status string
}

// QueueItem 是候诊队列项（医生端）。
type QueueItem struct {
	ID        uint64 `gorm:"column:id" json:"appointment_id"`
	QueueNo   int    `gorm:"column:queue_no" json:"queue_no"`
	PatientID uint64 `gorm:"column:user_id" json:"patient_id"`
	// PatientName 是患者姓名原文，**刻意不脱敏**：
	// 医生必须能识别面前的患者，打成"张*"会让队列失去意义。
	// 该接口仅对 doctor/admin 开放，是 spec §2.2 脱敏规则的有意例外。
	PatientName string `gorm:"column:patient_name" json:"patient_name"`
	CheckedInAt string `gorm:"column:checked_in_at" json:"checked_in_at"`
	Status      string `gorm:"column:status" json:"status"`
}
