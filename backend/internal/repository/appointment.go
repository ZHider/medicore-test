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

// 领域错误。service 层据此转换为业务错误码。
var (
	// ErrNoStock 号源已尽（排班存在但 remaining 为 0）。
	ErrNoStock = errors.New("no stock")
	// ErrInvalidState 当前状态不允许该操作（如已取消的预约不能再取消）。
	ErrInvalidState = errors.New("invalid appointment state")
)

// CreateAppointmentInput 是创建预约的输入。
type CreateAppointmentInput struct {
	UserID         uint64
	ScheduleID     uint64
	DoctorID       uint64
	DepartmentID   uint64
	SchedDate      time.Time
	TimeSlot       string
	IdempotencyKey string
	ExpireAt       time.Time
}

// StateChangeResult 是取消/过期这类"状态跃迁 + 回补号源"操作的结果。
// 携带 ScheduleTotal 是为了让 service 层不必再多查一次排班就能回补 Redis 号源。
type StateChangeResult struct {
	Appointment   *model.Appointment
	ScheduleTotal int
	// Changed 为 false 表示状态已被其他实例/请求处理过（幂等重复执行）。
	Changed bool
}

// AppointmentRepo 是 appointments 表的数据访问。
type AppointmentRepo struct {
	db *gorm.DB
}

// NewAppointmentRepo 构造 AppointmentRepo。
func NewAppointmentRepo(db *gorm.DB) *AppointmentRepo {
	return &AppointmentRepo{db: db}
}

// Create 在一个事务中扣减 MySQL 号源并写入预约。
//
// 事务内只做数据库写入，不含任何网络调用（spec §3.2 铁律 3）：
// Redis 的扣减在进入本方法之前已经完成。
func (r *AppointmentRepo) Create(ctx context.Context, in CreateAppointmentInput) (*model.Appointment, error) {
	var appt model.Appointment

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 扣减号源。单条 UPDATE 在 InnoDB 行锁下已是原子的，
		// 所以 `remaining > 0` 本身就是充分的并发条件。
		// 若改成"先读 version 再比对更新"，会多一次 SELECT 且必须写重试循环，
		// 在 Redis 已经挡过一层的前提下属于纯粹的复杂度负担。
		res := tx.Model(&model.Schedule{}).
			Where("id = ? AND status = ? AND remaining > 0", in.ScheduleID, model.ScheduleStatusOpen).
			Updates(map[string]any{
				"remaining": gorm.Expr("remaining - 1"),
				"version":   gorm.Expr("version + 1"),
			})
		if res.Error != nil {
			return fmt.Errorf("decrement schedule stock: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			// RowsAffected=0 有两种原因，必须区分：
			// 排班不存在/已关闭 vs 号源已尽。混为一谈会给用户错误的提示。
			var exists int64
			if err := tx.Model(&model.Schedule{}).
				Where("id = ? AND status = ?", in.ScheduleID, model.ScheduleStatusOpen).
				Count(&exists).Error; err != nil {
				return fmt.Errorf("check schedule: %w", err)
			}
			if exists == 0 {
				return ErrNotFound
			}
			return ErrNoStock
		}

		appt = model.Appointment{
			UserID:         in.UserID,
			ScheduleID:     in.ScheduleID,
			DoctorID:       in.DoctorID,
			DepartmentID:   in.DepartmentID,
			SchedDate:      in.SchedDate,
			TimeSlot:       in.TimeSlot,
			Status:         model.AppointmentStatusPending,
			IdempotencyKey: in.IdempotencyKey,
			ExpireAt:       in.ExpireAt,
		}
		if err := tx.Create(&appt).Error; err != nil {
			// uk_user_idem 或 uk_active 冲突：都是"重复抢号"，
			// 由上层转成幂等语义而不是 500。
			if errors.Is(err, gorm.ErrDuplicatedKey) {
				return fmt.Errorf("create appointment: %w", ErrDuplicateKey)
			}
			return fmt.Errorf("create appointment: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &appt, nil
}

// FindByID 按主键查找预约。
func (r *AppointmentRepo) FindByID(ctx context.Context, id uint64) (*model.Appointment, error) {
	var appt model.Appointment

	err := r.db.WithContext(ctx).First(&appt, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("find appointment: %w", err)
	}
	return &appt, nil
}

// FindByUserAndIdempotencyKey 用于幂等重放：返回该用户此前用同一幂等键创建的预约。
func (r *AppointmentRepo) FindByUserAndIdempotencyKey(
	ctx context.Context, userID uint64, key string,
) (*model.Appointment, error) {
	var appt model.Appointment

	err := r.db.WithContext(ctx).
		Where("user_id = ? AND idempotency_key = ?", userID, key).
		First(&appt).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("find appointment by idempotency key: %w", err)
	}
	return &appt, nil
}

// ListByUserCursor 游标分页返回用户的预约，附带医生与科室名称。
func (r *AppointmentRepo) ListByUserCursor(
	ctx context.Context, userID, cursor uint64, limit int,
) ([]model.AppointmentItem, error) {
	q := r.db.WithContext(ctx).
		Table("appointments AS a").
		Select(`a.id, a.schedule_id, a.doctor_id,
		        COALESCE(u.name, '') AS doctor_name,
		        a.department_id,
		        COALESCE(dp.name, '') AS department_name,
		        DATE_FORMAT(a.sched_date, '%Y-%m-%d') AS sched_date,
		        a.time_slot, a.queue_no, a.status,
		        DATE_FORMAT(a.expire_at, '%Y-%m-%d %H:%i:%s') AS expire_at,
		        DATE_FORMAT(a.created_at, '%Y-%m-%d %H:%i:%s') AS created_at`).
		// 用 LEFT JOIN 取展示用名称：医生即便已被软删除，
		// 历史预约仍应显示当时的医生姓名，而不是空白。
		Joins("LEFT JOIN doctors AS d ON d.id = a.doctor_id").
		Joins("LEFT JOIN users AS u ON u.id = d.user_id").
		Joins("LEFT JOIN departments AS dp ON dp.id = a.department_id").
		// 手写 JOIN 不会自动带软删除条件，这里只需过滤预约本身。
		Where("a.deleted_at IS NULL").
		Where("a.user_id = ?", userID)

	if cursor > 0 {
		q = q.Where("a.id < ?", cursor)
	}

	var items []model.AppointmentItem
	if err := q.Order("a.id DESC").Limit(limit + 1).Scan(&items).Error; err != nil {
		return nil, fmt.Errorf("list appointments: %w", err)
	}
	return items, nil
}

// Cancel 取消预约并回补 MySQL 号源。
func (r *AppointmentRepo) Cancel(
	ctx context.Context, userID, appointmentID uint64, reason string,
) (*StateChangeResult, error) {
	var (
		appt  model.Appointment
		total int
	)

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// FOR UPDATE 串行化同一预约的并发取消，避免重复回补号源。
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND user_id = ?", appointmentID, userID).
			First(&appt).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock appointment: %w", err)
		}
		if !appt.IsActive() {
			return ErrInvalidState
		}

		now := time.Now()
		if err := tx.Model(&model.Appointment{}).Where("id = ?", appt.ID).
			Updates(map[string]any{
				"status":        model.AppointmentStatusCancelled,
				"cancelled_at":  now,
				"cancel_reason": reason,
			}).Error; err != nil {
			return fmt.Errorf("cancel appointment: %w", err)
		}

		var sched model.Schedule
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&sched, appt.ScheduleID).Error; err != nil {
			return fmt.Errorf("lock schedule: %w", err)
		}
		total = sched.Total

		// remaining < total 作为容量上限：即使出现重复回补也不会超过总号源。
		if err := tx.Model(&model.Schedule{}).
			Where("id = ? AND remaining < total", sched.ID).
			Updates(map[string]any{
				"remaining": gorm.Expr("remaining + 1"),
				"version":   gorm.Expr("version + 1"),
			}).Error; err != nil {
			return fmt.Errorf("restore schedule stock: %w", err)
		}

		appt.Status = model.AppointmentStatusCancelled
		appt.CancelledAt = &now
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &StateChangeResult{Appointment: &appt, ScheduleTotal: total, Changed: true}, nil
}

// ListExpiredPending 返回已超时但仍占用号源的预约，供 cron 释放。
// 命中 idx_status_expire (status, expire_at)。
func (r *AppointmentRepo) ListExpiredPending(
	ctx context.Context, now time.Time, limit int,
) ([]model.Appointment, error) {
	var rows []model.Appointment

	err := r.db.WithContext(ctx).
		Where("status = ? AND expire_at < ?", model.AppointmentStatusPending, now).
		Order("id ASC").
		Limit(limit).
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list expired pending appointments: %w", err)
	}
	return rows, nil
}

// Expire 把超时预约置为 expired 并回补 MySQL 号源。
//
// 用"带状态条件的 UPDATE"作为幂等闸门：只有仍为 pending 时才命中，
// 因此多实例并发扫描同一批订单时不会重复回补号源。
func (r *AppointmentRepo) Expire(ctx context.Context, appointmentID uint64) (*StateChangeResult, error) {
	var (
		appt    model.Appointment
		total   int
		changed bool
	)

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&model.Appointment{}).
			Where("id = ? AND status = ?", appointmentID, model.AppointmentStatusPending).
			Update("status", model.AppointmentStatusExpired)
		if res.Error != nil {
			return fmt.Errorf("expire appointment: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			// 已被其它实例处理，直接返回，不重复回补
			return nil
		}
		changed = true

		if err := tx.First(&appt, appointmentID).Error; err != nil {
			return fmt.Errorf("load expired appointment: %w", err)
		}

		var sched model.Schedule
		if err := tx.First(&sched, appt.ScheduleID).Error; err != nil {
			return fmt.Errorf("load schedule: %w", err)
		}
		total = sched.Total

		if err := tx.Model(&model.Schedule{}).
			Where("id = ? AND remaining < total", sched.ID).
			Updates(map[string]any{
				"remaining": gorm.Expr("remaining + 1"),
				"version":   gorm.Expr("version + 1"),
			}).Error; err != nil {
			return fmt.Errorf("restore schedule stock: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &StateChangeResult{Appointment: &appt, ScheduleTotal: total, Changed: changed}, nil
}

// CheckIn 为预约分配就诊序号。
//
// 序号按**实际签到顺序**分配：先锁排班行使同一排班的签到串行，
// 再取当前最大序号加一。表上的 uk_sched_queue 唯一键是最后一道防线。
func (r *AppointmentRepo) CheckIn(
	ctx context.Context, userID, appointmentID uint64,
) (*model.Appointment, error) {
	var appt model.Appointment

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND user_id = ?", appointmentID, userID).
			First(&appt).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock appointment: %w", err)
		}

		// 幂等：已签到直接返回原序号，患者重复点击不会重新排队。
		if appt.CheckedInAt != nil {
			return nil
		}
		if !appt.IsActive() {
			return ErrInvalidState
		}

		var sched model.Schedule
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&sched, appt.ScheduleID).Error; err != nil {
			return fmt.Errorf("lock schedule: %w", err)
		}

		var row struct {
			MaxNo *int `gorm:"column:max_no"`
		}
		if err := tx.Model(&model.Appointment{}).
			Select("MAX(queue_no) AS max_no").
			Where("schedule_id = ?", appt.ScheduleID).
			Scan(&row).Error; err != nil {
			return fmt.Errorf("compute next queue no: %w", err)
		}

		next := 1
		if row.MaxNo != nil {
			next = *row.MaxNo + 1
		}

		now := time.Now()
		if err := tx.Model(&model.Appointment{}).Where("id = ?", appt.ID).
			Updates(map[string]any{"checked_in_at": now, "queue_no": next}).Error; err != nil {
			return fmt.Errorf("check in: %w", err)
		}

		appt.CheckedInAt = &now
		appt.QueueNo = &next
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &appt, nil
}

// ListQueue 返回某排班已签到的候诊队列，按序号升序。
//
// 只包含仍然有效的预约：已取消或已过期的患者不应继续出现在候诊队列中，
// 否则医生会对着一个不会来的号叫诊。
func (r *AppointmentRepo) ListQueue(ctx context.Context, scheduleID uint64) ([]model.QueueItem, error) {
	var items []model.QueueItem

	err := r.db.WithContext(ctx).
		Table("appointments AS a").
		Select(`a.id, a.queue_no, a.user_id,
		        COALESCE(p.name, '') AS patient_name,
		        DATE_FORMAT(a.checked_in_at, '%Y-%m-%d %H:%i:%s') AS checked_in_at,
		        a.status`).
		Joins("LEFT JOIN users AS p ON p.id = a.user_id").
		Where("a.schedule_id = ? AND a.queue_no IS NOT NULL", scheduleID).
		Where("a.status IN ?", []string{
			model.AppointmentStatusPending,
			model.AppointmentStatusConfirmed,
		}).
		Where("a.deleted_at IS NULL").
		Order("a.queue_no ASC").
		Scan(&items).Error
	if err != nil {
		return nil, fmt.Errorf("list queue: %w", err)
	}
	return items, nil
}

// FindItemByID 返回单个预约的展示结构（含医生与科室名称）。
// 幂等重放时用它保证返回结构与首次创建完全一致。
func (r *AppointmentRepo) FindItemByID(
	ctx context.Context, userID, appointmentID uint64,
) (*model.AppointmentItem, error) {
	var item model.AppointmentItem

	err := r.db.WithContext(ctx).
		Table("appointments AS a").
		Select(`a.id, a.schedule_id, a.doctor_id,
		        COALESCE(u.name, '') AS doctor_name,
		        a.department_id,
		        COALESCE(dp.name, '') AS department_name,
		        DATE_FORMAT(a.sched_date, '%Y-%m-%d') AS sched_date,
		        a.time_slot, a.queue_no, a.status,
		        DATE_FORMAT(a.expire_at, '%Y-%m-%d %H:%i:%s') AS expire_at,
		        DATE_FORMAT(a.created_at, '%Y-%m-%d %H:%i:%s') AS created_at`).
		Joins("LEFT JOIN doctors AS d ON d.id = a.doctor_id").
		Joins("LEFT JOIN users AS u ON u.id = d.user_id").
		Joins("LEFT JOIN departments AS dp ON dp.id = a.department_id").
		Where("a.deleted_at IS NULL").
		Where("a.id = ? AND a.user_id = ?", appointmentID, userID).
		Scan(&item).Error
	if err != nil {
		return nil, fmt.Errorf("find appointment item: %w", err)
	}
	if item.ID == 0 {
		return nil, ErrNotFound
	}
	return &item, nil
}
