package service

import (
	"context"
	"fmt"
	"sync/atomic"

	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"

	"github.com/medicore/backend/internal/apperr"
	"github.com/medicore/backend/internal/errcode"
	"github.com/medicore/backend/internal/his"
	"github.com/medicore/backend/internal/model"
	"github.com/medicore/backend/internal/repository"
	"github.com/medicore/backend/internal/secret"
	"github.com/medicore/backend/internal/traceid"
)

// SourceName 是模拟 HIS 数据源的名字，写入 sync_logs.source。
const SourceName = "mockhis"

// defaultSyncConcurrency 是同步的并发上限（spec §7.3 要求限并发 5）。
//
// 限并发是必要的：外部系统往往有连接数或 QPS 限制，
// 一次性打出全部请求容易被限流甚至封禁。
//
// 用常量而不是配置项：spec 把值定死为 5，且没有任何调用方设过它
// ——保留一个"谁都不设"的旋钮只会让人以为它可调。
const defaultSyncConcurrency = 5

// SyncSource 是外部数据源。按本项目惯例由使用方声明接口，
// 因此这里依赖的是 his 契约包而不是任何具体实现
// （曾经的签名直接返回 *mockhis.Dataset，导致 service 反过来依赖 mock 包）。
type SyncSource interface {
	Load(ctx context.Context) (*his.Dataset, error)
}

// SyncDeps 是 SyncService 的依赖集合。
type SyncDeps struct {
	Source      SyncSource
	Sync        syncStore
	Logs        syncLogStore
	Cipher      *secret.Cipher
	PhonePepper string
	Log         *zap.Logger
}

// SyncService 承载 HIS 数据同步（spec §7.3）。
type SyncService struct {
	source      SyncSource
	sync        syncStore
	logs        syncLogStore
	cipher      *secret.Cipher
	phonePepper string
	log         *zap.Logger
}

// NewSyncService 构造 SyncService。
func NewSyncService(d SyncDeps) *SyncService {
	log := d.Log
	if log == nil {
		log = zap.NewNop()
	}

	return &SyncService{
		source:      d.Source,
		sync:        d.Sync,
		logs:        d.Logs,
		cipher:      d.Cipher,
		phonePepper: d.PhonePepper,
		log:         log,
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
	// 带上 TraceID：手动触发时它能把 sync_logs 与同一次请求的审计记录串起来，
	// 否则"谁在什么时候手动同步了什么"在两张表之间是断的。
	// cron 触发没有 TraceID，落库为 NULL。
	logID, err := s.logs.Start(ctx, SourceName, trigger, traceid.FromContext(ctx))
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
//
// 唯一可能返回 error 的地方是数据源本身。单条记录失败**只计数、不中断整批**
// （见各 upsert 的说明），所以三个阶段的调用都不需要错误通道。
//
// 分三阶段而非一把梭：排班依赖医生、医生依赖科室，
// 若并发处理全部实体，排班会因为医生还没落库而大量失败。
func (s *SyncService) run(ctx context.Context) (*counters, error) {
	dataset, err := s.source.Load(ctx)
	if err != nil {
		return &counters{}, fmt.Errorf("加载数据源: %w", err)
	}

	c := &counters{}

	// 阶段 1：科室
	s.runPhase(ctx, len(dataset.Departments), func(ctx context.Context, i int) {
		s.upsertDepartment(ctx, dataset.Departments[i], c)
	})

	// 阶段 2：医生（依赖科室）
	s.runPhase(ctx, len(dataset.Doctors), func(ctx context.Context, i int) {
		s.upsertDoctor(ctx, dataset.Doctors[i], c)
	})

	// 阶段 3：排班（依赖医生与科室）
	s.runPhase(ctx, len(dataset.Schedules), func(ctx context.Context, i int) {
		s.upsertSchedule(ctx, dataset.Schedules[i], c)
	})

	return c, nil
}

// runPhase 以受限并发执行一个阶段。
//
// 用 errgroup 只是为了 SetLimit 这道并发闸门。这里**没有错误通道**是有意的：
// 每个 work 都不返回错误（单条失败只计数），因此原先的
// `if err := g.Wait(); err != nil` 分支永远不可达，读起来却像"失败会中止整批"。
// 父 context 被取消时 errgroup 会立刻结束 Wait，下面的循环也随之 break。
func (s *SyncService) runPhase(ctx context.Context, total int, work func(ctx context.Context, i int)) {
	g, gCtx := errgroup.WithContext(ctx)
	g.SetLimit(defaultSyncConcurrency)

	for i := 0; i < total; i++ {
		if gCtx.Err() != nil {
			break
		}
		g.Go(func() error {
			work(gCtx, i)
			return nil
		})
	}

	_ = g.Wait()
}

func (s *SyncService) upsertDepartment(ctx context.Context, in his.Department, c *counters) {
	dept := model.Department{
		ExternalID: &in.ExternalID,
		Code:       in.Code,
		Name:       in.Name,
		SortOrder:  in.SortOrder,
		Status:     model.StatusActive,
	}
	dept.Description = optionalTrimmedString(in.Description)

	if err := s.sync.UpsertDepartment(ctx, dept); err != nil {
		c.markFailure()
		s.log.Warn("sync department failed",
			zap.String("external_id", in.ExternalID), zap.Error(err))
		return // 单条失败不中断整批，否则一条脏数据会阻塞全部同步
	}
	c.markSuccess()
}

func (s *SyncService) upsertDoctor(ctx context.Context, in his.Doctor, c *counters) {
	departmentID, err := s.sync.FindDepartmentIDByCode(ctx, in.DepartmentCode)
	if err != nil {
		c.markFailure()
		s.log.Warn("sync doctor failed: department missing",
			zap.String("external_id", in.ExternalID),
			zap.String("department_code", in.DepartmentCode), zap.Error(err))
		return
	}

	phoneEnc, err := s.cipher.Encrypt(in.Phone)
	if err != nil {
		c.markFailure()
		s.log.Warn("sync doctor failed: encrypt phone",
			zap.String("external_id", in.ExternalID), zap.Error(err))
		return
	}

	// HIS 不会提供口令，这里生成一个随机口令的散列：
	// 同步来的账号在医生自行重置口令前无法登录。
	// 比给一个固定默认口令安全得多——默认口令是最常见的入侵入口。
	randomPassword, err := secret.RandomToken()
	if err != nil {
		c.markFailure()
		s.log.Warn("sync doctor failed: generate password", zap.Error(err))
		return
	}
	passwordHash, err := secret.HashPassword(randomPassword, secret.MinBcryptCost)
	if err != nil {
		c.markFailure()
		s.log.Warn("sync doctor failed: hash password", zap.Error(err))
		return
	}

	err = s.sync.UpsertDoctor(ctx, repository.SyncDoctorInput{
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
		return
	}

	c.markSuccess()
}

func (s *SyncService) upsertSchedule(ctx context.Context, in his.Schedule, c *counters) {
	doctorID, err := s.sync.FindDoctorIDByExternalID(ctx, in.DoctorExternal)
	if err != nil {
		c.markFailure()
		s.log.Warn("sync schedule failed: doctor missing",
			zap.String("external_id", in.ExternalID),
			zap.String("doctor", in.DoctorExternal), zap.Error(err))
		return
	}

	departmentID, err := s.sync.FindDepartmentIDByCode(ctx, in.DepartmentCode)
	if err != nil {
		c.markFailure()
		s.log.Warn("sync schedule failed: department missing",
			zap.String("external_id", in.ExternalID), zap.Error(err))
		return
	}

	schedule := model.Schedule{
		DoctorID:     doctorID,
		DepartmentID: departmentID,
		SchedDate:    in.SchedDate,
		TimeSlot:     in.TimeSlot,
		Total:        in.Total,
	}
	if err := s.sync.UpsertSchedule(ctx, schedule, in.ExternalID); err != nil {
		c.markFailure()
		s.log.Warn("sync schedule failed",
			zap.String("external_id", in.ExternalID), zap.Error(err))
		return
	}

	c.markSuccess()
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
