package repository

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/medicore/backend/internal/model"
)

// SyncDoctorInput 是 upsert 医生所需的全部字段。
//
// 手机号密文与摘要由 service 层算好再传进来：
// 加密属于业务策略，不该由数据访问层决定用哪个密钥。
type SyncDoctorInput struct {
	ExternalID   string
	Name         string
	PhoneEnc     []byte
	PhoneHash    string
	PasswordHash string
	Title        string
	Specialty    string
	ConsultFee   string
}

// SyncRepo 是 HIS 同步所需的写入操作。
type SyncRepo struct {
	db *gorm.DB
}

// NewSyncRepo 构造 SyncRepo。
func NewSyncRepo(db *gorm.DB) *SyncRepo {
	return &SyncRepo{db: db}
}

// UpsertDepartment 按 external_id upsert 科室。
//
// 用 external_id 而非 code 作为锚点：HIS 侧改编码时不应导致本地重复建科室。
func (r *SyncRepo) UpsertDepartment(ctx context.Context, dept model.Department) error {
	err := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "external_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"code", "name", "description", "sort_order", "updated_at",
		}),
	}).Create(&dept).Error
	if err != nil {
		return fmt.Errorf("upsert department: %w", err)
	}
	return nil
}

// FindDepartmentIDByCode 按科室编码查 ID。
func (r *SyncRepo) FindDepartmentIDByCode(ctx context.Context, code string) (uint64, error) {
	var id uint64

	err := r.db.WithContext(ctx).
		Model(&model.Department{}).
		Where("code = ?", code).
		Limit(1).
		Pluck("id", &id).Error
	if err != nil {
		return 0, fmt.Errorf("find department by code: %w", err)
	}
	if id == 0 {
		return 0, ErrNotFound
	}
	return id, nil
}

// UpsertDoctor 创建/更新医生账号与档案。
//
// 三步在同一事务内完成：账号、档案、科室关联缺一不可，
// 中途失败会留下"有账号没档案"或"有档案没科室"的残缺数据，
// 表现为医生在列表里凭空消失。
//
// 不返回医生档案 ID：唯一的调用方用不到它（曾经返回但被丢弃），
// 需要时再加比留一个无人使用的返回值更好。
func (r *SyncRepo) UpsertDoctor(
	ctx context.Context, in SyncDoctorInput, departmentID uint64, isPrimary bool,
) error {
	var doctorID uint64

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 1. 账号：phone_hash 上有唯一索引，冲突即忽略后回查
		user := model.User{
			Role:         model.RoleDoctor,
			Name:         in.Name,
			PhoneEnc:     in.PhoneEnc,
			PhoneHash:    in.PhoneHash,
			PasswordHash: in.PasswordHash,
			Status:       model.StatusActive,
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&user).Error; err != nil {
			return fmt.Errorf("upsert doctor user: %w", err)
		}
		if user.ID == 0 {
			if err := tx.Where("phone_hash = ?", in.PhoneHash).First(&user).Error; err != nil {
				return fmt.Errorf("find doctor user: %w", err)
			}
		}

		// 2. 医生档案：以 external_id 为锚点
		doctor := model.Doctor{
			UserID:     user.ID,
			ExternalID: &in.ExternalID,
			Title:      in.Title,
			ConsultFee: in.ConsultFee,
			Status:     model.StatusActive,
		}
		if in.Specialty != "" {
			doctor.Specialty = &in.Specialty
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "external_id"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"title", "specialty", "consult_fee", "status", "updated_at",
			}),
		}).Create(&doctor).Error; err != nil {
			return fmt.Errorf("upsert doctor: %w", err)
		}
		if doctor.ID == 0 {
			if err := tx.Where("external_id = ?", in.ExternalID).First(&doctor).Error; err != nil {
				return fmt.Errorf("find doctor: %w", err)
			}
		}
		doctorID = doctor.ID

		// 3. 科室关联（多对多）
		rel := model.DoctorDepartment{
			DoctorID:     doctorID,
			DepartmentID: departmentID,
			IsPrimary:    isPrimary,
		}
		return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&rel).Error
	})
	if err != nil {
		return err
	}
	return nil
}

// FindDoctorIDByExternalID 按 HIS 外部 ID 查医生档案 ID。
func (r *SyncRepo) FindDoctorIDByExternalID(ctx context.Context, externalID string) (uint64, error) {
	var id uint64

	err := r.db.WithContext(ctx).
		Model(&model.Doctor{}).
		Where("external_id = ?", externalID).
		Limit(1).
		Pluck("id", &id).Error
	if err != nil {
		return 0, fmt.Errorf("find doctor by external id: %w", err)
	}
	if id == 0 {
		return 0, ErrNotFound
	}
	return id, nil
}

// UpsertSchedule 按 external_id upsert 排班。
//
// **关键：已存在的排班不重置 remaining。**
// 若把 total 与 remaining 一起覆盖，HIS 每次同步都会把已被预约的号源"还回去"，
// 直接造成超卖。正确做法是只跟随 total 的**增量**调整 remaining：
//
//	remaining' = max(0, remaining + (total_new - total_old))
//
// 用事务包住"读旧值 + 写新值"，避免并发同步把增量算错。
func (r *SyncRepo) UpsertSchedule(
	ctx context.Context, in model.Schedule, externalID string,
) error {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing model.Schedule

		findErr := tx.Where("external_id = ?", externalID).First(&existing).Error
		switch {
		case errors.Is(findErr, gorm.ErrRecordNotFound):
			in.ExternalID = &externalID
			in.Remaining = in.Total
			in.Status = model.ScheduleStatusOpen
			if err := tx.Create(&in).Error; err != nil {
				return fmt.Errorf("create schedule: %w", err)
			}
			return nil

		case findErr != nil:
			return fmt.Errorf("find schedule: %w", findErr)
		}

		delta := in.Total - existing.Total
		remaining := existing.Remaining + delta
		if remaining < 0 {
			remaining = 0
		}
		if remaining > in.Total {
			remaining = in.Total
		}

		return tx.Model(&model.Schedule{}).Where("id = ?", existing.ID).
			Updates(map[string]any{
				"doctor_id":     in.DoctorID,
				"department_id": in.DepartmentID,
				"sched_date":    in.SchedDate,
				"time_slot":     in.TimeSlot,
				"total":         in.Total,
				"remaining":     remaining,
				"updated_at":    gorm.Expr("NOW(3)"),
			}).Error
	})
	return err
}
