package model

import (
	"encoding/json"
	"time"

	"gorm.io/gorm"
)

// AI 报告状态，与 ai_pre_reports.status ENUM 对齐。
const (
	AIReportStatusPending    = "pending"
	AIReportStatusProcessing = "processing"
	AIReportStatusDone       = "done"
	AIReportStatusFailed     = "failed"
)

// AIReport 对应 ai_pre_reports 表。
//
// JSON 列用 *string / string 承载而非引入 datatypes 依赖：
// MySQL 会自行校验内容是否为合法 JSON，GORM 也能正常写入字符串。
type AIReport struct {
	ID            uint64         `gorm:"column:id;primaryKey"`
	AppointmentID uint64         `gorm:"column:appointment_id"`
	UserID        uint64         `gorm:"column:user_id"`
	Status        string         `gorm:"column:status"`
	StructuredRaw *string        `gorm:"column:structured_json"`
	Conversation  string         `gorm:"column:conversation"`
	LLMModel      string         `gorm:"column:llm_model"`
	PromptVersion string         `gorm:"column:prompt_version"`
	Fallback      bool           `gorm:"column:fallback"`
	AttemptCount  int            `gorm:"column:attempt_count"`
	ErrorMessage  *string        `gorm:"column:error_msg"`
	StartedAt     *time.Time     `gorm:"column:started_at"`
	FinishedAt    *time.Time     `gorm:"column:finished_at"`
	DeletedAt     gorm.DeletedAt `gorm:"column:deleted_at;index"`
	CreatedAt     time.Time      `gorm:"column:created_at"`
	UpdatedAt     time.Time      `gorm:"column:updated_at"`
}

// TableName 指定表名。
func (AIReport) TableName() string { return "ai_pre_reports" }

// IsTerminal 判断报告是否已进入终态。
func (r *AIReport) IsTerminal() bool {
	return r.Status == AIReportStatusDone || r.Status == AIReportStatusFailed
}

// AIReportView 是返回给客户端的报告视图。
type AIReportView struct {
	ID            uint64          `json:"id"`
	AppointmentID uint64          `json:"appointment_id"`
	Status        string          `json:"status"`
	Fallback      bool            `json:"fallback"`
	Structured    json.RawMessage `json:"structured,omitempty"`
	Conversation  json.RawMessage `json:"conversation"`
	LLMModel      string          `json:"llm_model"`
	PromptVersion string          `json:"prompt_version"`
	ErrorMessage  string          `json:"error_message,omitempty"`
	CreatedAt     string          `json:"created_at"`
	FinishedAt    string          `json:"finished_at,omitempty"`
}

// SubmitResult 是提交总结后的即时响应。
type SubmitResult struct {
	ReportID uint64 `json:"report_id"`
	Status   string `json:"status"`
	// AlreadySubmitted 为 true 表示此前已提交过，本次未重复入队。
	AlreadySubmitted bool `json:"already_submitted"`
}
