package model

import "time"

// 同步状态，与 sync_logs.status ENUM 对齐。
const (
	SyncStatusRunning = "running"
	SyncStatusSuccess = "success"
	SyncStatusPartial = "partial"
	SyncStatusFailed  = "failed"
)

// 同步触发方式。
const (
	SyncTriggerManual = "manual"
	SyncTriggerCron   = "cron"
)

// SyncLog 对应 sync_logs 表（spec §7.3 要求记录 total/succeeded/failed）。
type SyncLog struct {
	ID           uint64     `gorm:"column:id;primaryKey"`
	Source       string     `gorm:"column:source"`
	TriggerType  string     `gorm:"column:trigger_type"`
	Total        int        `gorm:"column:total"`
	Succeeded    int        `gorm:"column:succeeded"`
	Failed       int        `gorm:"column:failed"`
	Status       string     `gorm:"column:status"`
	ErrorMessage *string    `gorm:"column:error_msg"`
	TraceID      *string    `gorm:"column:trace_id"`
	StartedAt    time.Time  `gorm:"column:started_at"`
	FinishedAt   *time.Time `gorm:"column:finished_at"`
}

// TableName 指定表名。
func (SyncLog) TableName() string { return "sync_logs" }

// SyncLogView 是同步记录的对外表示。
type SyncLogView struct {
	ID           uint64 `gorm:"column:id" json:"id"`
	Source       string `gorm:"column:source" json:"source"`
	TriggerType  string `gorm:"column:trigger_type" json:"trigger_type"`
	Total        int    `gorm:"column:total" json:"total"`
	Succeeded    int    `gorm:"column:succeeded" json:"succeeded"`
	Failed       int    `gorm:"column:failed" json:"failed"`
	Status       string `gorm:"column:status" json:"status"`
	ErrorMessage string `gorm:"column:error_msg" json:"error_message,omitempty"`
	StartedAt    string `gorm:"column:started_at" json:"started_at"`
	FinishedAt   string `gorm:"column:finished_at" json:"finished_at,omitempty"`
}

// GetID 供游标分页取 ID。
func (s SyncLogView) GetID() uint64 { return s.ID }
