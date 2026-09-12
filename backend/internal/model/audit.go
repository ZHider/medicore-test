package model

import "time"

// AuditLog 对应 audit_logs 表。
//
// 注意本表刻意没有 deleted_at / updated_at：审计记录一旦写入就不可修改、不可删除，
// 也不随业务数据被软删除（spec §2.2 要求管理员可查，§9.2 要求 TraceID 贯穿）。
type AuditLog struct {
	ID         uint64    `gorm:"column:id;primaryKey"`
	TraceID    string    `gorm:"column:trace_id"`
	UserID     *uint64   `gorm:"column:user_id"`
	Role       *string   `gorm:"column:role"`
	Method     string    `gorm:"column:method"`
	Path       string    `gorm:"column:path"`
	Resource   *string   `gorm:"column:resource"`
	ResourceID *uint64   `gorm:"column:resource_id"`
	Action     *string   `gorm:"column:action"`
	StatusCode int       `gorm:"column:status_code"`
	LatencyMS  int       `gorm:"column:latency_ms"`
	ClientIP   *string   `gorm:"column:client_ip"`
	UserAgent  *string   `gorm:"column:user_agent"`
	Detail     *string   `gorm:"column:detail"`
	CreatedAt  time.Time `gorm:"column:created_at"`
}

// TableName 指定表名。
func (AuditLog) TableName() string { return "audit_logs" }

// AuditLogView 是审计日志的对外表示。
type AuditLogView struct {
	ID         uint64  `gorm:"column:id" json:"id"`
	TraceID    string  `gorm:"column:trace_id" json:"trace_id"`
	UserID     *uint64 `gorm:"column:user_id" json:"user_id,omitempty"`
	Role       string  `gorm:"column:role" json:"role,omitempty"`
	Method     string  `gorm:"column:method" json:"method"`
	Path       string  `gorm:"column:path" json:"path"`
	Resource   string  `gorm:"column:resource" json:"resource,omitempty"`
	ResourceID *uint64 `gorm:"column:resource_id" json:"resource_id,omitempty"`
	Action     string  `gorm:"column:action" json:"action,omitempty"`
	StatusCode int     `gorm:"column:status_code" json:"status_code"`
	LatencyMS  int     `gorm:"column:latency_ms" json:"latency_ms"`
	ClientIP   string  `gorm:"column:client_ip" json:"client_ip,omitempty"`
	CreatedAt  string  `gorm:"column:created_at" json:"created_at"`
}

// GetID 供游标分页取 ID。
func (a AuditLogView) GetID() uint64 { return a.ID }
