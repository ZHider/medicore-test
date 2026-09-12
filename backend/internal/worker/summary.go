// Package worker 承载 Redis Stream 的消费者。
package worker

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/medicore/backend/internal/metrics"
	"github.com/medicore/backend/internal/repository"
)

// Worker 运行参数。
const (
	// readBatch 单次最多取几条任务。
	readBatch = 4
	// blockDuration 是 XREADGROUP 的阻塞等待时长。有它才能在有新任务时立即醒来，
	// 而不是靠轮询空转。
	blockDuration = 2 * time.Second
	// claimMinIdle 超过这个空闲时间的未确认消息会被重新认领（XAUTOCLAIM）。
	claimMinIdle = 30 * time.Second
	// claimBatch 单次认领上限。
	claimBatch = 10
	// jobTimeout 单条任务的处理上限。
	jobTimeout = 60 * time.Second
	// maxAttempts 单条任务的最大投递次数，超过转入死信。
	maxAttempts = 3
	// backoffOnError 读取队列出错时的退避，避免 Redis 异常时空转刷日志。
	backoffOnError = time.Second
	// shutdownTimeout 等待当前任务结束的上限。
	shutdownTimeout = 10 * time.Second
)

// 任务处理结果标签。
const (
	taskResultDone       = "done"
	taskResultRetry      = "retry"
	taskResultDeadLetter = "dead_letter"
	taskResultMalformed  = "malformed"
)

var summaryTasksTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "summary_tasks_total",
	Help: "预问诊总结任务处理结果计数。dead_letter 与 malformed 上涨都说明有问题需要排查。",
}, []string{"result"})

func init() {
	metrics.Register(summaryTasksTotal)
}

// summaryQueue 是 worker 所需的队列能力。
type summaryQueue interface {
	EnsureGroup(ctx context.Context) error
	Read(ctx context.Context, consumer string, count int, block time.Duration) ([]redis.XMessage, error)
	Ack(ctx context.Context, messageID string) error
	ClaimStale(ctx context.Context, consumer string, minIdle time.Duration, count int) ([]redis.XMessage, error)
	DeadLetter(ctx context.Context, reportID uint64, reason string) error
	DeliveryCount(ctx context.Context, messageID string) (int64, error)
}

// summarizer 是 worker 所需的服务能力。
type summarizer interface {
	SummarizeReport(ctx context.Context, reportID uint64) error
	FailReport(ctx context.Context, reportID uint64, reason string) error
}

// SummaryWorker 消费预问诊总结任务。
type SummaryWorker struct {
	queue    summaryQueue
	svc      summarizer
	log      *zap.Logger
	consumer string

	stopOnce sync.Once
	stop     chan struct{}
	wg       sync.WaitGroup
}

// NewSummaryWorker 构造 SummaryWorker。
//
// 消费者名带主机名与 PID：多实例部署时能从 XPENDING 里区分
// 是哪个进程留下的未确认消息，排查崩溃问题时很关键。
func NewSummaryWorker(queue summaryQueue, svc summarizer, log *zap.Logger) *SummaryWorker {
	if log == nil {
		log = zap.NewNop()
	}
	host, err := os.Hostname()
	if err != nil {
		host = "unknown"
	}

	return &SummaryWorker{
		queue:    queue,
		svc:      svc,
		log:      log,
		consumer: fmt.Sprintf("%s-%d", host, os.Getpid()),
		stop:     make(chan struct{}),
	}
}

// Start 创建消费者组并启动消费循环。
func (w *SummaryWorker) Start() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := w.queue.EnsureGroup(ctx); err != nil {
		return err
	}

	w.wg.Add(1)
	go w.loop()

	w.log.Info("summary worker started",
		zap.String("consumer", w.consumer),
		zap.String("stream", repository.SummaryStreamKey),
		zap.String("group", repository.SummaryGroup))
	return nil
}

// Stop 停止消费并等待当前任务结束。
func (w *SummaryWorker) Stop() {
	w.stopOnce.Do(func() { close(w.stop) })

	done := make(chan struct{})
	go func() {
		w.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(shutdownTimeout):
		w.log.Warn("summary worker stop timed out; some tasks may still be running")
	}
	w.log.Info("summary worker stopped")
}

func (w *SummaryWorker) loop() {
	defer w.wg.Done()

	// 启动时先认领上次崩溃遗留的未确认消息，
	// 否则那些任务会一直躺在 pending 里没人处理。
	w.claimStale()

	ticker := time.NewTicker(claimMinIdle)
	defer ticker.Stop()

	for {
		select {
		case <-w.stop:
			return
		case <-ticker.C:
			w.claimStale()
		default:
		}

		ctx, cancel := context.WithTimeout(context.Background(), blockDuration+5*time.Second)
		msgs, err := w.queue.Read(ctx, w.consumer, readBatch, blockDuration)
		cancel()

		if err != nil {
			// 消费者组消失（Redis 被 flush / 重启未持久化 / 误操作）时必须重建，
			// 否则这个循环会永远报同一个错，所有总结任务静默卡死。
			if repository.IsNoGroupError(err) {
				w.log.Warn("summary consumer group missing, recreating")
				if gErr := w.queue.EnsureGroup(ctx); gErr != nil {
					w.log.Error("recreate summary consumer group failed", zap.Error(gErr))
					w.sleep(backoffOnError)
				}
				continue
			}

			// 队列不可用：退避后重试，不能让循环空转刷日志
			w.log.Error("read summary stream failed", zap.Error(err))
			w.sleep(backoffOnError)
			continue
		}

		for _, msg := range msgs {
			w.handle(msg)
		}
	}
}

// handle 处理单条任务，并决定确认、重试还是转入死信。
func (w *SummaryWorker) handle(msg redis.XMessage) {
	reportID, err := repository.ParseReportID(msg)
	if err != nil {
		// 消息体损坏：重投也不会变好，确认丢弃并留痕，避免堵住队列
		w.log.Error("dropping malformed summary task",
			zap.String("message_id", msg.ID), zap.Error(err))
		summaryTasksTotal.WithLabelValues(taskResultMalformed).Inc()
		w.ack(msg.ID)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), jobTimeout)
	err = w.svc.SummarizeReport(ctx, reportID)
	cancel()

	if err == nil {
		summaryTasksTotal.WithLabelValues(taskResultDone).Inc()
		w.ack(msg.ID)
		return
	}

	// 处理失败：不确认，让消息留在 pending 等待重新认领。
	attempts := w.deliveryCount(msg.ID)
	if attempts < maxAttempts {
		summaryTasksTotal.WithLabelValues(taskResultRetry).Inc()
		w.log.Warn("summary task failed, leaving for retry",
			zap.Uint64("report_id", reportID),
			zap.Int64("attempt", attempts),
			zap.Int64("max_attempts", maxAttempts),
			zap.Error(err))
		return
	}

	// 重试超限：转死信并确认，否则这条消息会永远卡在 pending 里。
	summaryTasksTotal.WithLabelValues(taskResultDeadLetter).Inc()
	w.log.Error("summary task exceeded max attempts, moving to dead letter",
		zap.Uint64("report_id", reportID),
		zap.Int64("attempts", attempts),
		zap.Error(err))

	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if dlErr := w.queue.DeadLetter(ctx, reportID, err.Error()); dlErr != nil {
		w.log.Error("dead letter summary task failed",
			zap.Uint64("report_id", reportID), zap.Error(dlErr))
	}

	// 报告不能永远停在 processing——那对客户端和"正在处理"无法区分
	if fErr := w.svc.FailReport(ctx, reportID, "多次处理失败："+err.Error()); fErr != nil {
		w.log.Error("mark report failed after dead letter",
			zap.Uint64("report_id", reportID), zap.Error(fErr))
	}

	w.ack(msg.ID)
}

func (w *SummaryWorker) claimStale() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	msgs, err := w.queue.ClaimStale(ctx, w.consumer, claimMinIdle, claimBatch)
	if err != nil {
		w.log.Error("claim stale summary tasks failed", zap.Error(err))
		return
	}
	if len(msgs) == 0 {
		return
	}

	w.log.Info("claimed stale summary tasks", zap.Int("count", len(msgs)))
	for _, msg := range msgs {
		w.handle(msg)
	}
}

func (w *SummaryWorker) ack(messageID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := w.queue.Ack(ctx, messageID); err != nil {
		w.log.Error("ack summary task failed",
			zap.String("message_id", messageID), zap.Error(err))
	}
}

func (w *SummaryWorker) deliveryCount(messageID string) int64 {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	attempts, err := w.queue.DeliveryCount(ctx, messageID)
	if err != nil {
		// 取不到投递次数时保守按 1 次处理，避免误判为超限而直接丢进死信
		w.log.Warn("read delivery count failed", zap.String("message_id", messageID), zap.Error(err))
		return 1
	}
	return attempts
}

// sleep 可被 Stop 打断的等待。
func (w *SummaryWorker) sleep(d time.Duration) {
	select {
	case <-w.stop:
	case <-time.After(d):
	}
}
