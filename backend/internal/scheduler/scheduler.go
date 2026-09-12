// Package scheduler 承载进程内定时任务（spec §1.3：用 robfig/cron 而非 xxl-job）。
package scheduler

import (
	"context"
	"time"

	"github.com/robfig/cron/v3"
	"go.uber.org/zap"

	"github.com/medicore/backend/internal/service"
)

// expireScanSpec 是超时订单扫描周期。
// 每分钟一次：确认时限是 15 分钟，1 分钟的释放延迟对号源周转影响可忽略，
// 又不会让扫描本身成为数据库负担。
const expireScanSpec = "@every 1m"

// jobTimeout 限制单次任务的最长执行时间，避免上一轮未结束、下一轮又启动。
const jobTimeout = 30 * time.Second

// Scheduler 封装 cron 实例。
type Scheduler struct {
	cron  *cron.Cron
	log   *zap.Logger
	appts *service.AppointmentService
}

// New 构造 Scheduler 并注册任务（尚未启动）。
func New(log *zap.Logger, appts *service.AppointmentService) *Scheduler {
	if log == nil {
		log = zap.NewNop()
	}

	return &Scheduler{
		// WithLogger 让 cron 的内部日志也走结构化日志，
		// 否则它默认用标准库 log，会污染 JSON 日志流。
		cron:  cron.New(cron.WithLogger(cronLogAdapter{log})),
		log:   log,
		appts: appts,
	}
}

// Start 启动调度并注册超时释放任务。
func (s *Scheduler) Start() error {
	_, err := s.cron.AddFunc(expireScanSpec, s.releaseExpired)
	if err != nil {
		return err
	}
	s.cron.Start()
	s.log.Info("scheduler started", zap.String("expire_scan", expireScanSpec))
	return nil
}

// Stop 停止调度并等待正在执行的任务结束。
func (s *Scheduler) Stop() {
	if s.cron == nil {
		return
	}
	ctx := s.cron.Stop()
	select {
	case <-ctx.Done():
	case <-time.After(jobTimeout):
		s.log.Warn("scheduler stop timed out")
	}
	s.log.Info("scheduler stopped")
}

func (s *Scheduler) releaseExpired() {
	ctx, cancel := context.WithTimeout(context.Background(), jobTimeout)
	defer cancel()

	released, err := s.appts.ReleaseExpired(ctx)
	if err != nil {
		s.log.Error("release expired appointments failed", zap.Error(err))
		return
	}
	if released > 0 {
		s.log.Info("released expired appointments", zap.Int("count", released))
	}
}

// cronLogAdapter 把 cron 的日志转发到 zap。
type cronLogAdapter struct {
	log *zap.Logger
}

func (a cronLogAdapter) Info(msg string, keysAndValues ...any) {
	a.log.Info("cron: "+msg, toZapFields(keysAndValues)...)
}

func (a cronLogAdapter) Error(err error, msg string, keysAndValues ...any) {
	fields := append(toZapFields(keysAndValues), zap.Error(err))
	a.log.Error("cron: "+msg, fields...)
}

func toZapFields(keysAndValues []any) []zap.Field {
	fields := make([]zap.Field, 0, len(keysAndValues)/2)
	for i := 0; i+1 < len(keysAndValues); i += 2 {
		key, ok := keysAndValues[i].(string)
		if !ok {
			key = "key"
		}
		fields = append(fields, zap.Any(key, keysAndValues[i+1]))
	}
	return fields
}
