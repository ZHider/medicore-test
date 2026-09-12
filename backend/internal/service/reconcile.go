package service

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"

	"github.com/medicore/backend/internal/apperr"
	"github.com/medicore/backend/internal/errcode"
	"github.com/medicore/backend/internal/metrics"
	"github.com/medicore/backend/internal/model"
)

// reconcileBatchSize 是单批扫描的排班数量。
const reconcileBatchSize = 500

const (
	reconcileResultCorrected = "corrected"
	reconcileResultSkipped   = "skipped"
	reconcileResultError     = "error"
)

var stockReconcileTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "stock_reconcile_total",
	Help: "号源对账结果计数。corrected 一旦上涨，说明 Redis 与 MySQL 已出现偏差并被修正。",
}, []string{"result"})

func init() {
	metrics.Register(stockReconcileTotal)
}

// ReconcileReport 是对账结果。
type ReconcileReport struct {
	// Scanned 扫描到的排班数。
	Scanned int
	// Matched 无需处理的排班数（键不存在，或两边的值本就一致）。
	Matched int
	// Corrected 实际修正的排班数。
	Corrected int
	// Skipped 因值被并发修改而放弃修正的数量，交给下一轮。
	Skipped int
	// Errors 读取/写入 Redis 失败的数量。
	Errors int
}

// ReconcileStock 以 MySQL 为准校准 Redis 号源。
//
// 为什么需要它：抢号是"先扣 Redis 再写 MySQL"。若进程在两步之间崩溃、
// 或失败后回补 Redis 也失败，两边就会产生偏差。
// 偏差方向通常是 Redis <= MySQL（少放了号），表现为"明明有余号却抢不到"；
// 反向偏差（Redis > MySQL）则是超卖风险。本任务两个方向都能发现并修正。
//
// 键不存在的排班直接跳过：说明尚未预热或已自然过期，下次抢号会从数据库重新加载。
func (s *AppointmentService) ReconcileStock(ctx context.Context) (*ReconcileReport, error) {
	report := &ReconcileReport{}
	from := time.Now()
	var afterID uint64

	for {
		// 超时或取消时返回已完成的部分，不把"没跑完"当成失败
		if ctx.Err() != nil {
			return report, nil
		}

		batch, err := s.schedules.ListReconcilable(ctx, from, afterID, reconcileBatchSize)
		if err != nil {
			return report, apperr.Wrap(errcode.ErrInternal, "扫描排班失败", err)
		}
		if len(batch) == 0 {
			return report, nil
		}

		for i := range batch {
			s.reconcileOne(ctx, &batch[i], report)
		}

		afterID = batch[len(batch)-1].ID
		if len(batch) < reconcileBatchSize {
			return report, nil
		}
	}
}

func (s *AppointmentService) reconcileOne(
	ctx context.Context, sched *model.Schedule, report *ReconcileReport,
) {
	report.Scanned++

	current, exists, err := s.stock.Current(ctx, sched.ID)
	if err != nil {
		report.Errors++
		stockReconcileTotal.WithLabelValues(reconcileResultError).Inc()
		s.log.Warn("reconcile: read redis stock failed",
			zap.Uint64("schedule_id", sched.ID), zap.Error(err))
		return
	}

	// 键不存在（未预热/已过期）或两边本就一致：无需处理
	if !exists || current == sched.Remaining {
		report.Matched++
		return
	}

	// 只在值未被并发修改时才修正。
	// 对账读取 MySQL 与写入 Redis 之间存在时间窗，无条件覆盖会把并发扣减的号源还回去。
	ok, err := s.stock.CompareAndSet(ctx, sched.ID, current, sched.Remaining)
	if err != nil {
		report.Errors++
		stockReconcileTotal.WithLabelValues(reconcileResultError).Inc()
		s.log.Warn("reconcile: compare and set failed",
			zap.Uint64("schedule_id", sched.ID), zap.Error(err))
		return
	}
	if !ok {
		report.Skipped++
		stockReconcileTotal.WithLabelValues(reconcileResultSkipped).Inc()
		return
	}

	report.Corrected++
	stockReconcileTotal.WithLabelValues(reconcileResultCorrected).Inc()

	// 用 Warn 而非 Info：这是异常事件，应当能被告警规则直接捕获。
	s.log.Warn("reconcile: redis stock corrected",
		zap.Uint64("schedule_id", sched.ID),
		zap.Int("redis_before", current),
		zap.Int("mysql_authoritative", sched.Remaining),
		zap.Int("diff", sched.Remaining-current))
}
