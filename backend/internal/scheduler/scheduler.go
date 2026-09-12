// Package scheduler 承载进程内定时任务（spec §1.3：用 robfig/cron 而非 xxl-job）。
package scheduler

import (
	"context"
	"fmt"
	"time"

	"github.com/robfig/cron/v3"
	"go.uber.org/zap"

	"github.com/medicore/backend/internal/service"
)

// 默认调度周期（可由配置覆盖）。
const (
	defaultExpireScanSpec     = "@every 1m"
	defaultStockReconcileSpec = "@every 5m"
)

// jobTimeout 限制单次任务的最长执行时间，避免上一轮未结束、下一轮又启动。
const jobTimeout = 30 * time.Second

// Options 是调度器配置。
type Options struct {
	// ExpireScan 超时订单释放周期，空串用默认值。
	ExpireScan string
	// StockReconcile 号源对账周期，空串用默认值。
	StockReconcile string
}

// Scheduler 封装 cron 实例。
type Scheduler struct {
	cron           *cron.Cron
	log            *zap.Logger
	appts          *service.AppointmentService
	expireScanSpec string
	reconcileSpec  string
}

// New 构造 Scheduler 并注册任务（尚未启动）。
func New(log *zap.Logger, appts *service.AppointmentService, opts Options) *Scheduler {
	if log == nil {
		log = zap.NewNop()
	}
	if opts.ExpireScan == "" {
		opts.ExpireScan = defaultExpireScanSpec
	}
	if opts.StockReconcile == "" {
		opts.StockReconcile = defaultStockReconcileSpec
	}

	return &Scheduler{
		// WithLogger 让 cron 的内部日志也走结构化日志，
		// 否则它默认用标准库 log，会污染 JSON 日志流。
		cron:           cron.New(cron.WithLogger(cronLogAdapter{log})),
		log:            log,
		appts:          appts,
		expireScanSpec: opts.ExpireScan,
		reconcileSpec:  opts.StockReconcile,
	}
}

// Start 启动调度并注册定时任务。
func (s *Scheduler) Start() error {
	if _, err := s.cron.AddFunc(s.expireScanSpec, s.releaseExpired); err != nil {
		return fmt.Errorf("注册超时释放任务（周期 %q）: %w", s.expireScanSpec, err)
	}
	if _, err := s.cron.AddFunc(s.reconcileSpec, s.reconcileStock); err != nil {
		return fmt.Errorf("注册对账任务（周期 %q）: %w", s.reconcileSpec, err)
	}

	s.cron.Start()
	s.log.Info("scheduler started",
		zap.String("expire_scan", s.expireScanSpec),
		zap.String("stock_reconcile", s.reconcileSpec))
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

// reconcileStock 以 MySQL 为准校准 Redis 号源。
func (s *Scheduler) reconcileStock() {
	ctx, cancel := context.WithTimeout(context.Background(), jobTimeout)
	defer cancel()

	report, err := s.appts.ReconcileStock(ctx)
	if err != nil {
		s.log.Error("stock reconcile failed", zap.Error(err))
		return
	}

	// 无偏差时不打日志：5 分钟一次的正常路径不该刷屏。
	// 一旦出现偏差则必须显式记录，且 Corrected 有对应指标可供告警。
	if report.Corrected > 0 || report.Errors > 0 {
		s.log.Warn("stock reconcile finished with drift",
			zap.Int("scanned", report.Scanned),
			zap.Int("corrected", report.Corrected),
			zap.Int("skipped", report.Skipped),
			zap.Int("errors", report.Errors))
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
