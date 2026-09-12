package service

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"

	"github.com/medicore/backend/internal/apperr"
	"github.com/medicore/backend/internal/errcode"
	"github.com/medicore/backend/internal/mockhis"
	"github.com/medicore/backend/internal/model"
	"github.com/medicore/backend/internal/repository"
	"github.com/medicore/backend/internal/secret"
)

// SourceName 是模拟 HIS 数据源的名字，写入 sync_logs.source。
const SourceName = "mockhis"

// defaultSyncConcurrency 是同步的并发上限（spec §7.3 要求限并发 5）。
//
// 限并发是必要的：外部系统往往有连接数或 QPS 限制，
// 一次性打出全部请求容易被限流甚至封禁。
const defaultSyncConcurrency = 5

// SyncSource 是外部数据源。真实 HIS 接入时只需替换实现。
type SyncSource interface {
	Load(ctx context.Context) (*mockhis.Dataset, error)
}

// SyncDeps 是 SyncService 的依赖集合。
type SyncDeps struct {
	Source      SyncSource
	Sync        syncStore
	Logs        syncLogStore
	Cipher      *secret.Cipher
	PhonePepper string
	Log         *zap.Logger
	Concurrency int
}

// SyncService 承载 HIS 数据同步（spec §7.3）。
type SyncService struct {
	source      SyncSource
	sync        syncStore
	logs        syncLogStore
	cipher      *secret.Cipher
	phonePepper string
	log         *zap.Logger
	concurrency int
}

// NewSyncService 构造 SyncService。
func NewSyncService(d SyncDeps) *SyncService {
	log := d.Log
	if log == nil {
		log = zap.NewNop()
	}
	concurrency := d.Concurrency
	if concurrency <= 0 {
		concurrency = defaultSyncConcurrency
	}

	return &SyncService{
		source:      d.Source,
		sync:        d.Sync,
		logs:        d.Logs,
		cipher:      d.Cipher,
		phonePepper: d.PhonePepper,
		log:         log,
		concurrency: concurrency,
	}
}

// counters 是同步结果计数。用原子操作：errgroup 的各个 goroutine 会并发累加。
type counters struct {
	succeeded atomic.Int64
	failed    atomic.Int64
}

func (c *counters) markSuccess() { c.succeeded.Add(1) }
func (c *counters) markFailure() { c.failed.Add(1) }

// Sync 执行一次全量同步并返回同步记录。
//
// 分三阶段而非一把梭：排班依赖医生、医生依赖科室，
// 若并发处理全部实体，排班会因为医生还没落库而大量失败。
func (s *SyncService) Sync(ctx context.Context, trigger string) (*model.SyncLogView, error) {
	logID, err := s.logs.Start(ctx, SourceName, trigger)
	if err != nil {
		return nil, apperr.Wrap(errcode.ErrInternal, "创建同步记录失败", err)
	}

	counters, syncErr := s.run(ctx)

	result := repository.SyncResult{
		Total:     int(counters.succeeded.Load() + counters.failed.Load()),
		Succeeded: int(counters.succeeded.Load()),
		Failed:    int(counters.failed.Load()),
		Status:    statusOf(counters),
	}
	if syncErr != nil {
		msg := syncErr.Error()
		result.ErrorMessage = &msg
		result.Status = model.SyncStatusFailed
	}

	if err := s.logs.Finish(ctx, logID, result); err != nil {
		// 结果写不进去只影响记录，不该让同步整体失败
		s.log.Error("finish sync log failed", zap.Uint64("sync_log_id", logID), zap.Error(err))
	}

	s.log.Info("his sync finished",
		zap.String("trigger", trigger),
		zap.String("status", result.Status),
		zap.Int("total", result.Total),
		zap.Int("succeeded", result.Succeeded),
		zap.Int("failed", result.Failed))

	if syncErr != nil {
		return nil, apperr.Wrap(errcode.ErrInternal, "同步失败", syncErr)
	}
	return s.logsView(ctx, logID)
}

// run 执行三阶段同步。
func (s *SyncService) run(ctx context.Context) (*counters, error) {
	dataset, err := s.source.Load(ctx)
	if err != nil {
		return &counters{}, fmt.Errorf("加载数据源: %w", err)
	}

	c := &counters{}

	// 阶段 1：科室
	if err := s.runPhase(ctx, len(dataset.Departments), func(g *errgroup.Group, i int) {
		dept := dataset.Departments[i]
		g.Go(func() error {
			return s.upsertDepartment(ctx, dept, c)
		})
	}); err != nil {
		return c, err
	}

	// 阶段 2：医生（依赖科室）
	if err := s.runPhase(ctx, len(dataset.Doctors), func(g *errgroup.Group, i int) {
		doctor := dataset.Doctors[i]
		g.Go(func() error {
			return s.upsertDoctor(ctx, doctor, c)
		})
	}); err != nil {
		return c, err
	}

	// 阶段 3：排班（依赖医生与科室）
	if err := s.runPhase(ctx, len(dataset.Schedules), func(g *errgroup.Group, i int) {
		schedule := dataset.Schedules[i]
		g.Go(func() error {
			return s.upsertSchedule(ctx, schedule, c)
		})
	}); err != nil {
		return c, err
	}

	return c, nil
}

// runPhase 并发执行一个阶段，并发上限由配置决定。
func (s *SyncService) runPhase(ctx context.Context, total int, launch func(*errgroup.Group, int)) error {
	g, gCtx := errgroup.WithContext(ctx)
	g.SetLimit(s.concurrency)

	for i := 0; i < total; i++ {
		if gCtx.Err() != nil {
			break
		}
		launch(g, i)
	}

	if err := g.Wait(); err != nil {
		return fmt.Errorf("同步阶段失败: %w", err)
	}
	return nil
}

func (s *SyncService) upsertDepartment(ctx context.Context, in mockhis.Department, c *counters) error {
	dept := model.Department{
		ExternalID: &in.ExternalID,
		Code:       in.Code,
		Name:       in.Name,
		SortOrder:  in.SortOrder,
		Status:     model.StatusActive,
	}
	if in.Description != "" {
		dept.Description = &in.Description
	}

	if err := s.sync.UpsertDepartment(ctx, dept); err != nil {
		c.markFailure()
		s.log.Warn("sync department failed",
			zap.String("external_id", in.ExternalID), zap.Error(err))
		return nil // 单条失败不中断整批，否则一条脏数据会阻塞全部同步
	}
	c.markSuccess()
	return nil
}

func (s *SyncService) upsertDoctor(ctx context.Context, in mockhis.Doctor, c *counters) error {
	departmentID, err := s.sync.FindDepartmentIDByCode(ctx, in.DepartmentCode)
	if err != nil {
		c.markFailure()
		s.log.Warn("sync doctor failed: department missing",
			zap.String("external_id", in.ExternalID),
			zap.String("department_code", in.DepartmentCode), zap.Error(err))
		return nil
	}

	phoneEnc, err := s.cipher.Encrypt(in.Phone)
	if err != nil {
		c.markFailure()
		s.log.Warn("sync doctor failed: encrypt phone",
			zap.String("external_id", in.ExternalID), zap.Error(err))
		return nil
	}

	// HIS 不会提供口令，这里生成一个随机口令的散列：
	// 同步来的账号在医生自行重置口令前无法登录。
	// 比给一个固定默认口令安全得多——默认口令是最常见的入侵入口。
	randomPassword, err := secret.RandomToken()
	if err != nil {
		c.markFailure()
		s.log.Warn("sync doctor failed: generate password", zap.Error(err))
		return nil
	}
	passwordHash, err := secret.HashPassword(randomPassword, secret.MinBcryptCost)
	if err != nil {
		c.markFailure()
		s.log.Warn("sync doctor failed: hash password", zap.Error(err))
		return nil
	}

	_, err = s.sync.UpsertDoctor(ctx, repository.SyncDoctorInput{
		ExternalID:   in.ExternalID,
		Name:         in.Name,
		PhoneEnc:     phoneEnc,
		PhoneHash:    secret.Digest(in.Phone, s.phonePepper),
		PasswordHash: passwordHash,
		Title:        in.Title,
		Specialty:    in.Specialty,
		ConsultFee:   in.ConsultFee,
	}, departmentID, in.IsPrimary)
	if err != nil {
		c.markFailure()
		s.log.Warn("sync doctor failed",
			zap.String("external_id", in.ExternalID), zap.Error(err))
		return nil
	}

	c.markSuccess()
	return nil
}

func (s *SyncService) upsertSchedule(ctx context.Context, in mockhis.Schedule, c *counters) error {
	doctorID, err := s.sync.FindDoctorIDByExternalID(ctx, in.DoctorExternal)
	if err != nil {
		c.markFailure()
		s.log.Warn("sync schedule failed: doctor missing",
			zap.String("external_id", in.ExternalID),
			zap.String("doctor", in.DoctorExternal), zap.Error(err))
		return nil
	}

	departmentID, err := s.sync.FindDepartmentIDByCode(ctx, in.DepartmentCode)
	if err != nil {
		c.markFailure()
		s.log.Warn("sync schedule failed: department missing",
			zap.String("external_id", in.ExternalID), zap.Error(err))
		return nil
	}

	schedule := model.Schedule{
		DoctorID:     doctorID,
		DepartmentID: departmentID,
		SchedDate:    in.ScheduleDate(time.Now()),
		TimeSlot:     in.TimeSlot,
		Total:        in.Total,
	}
	if _, err := s.sync.UpsertSchedule(ctx, schedule, in.ExternalID); err != nil {
		c.markFailure()
		s.log.Warn("sync schedule failed",
			zap.String("external_id", in.ExternalID), zap.Error(err))
		return nil
	}

	c.markSuccess()
	return nil
}

// ListLogs 游标分页返回同步记录。
func (s *SyncService) ListLogs(
	ctx context.Context, source, cursorRaw string, size int,
) (model.CursorPage[model.SyncLogView], error) {
	var empty model.CursorPage[model.SyncLogView]

	cursor, err := parseCursor(cursorRaw)
	if err != nil {
		return empty, err
	}
	limit := model.NormalizePageSize(size)

	rows, err := s.logs.ListByCursor(ctx, source, cursor, limit)
	if err != nil {
		return empty, apperr.Wrap(errcode.ErrInternal, "查询同步记录失败", err)
	}
	return model.NewCursorPage(rows, limit, model.SyncLogView.GetID), nil
}

func (s *SyncService) logsView(ctx context.Context, logID uint64) (*model.SyncLogView, error) {
	view, err := s.logs.FindViewByID(ctx, logID)
	if err != nil {
		return nil, apperr.Wrap(errcode.ErrInternal, "读取同步记录失败", err)
	}
	return view, nil
}

// statusOf 由成功/失败数推断同步状态。
func statusOf(c *counters) string {
	succeeded, failed := c.succeeded.Load(), c.failed.Load()
	switch {
	case failed == 0:
		return model.SyncStatusSuccess
	case succeeded == 0:
		return model.SyncStatusFailed
	default:
		return model.SyncStatusPartial
	}
}
