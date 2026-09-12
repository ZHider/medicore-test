package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/medicore/backend/internal/model"
)

// SummaryOutcomeRow 是要写入报告的结果。
type SummaryOutcomeRow struct {
	Status        string
	Structured    *string
	LLMModel      string
	PromptVersion string
	Fallback      bool
	Attempts      int
	ErrorMessage  *string
}

// AIReportRepo 是 ai_pre_reports 表的数据访问。
type AIReportRepo struct {
	db *gorm.DB
}

// NewAIReportRepo 构造 AIReportRepo。
func NewAIReportRepo(db *gorm.DB) *AIReportRepo {
	return &AIReportRepo{db: db}
}

// CreatePending 创建待处理的报告。
//
// 第二个返回值表示"本次是否真的新建"。若该预约已有报告（uk_appointment 冲突），
// 则返回已存在的那一份，使提交接口天然幂等——用户重复点击不会产生第二份报告，
// 也不会重复入队。
func (r *AIReportRepo) CreatePending(
	ctx context.Context, appointmentID, userID uint64, conversation, llmModel, promptVersion string,
) (*model.AIReport, bool, error) {
	report := &model.AIReport{
		AppointmentID: appointmentID,
		UserID:        userID,
		Status:        model.AIReportStatusPending,
		Conversation:  conversation,
		LLMModel:      llmModel,
		PromptVersion: promptVersion,
	}

	res := r.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(report)
	if res.Error != nil {
		return nil, false, fmt.Errorf("create ai report: %w", res.Error)
	}
	if res.RowsAffected > 0 {
		return report, true, nil
	}

	existing, err := r.FindByAppointment(ctx, appointmentID)
	if err != nil {
		return nil, false, err
	}
	return existing, false, nil
}

// FindByAppointment 按预约查询报告。一次预约只有一份报告。
func (r *AIReportRepo) FindByAppointment(ctx context.Context, appointmentID uint64) (*model.AIReport, error) {
	var report model.AIReport

	err := r.db.WithContext(ctx).Where("appointment_id = ?", appointmentID).First(&report).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("find ai report: %w", err)
	}
	return &report, nil
}

// FindByID 按主键查询报告。
func (r *AIReportRepo) FindByID(ctx context.Context, id uint64) (*model.AIReport, error) {
	var report model.AIReport

	err := r.db.WithContext(ctx).First(&report, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("find ai report: %w", err)
	}
	return &report, nil
}

// MarkProcessing 抢占报告并累加尝试次数。
//
// 用带状态条件的 UPDATE 作为幂等闸门：
//   - 只允许 pending / processing 进入处理中，已 done / failed 的不再重复处理；
//   - 允许 processing → processing，因为 XAUTOCLAIM 重投的正是
//     上次崩溃时停在 processing 的消息，拒绝它会让任务永远卡住。
func (r *AIReportRepo) MarkProcessing(ctx context.Context, id uint64) (bool, error) {
	now := time.Now()

	res := r.db.WithContext(ctx).
		Model(&model.AIReport{}).
		Where("id = ? AND status IN ?", id, []string{
			model.AIReportStatusPending,
			model.AIReportStatusProcessing,
		}).
		Updates(map[string]any{
			"status":        model.AIReportStatusProcessing,
			"started_at":    now,
			"attempt_count": gorm.Expr("attempt_count + 1"),
		})
	if res.Error != nil {
		return false, fmt.Errorf("mark ai report processing: %w", res.Error)
	}
	return res.RowsAffected > 0, nil
}

// SaveOutcome 写入报告的最终结果。
func (r *AIReportRepo) SaveOutcome(ctx context.Context, id uint64, o SummaryOutcomeRow) error {
	now := time.Now()

	updates := map[string]any{
		"status":          o.Status,
		"structured_json": o.Structured,
		"llm_model":       o.LLMModel,
		"prompt_version":  o.PromptVersion,
		"fallback":        o.Fallback,
		"attempt_count":   o.Attempts,
		"error_msg":       o.ErrorMessage,
		"finished_at":     now,
	}

	if err := r.db.WithContext(ctx).
		Model(&model.AIReport{}).
		Where("id = ?", id).
		Updates(updates).Error; err != nil {
		return fmt.Errorf("save ai report outcome: %w", err)
	}
	return nil
}
