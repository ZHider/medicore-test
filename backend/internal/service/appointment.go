package service

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"

	"github.com/medicore/backend/internal/apperr"
	"github.com/medicore/backend/internal/errcode"
	"github.com/medicore/backend/internal/metrics"
	"github.com/medicore/backend/internal/model"
	"github.com/medicore/backend/internal/repository"
)

// expiredBatchSize 是单轮清理处理的过期订单上限。
// 设上限是为了让每轮 cron 的耗时可控，积压时由下一轮继续处理。
const expiredBatchSize = 200

// 抢号结果标签（spec §9.2 要求 appointment_grab_total）。
const (
	grabResultSuccess   = "success"
	grabResultDuplicate = "duplicate"
	grabResultInFlight  = "in_flight"
	grabResultNoStock   = "no_stock"
	grabResultError     = "error"
)

var appointmentGrabTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "appointment_grab_total",
	Help: "抢号请求计数，按结果分类。",
}, []string{"result"})

func init() {
	metrics.Register(appointmentGrabTotal)
}

// AppointmentDeps 是 AppointmentService 的依赖集合。
type AppointmentDeps struct {
	Appointments appointmentStore
	// DoctorAppointments 供医生/管理员视角的列表使用；与患者侧分开声明。
	DoctorAppointments doctorAppointmentStore
	Doctors            doctorLookup
	Janitor            appointmentJanitor
	Schedules          scheduleStore
	Stock              stockStore
	Idempotency        idempotencyStore
	Log                *zap.Logger
	ConfirmWindow      time.Duration
}

// AppointmentService 承载抢号、取消、签到与超时释放。
type AppointmentService struct {
	appointments       appointmentStore
	doctorAppointments doctorAppointmentStore
	doctors            doctorLookup
	janitor            appointmentJanitor
	schedules          scheduleStore
	stock              stockStore
	idempotency        idempotencyStore
	log                *zap.Logger
	confirmWindow      time.Duration
}

// NewAppointmentService 构造 AppointmentService。
func NewAppointmentService(d AppointmentDeps) *AppointmentService {
	log := d.Log
	if log == nil {
		log = zap.NewNop()
	}
	window := d.ConfirmWindow
	if window <= 0 {
		window = model.DefaultConfirmWindow
	}

	return &AppointmentService{
		appointments:       d.Appointments,
		doctorAppointments: d.DoctorAppointments,
		doctors:            d.Doctors,
		janitor:            d.Janitor,
		schedules:          d.Schedules,
		stock:              d.Stock,
		idempotency:        d.Idempotency,
		log:                log,
		confirmWindow:      window,
	}
}

// Grab 抢号。
//
// 完整链路（spec §7.1，并修正了其中的两个缺陷）：
//
//	幂等占用 → 预热号源 → Redis 原子扣减 → MySQL 事务落库 → 记录幂等结果
//
// 修正点：
//  1. Redis 键不存在时不再当作"号源不足"，而是先预热再重试（spec 原设计会让冷启动全盘失败）。
//  2. 幂等键用"比对删除"释放，避免失败清理时误删已完成的记录。
func (s *AppointmentService) Grab(
	ctx context.Context, userID uint64, req model.GrabRequest,
) (*model.AppointmentItem, error) {
	schedule, err := s.loadGrabbableSchedule(ctx, req.ScheduleID)
	if err != nil {
		return nil, err
	}

	// 客户端未提供幂等键时服务端补一个，仍能防住"同一次提交被重放"。
	idemKey := req.IdempotencyKey
	if idemKey == "" {
		idemKey = uuid.NewString()
	}

	acquired, existingID, err := s.idempotency.Acquire(ctx, userID, idemKey)
	if errors.Is(err, repository.ErrInFlight) {
		appointmentGrabTotal.WithLabelValues(grabResultInFlight).Inc()
		return nil, apperr.New(errcode.ErrAlreadyDone, "请求正在处理中，请勿重复提交")
	}
	if err != nil {
		appointmentGrabTotal.WithLabelValues(grabResultError).Inc()
		return nil, apperr.Wrap(errcode.ErrInternal, "幂等校验失败", err)
	}
	if !acquired {
		// 幂等重放：返回首次创建的预约，保证响应结构完全一致
		item, err := s.appointments.FindItemByID(ctx, userID, existingID)
		if err != nil {
			appointmentGrabTotal.WithLabelValues(grabResultError).Inc()
			return nil, apperr.Wrap(errcode.ErrInternal, "读取已有预约失败", err)
		}
		appointmentGrabTotal.WithLabelValues(grabResultDuplicate).Inc()
		return item, nil
	}

	item, err := s.doGrab(ctx, userID, idemKey, schedule)
	if err != nil {
		// 释放幂等键，让客户端修正后可以重试；
		// 只有仍处于"处理中"的键才会被删除，已完成的记录不受影响。
		if relErr := s.idempotency.Release(ctx, userID, idemKey); relErr != nil {
			s.log.Warn("release idempotency key failed",
				zap.Uint64("user_id", userID), zap.Error(relErr))
		}
		return nil, err
	}
	return item, nil
}

func (s *AppointmentService) doGrab(
	ctx context.Context, userID uint64, idemKey string, schedule *model.Schedule,
) (*model.AppointmentItem, error) {
	if err := s.decrementStock(ctx, schedule); err != nil {
		return nil, err
	}

	// 此处 Redis 号源已扣减，后续任何失败都必须回补，否则号源会凭空蒸发。
	appt, err := s.appointments.Create(ctx, repository.CreateAppointmentInput{
		UserID:         userID,
		ScheduleID:     schedule.ID,
		DoctorID:       schedule.DoctorID,
		DepartmentID:   schedule.DepartmentID,
		SchedDate:      schedule.SchedDate,
		TimeSlot:       schedule.TimeSlot,
		IdempotencyKey: idemKey,
		ExpireAt:       time.Now().Add(s.confirmWindow),
	})
	if err != nil {
		// MySQL 事务已回滚，Redis 必须同步回补
		s.releaseStockForRedis(ctx, schedule.ID, schedule.Total)

		switch {
		case errors.Is(err, repository.ErrNoStock):
			appointmentGrabTotal.WithLabelValues(grabResultNoStock).Inc()
			return nil, apperr.New(errcode.ErrNoStock, "号源已不足")
		case errors.Is(err, repository.ErrDuplicateKey):
			return s.recoverDuplicate(ctx, userID, idemKey)
		case errors.Is(err, repository.ErrNotFound):
			appointmentGrabTotal.WithLabelValues(grabResultError).Inc()
			return nil, apperr.New(errcode.ErrNotFound, "排班不存在或已停止挂号")
		default:
			appointmentGrabTotal.WithLabelValues(grabResultError).Inc()
			return nil, apperr.Wrap(errcode.ErrInternal, "创建预约失败", err)
		}
	}

	// 记录幂等结果。写失败不影响本次预约（已经创建成功），
	// 但要让重试能靠数据库唯一索引兜底，因此释放键而不是留下"处理中"。
	if err := s.idempotency.Complete(ctx, userID, idemKey, appt.ID); err != nil {
		s.log.Warn("complete idempotency key failed",
			zap.Uint64("user_id", userID), zap.Uint64("appointment_id", appt.ID), zap.Error(err))
		if relErr := s.idempotency.Release(ctx, userID, idemKey); relErr != nil {
			s.log.Warn("release idempotency key after complete failure", zap.Error(relErr))
		}
	}

	item, err := s.appointments.FindItemByID(ctx, userID, appt.ID)
	if err != nil {
		appointmentGrabTotal.WithLabelValues(grabResultError).Inc()
		return nil, apperr.Wrap(errcode.ErrInternal, "读取新预约失败", err)
	}
	appointmentGrabTotal.WithLabelValues(grabResultSuccess).Inc()
	return item, nil
}

// recoverDuplicate 处理数据库唯一键冲突。
//
// 走到这里说明幂等键没拦住（例如 Redis 失效），但数据库的
// uk_user_idem / uk_active 兜住了。此时返回已存在的预约，
// 语义上仍是一次成功的幂等重放。
func (s *AppointmentService) recoverDuplicate(
	ctx context.Context, userID uint64, idemKey string,
) (*model.AppointmentItem, error) {
	existing, err := s.appointments.FindByUserAndIdempotencyKey(ctx, userID, idemKey)
	if err != nil {
		appointmentGrabTotal.WithLabelValues(grabResultDuplicate).Inc()
		// 不是同一幂等键的重复，而是 uk_active 命中：该用户已占用同一号源
		return nil, apperr.New(errcode.ErrConflict, "您已预约该号源，请勿重复抢号")
	}

	item, err := s.appointments.FindItemByID(ctx, userID, existing.ID)
	if err != nil {
		appointmentGrabTotal.WithLabelValues(grabResultError).Inc()
		return nil, apperr.Wrap(errcode.ErrInternal, "读取已有预约失败", err)
	}
	appointmentGrabTotal.WithLabelValues(grabResultDuplicate).Inc()
	return item, nil
}

// decrementStock 预热并原子扣减 Redis 号源。
func (s *AppointmentService) decrementStock(ctx context.Context, schedule *model.Schedule) error {
	if err := s.stock.EnsureLoaded(ctx, schedule); err != nil {
		appointmentGrabTotal.WithLabelValues(grabResultError).Inc()
		return apperr.Wrap(errcode.ErrInternal, "号源预热失败", err)
	}

	result, err := s.stock.Decrement(ctx, schedule.ID, 1)
	if err != nil {
		appointmentGrabTotal.WithLabelValues(grabResultError).Inc()
		return apperr.Wrap(errcode.ErrInternal, "号源扣减失败", err)
	}

	// 键在"预热"与"扣减"之间过期：重新预热后重试一次。
	// 只重试一次，避免在 Redis 异常时形成重试风暴。
	if result == repository.StockNotLoaded {
		if err := s.stock.EnsureLoaded(ctx, schedule); err != nil {
			appointmentGrabTotal.WithLabelValues(grabResultError).Inc()
			return apperr.Wrap(errcode.ErrInternal, "号源预热失败", err)
		}
		result, err = s.stock.Decrement(ctx, schedule.ID, 1)
		if err != nil {
			appointmentGrabTotal.WithLabelValues(grabResultError).Inc()
			return apperr.Wrap(errcode.ErrInternal, "号源扣减失败", err)
		}
	}

	switch result {
	case repository.StockOK:
		return nil
	case repository.StockInsufficient:
		appointmentGrabTotal.WithLabelValues(grabResultNoStock).Inc()
		return apperr.New(errcode.ErrNoStock, "号源已不足")
	default:
		// 预热后仍取不到键，说明 Redis 状态异常
		appointmentGrabTotal.WithLabelValues(grabResultError).Inc()
		return apperr.New(errcode.ErrInternal, "号源状态异常，请稍后重试")
	}
}

// releaseStockForRedis 回补 Redis 号源。
//
// 失败不影响正确性：MySQL 才是事实来源，且键不存在时下次抢号会从数据库重新预热。
// 但仍要记录，否则会掩盖持续性的 Redis 异常。
func (s *AppointmentService) releaseStockForRedis(ctx context.Context, scheduleID uint64, capacity int) {
	remaining, err := s.stock.Release(ctx, scheduleID, 1, capacity)
	if err != nil {
		s.log.Warn("release redis stock failed",
			zap.Uint64("schedule_id", scheduleID), zap.Error(err))
		return
	}
	if remaining < 0 {
		s.log.Debug("redis stock key absent on release",
			zap.Uint64("schedule_id", scheduleID))
	}
}

func (s *AppointmentService) loadGrabbableSchedule(
	ctx context.Context, scheduleID uint64,
) (*model.Schedule, error) {
	schedule, err := s.schedules.FindByID(ctx, scheduleID)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, apperr.New(errcode.ErrNotFound, "排班不存在")
	}
	if err != nil {
		return nil, apperr.Wrap(errcode.ErrInternal, "查询排班失败", err)
	}
	if schedule.Status != model.ScheduleStatusOpen {
		return nil, apperr.New(errcode.ErrConflict, "该排班已停止挂号")
	}
	return schedule, nil
}

// ListMine 游标分页返回当前用户的预约。
func (s *AppointmentService) ListMine(
	ctx context.Context, userID uint64, cursorRaw string, size int,
) (model.CursorPage[model.AppointmentItem], error) {
	var empty model.CursorPage[model.AppointmentItem]

	cursor, err := parseCursor(cursorRaw)
	if err != nil {
		return empty, err
	}
	limit := model.NormalizePageSize(size)

	rows, err := s.appointments.ListByUserCursor(ctx, userID, cursor, limit)
	if err != nil {
		return empty, apperr.Wrap(errcode.ErrInternal, "查询预约失败", err)
	}
	return model.NewCursorPage(rows, limit, model.AppointmentItem.GetID), nil
}

// Cancel 取消预约并回补号源。
func (s *AppointmentService) Cancel(
	ctx context.Context, userID, appointmentID uint64, reason string,
) error {
	result, err := s.appointments.Cancel(ctx, userID, appointmentID, reason)
	switch {
	case errors.Is(err, repository.ErrNotFound):
		return apperr.New(errcode.ErrNotFound, "预约不存在")
	case errors.Is(err, repository.ErrInvalidState):
		return apperr.New(errcode.ErrConflict, "当前状态的预约无法取消")
	case err != nil:
		return apperr.Wrap(errcode.ErrInternal, "取消预约失败", err)
	}

	// MySQL 已回补，同步让 Redis 快速闸门跟上
	s.releaseStockForRedis(ctx, result.Appointment.ScheduleID, result.ScheduleTotal)
	return nil
}

// CheckIn 为预约分配就诊序号（按实际签到顺序）。
func (s *AppointmentService) CheckIn(
	ctx context.Context, userID, appointmentID uint64,
) (*model.Appointment, error) {
	appt, err := s.appointments.CheckIn(ctx, userID, appointmentID)
	switch {
	case errors.Is(err, repository.ErrNotFound):
		return nil, apperr.New(errcode.ErrNotFound, "预约不存在")
	case errors.Is(err, repository.ErrInvalidState):
		return nil, apperr.New(errcode.ErrConflict, "当前状态的预约无法签到")
	case err != nil:
		return nil, apperr.Wrap(errcode.ErrInternal, "签到失败", err)
	}
	return appt, nil
}

// Queue 返回某排班的候诊队列。
//
// 这里**刻意不对患者姓名脱敏**：医生必须能识别面前的患者，
// 把姓名打成"张*"会让队列失去意义。该接口仅对 doctor/admin 开放，
// 属于 spec §2.2 脱敏规则的合理例外，已在 README 中标注。
func (s *AppointmentService) Queue(ctx context.Context, scheduleID uint64) ([]model.QueueItem, error) {
	items, err := s.appointments.ListQueue(ctx, scheduleID)
	if err != nil {
		return nil, apperr.Wrap(errcode.ErrInternal, "查询候诊队列失败", err)
	}
	return items, nil
}

// ReleaseExpired 释放超时未确认的预约，返回实际释放的订单数。
//
// 由 cron 周期调用。多实例部署时同一批订单可能被同时扫到，
// 但仓库层的"带状态条件的 UPDATE"保证了只有一个实例会真正回补号源。
func (s *AppointmentService) ReleaseExpired(ctx context.Context) (int, error) {
	rows, err := s.janitor.ListExpiredPending(ctx, time.Now(), expiredBatchSize)
	if err != nil {
		return 0, apperr.Wrap(errcode.ErrInternal, "查询超时预约失败", err)
	}

	released := 0
	for _, appt := range rows {
		result, err := s.janitor.Expire(ctx, appt.ID)
		if err != nil {
			// 单条失败不应中断整批，否则一条坏数据会永久阻塞清理任务
			s.log.Error("expire appointment failed",
				zap.Uint64("appointment_id", appt.ID), zap.Error(err))
			continue
		}
		if !result.Changed {
			// 已被其它实例处理，跳过回补避免重复加号
			continue
		}

		s.releaseStockForRedis(ctx, result.Appointment.ScheduleID, result.ScheduleTotal)
		released++

		s.log.Info("appointment expired",
			zap.Uint64("appointment_id", appt.ID),
			zap.Uint64("schedule_id", result.Appointment.ScheduleID))
	}
	return released, nil
}

// ListForDoctor 按访问者身份返回预约列表（spec §2.1：医生查看本人预约列表）。
//
// 医生的"预约列表"指**本人接诊的**预约——不是全部预约，也不是患者视角的"我的预约"。
// 两者响应形态不同（医生要看患者姓名与病历状态），因此是独立端点而非复用 ListMine。
//
// 管理员按 spec §2.1 的"全部读权限"可查看全部。
func (s *AppointmentService) ListForDoctor(
	ctx context.Context,
	access Access,
	filter model.DoctorAppointmentFilter,
	cursorRaw string,
	size int,
) (model.CursorPage[model.DoctorAppointmentItem], error) {
	var empty model.CursorPage[model.DoctorAppointmentItem]

	if !access.IsDoctor() && !access.IsAdmin() {
		return empty, apperr.New(errcode.ErrForbidden, "仅医生与管理员可查看接诊列表")
	}
	if err := validateAppointmentFilter(filter); err != nil {
		return empty, err
	}

	cursor, err := parseCursor(cursorRaw)
	if err != nil {
		return empty, err
	}
	limit := model.NormalizePageSize(size)

	var rows []model.DoctorAppointmentItem
	if access.IsAdmin() {
		rows, err = s.doctorAppointments.ListAllCursor(ctx, filter, cursor, limit)
	} else {
		var doctorID uint64
		doctorID, err = s.doctorIDOf(ctx, access.UserID)
		if err == nil {
			rows, err = s.doctorAppointments.ListByDoctorCursor(ctx, doctorID, filter, cursor, limit)
		}
	}
	if err != nil {
		return empty, err
	}
	return model.NewCursorPage(rows, limit, model.DoctorAppointmentItem.GetID), nil
}

// doctorIDOf 由用户 ID 解析医生档案 ID。
//
// 预约表存的是 doctors.id，直接拿 users.id 比较会永远不相等。
func (s *AppointmentService) doctorIDOf(ctx context.Context, userID uint64) (uint64, error) {
	doctorID, err := s.doctors.FindIDByUserID(ctx, userID)
	if errors.Is(err, repository.ErrNotFound) {
		return 0, apperr.New(errcode.ErrForbidden, "当前账号没有医生档案")
	}
	if err != nil {
		return 0, apperr.Wrap(errcode.ErrInternal, "查询医生档案失败", err)
	}
	return doctorID, nil
}

// validateAppointmentFilter 校验筛选条件。
//
// 状态取值必须白名单：直接塞进 SQL 虽然参数化后不会注入，
// 但拼错的状态会**静默返回空列表**，让人误以为"今天没有预约"。
func validateAppointmentFilter(filter model.DoctorAppointmentFilter) error {
	if filter.Date != "" {
		if _, err := time.ParseInLocation(time.DateOnly, filter.Date, time.Local); err != nil {
			return apperr.New(errcode.ErrInvalidParam, "日期格式应为 YYYY-MM-DD")
		}
	}
	if filter.Status != "" {
		switch filter.Status {
		case model.AppointmentStatusPending, model.AppointmentStatusConfirmed,
			model.AppointmentStatusCancelled, model.AppointmentStatusExpired:
		default:
			return apperr.New(errcode.ErrInvalidParam, "预约状态取值不合法")
		}
	}
	return nil
}
