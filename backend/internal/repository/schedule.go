package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/medicore/backend/internal/model"
)

// scheduleSelectColumns 统一排班查询的列与别名，
// 避免多处出现不一致的 DATE_FORMAT 写法。
const scheduleSelectColumns = `id,
	doctor_id,
	department_id,
	DATE_FORMAT(sched_date, '%Y-%m-%d') AS sched_date,
	time_slot,
	total,
	remaining,
	status`

// ScheduleRepo 是 schedules 表的数据访问。
type ScheduleRepo struct {
	db *gorm.DB
}

// NewScheduleRepo 构造 ScheduleRepo。
func NewScheduleRepo(db *gorm.DB) *ScheduleRepo {
	return &ScheduleRepo{db: db}
}

// ListByDepartmentDate 按"科室 + 就诊日"查询可挂号源，游标分页。
//
// 使用 Model(&model.Schedule{}) 而非 Table(...)：前者会自动带上软删除条件。
func (r *ScheduleRepo) ListByDepartmentDate(
	ctx context.Context,
	departmentID uint64,
	date time.Time,
	cursor uint64,
	limit int,
) ([]model.ScheduleListItem, error) {
	q := r.db.WithContext(ctx).
		Model(&model.Schedule{}).
		Select(scheduleSelectColumns).
		Where("department_id = ?", departmentID).
		Where("sched_date = ?", date.Format(time.DateOnly)).
		Where("status = ?", model.ScheduleStatusOpen)

	q = applyCursor(q, cursor)

	var items []model.ScheduleListItem
	if err := q.Order("id DESC").Limit(limit + 1).Scan(&items).Error; err != nil {
		return nil, fmt.Errorf("list schedules by department date: %w", err)
	}
	return items, nil
}

// ListByDoctorRange 按"医生 + 日期区间"查询排班，游标分页。
func (r *ScheduleRepo) ListByDoctorRange(
	ctx context.Context,
	doctorID uint64,
	from, to time.Time,
	cursor uint64,
	limit int,
) ([]model.ScheduleListItem, error) {
	q := r.db.WithContext(ctx).
		Model(&model.Schedule{}).
		Select(scheduleSelectColumns).
		Where("doctor_id = ?", doctorID).
		Where("sched_date BETWEEN ? AND ?", from.Format(time.DateOnly), to.Format(time.DateOnly)).
		Where("status = ?", model.ScheduleStatusOpen)

	q = applyCursor(q, cursor)

	var items []model.ScheduleListItem
	if err := q.Order("id DESC").Limit(limit + 1).Scan(&items).Error; err != nil {
		return nil, fmt.Errorf("list schedules by doctor range: %w", err)
	}
	return items, nil
}

// ListReconcilable 按 ID 升序扫描需要参与对账的排班：今天起、且仍开放中的。
//
// 这是后台全量遍历而非面向用户的翻页，因此用 "id > afterID 升序" 的稳定顺序，
// 而不是 `id < cursor 降序` 的游标分页语义——后者会在遍历期间漏掉新增行。
func (r *ScheduleRepo) ListReconcilable(
	ctx context.Context, from time.Time, afterID uint64, limit int,
) ([]model.Schedule, error) {
	var rows []model.Schedule

	err := r.db.WithContext(ctx).
		Where("status = ?", model.ScheduleStatusOpen).
		Where("sched_date >= ?", from.Format(time.DateOnly)).
		Where("id > ?", afterID).
		Order("id ASC").
		Limit(limit).
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list reconcilable schedules: %w", err)
	}
	return rows, nil
}

// FindByID 按主键查找排班，供后续抢号流程使用。
func (r *ScheduleRepo) FindByID(ctx context.Context, id uint64) (*model.Schedule, error) {
	var s model.Schedule

	err := r.db.WithContext(ctx).First(&s, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("find schedule: %w", err)
	}
	return &s, nil
}

// applyCursor 追加游标条件。
// 首页（cursor=0）不加条件，后续页用 `id < cursor` —— 这正是
// idx_user_cursor / PRIMARY 反向扫描能生效的写法，绝不使用 OFFSET。
func applyCursor(q *gorm.DB, cursor uint64) *gorm.DB {
	if cursor == 0 {
		return q
	}
	return q.Where("id < ?", cursor)
}
