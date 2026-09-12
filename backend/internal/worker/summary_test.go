package worker

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// --- 内存实现 ---

type fakeQueue struct {
	mu sync.Mutex

	groupErr error

	// readResults 按顺序返回；用完后返回空
	readResults [][]redis.XMessage
	readErr     error
	readCalls   int

	claimResults [][]redis.XMessage
	claimErr     error
	claimCalls   int

	deliveryCounts map[string]int64

	acked       []string
	deadLetters []struct {
		ReportID uint64
		Reason   string
	}
}

func newFakeQueue() *fakeQueue {
	return &fakeQueue{deliveryCounts: map[string]int64{}}
}

func (f *fakeQueue) EnsureGroup(context.Context) error { return f.groupErr }

func (f *fakeQueue) Read(ctx context.Context, _ string, _ int, _ time.Duration) ([]redis.XMessage, error) {
	f.mu.Lock()
	var msgs []redis.XMessage
	if f.readCalls < len(f.readResults) {
		msgs = f.readResults[f.readCalls]
	}
	f.readCalls++
	err := f.readErr
	f.mu.Unlock()

	if err != nil {
		return nil, err
	}
	if len(msgs) > 0 {
		return msgs, nil
	}
	// 无消息时短暂等待，避免循环空转烧 CPU
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(5 * time.Millisecond):
		return nil, nil
	}
}

func (f *fakeQueue) Ack(_ context.Context, messageID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acked = append(f.acked, messageID)
	return nil
}

func (f *fakeQueue) ClaimStale(context.Context, string, time.Duration, int) ([]redis.XMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.claimErr != nil {
		return nil, f.claimErr
	}
	if f.claimCalls < len(f.claimResults) {
		msgs := f.claimResults[f.claimCalls]
		f.claimCalls++
		return msgs, nil
	}
	f.claimCalls++
	return nil, nil
}

func (f *fakeQueue) DeadLetter(_ context.Context, reportID uint64, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deadLetters = append(f.deadLetters, struct {
		ReportID uint64
		Reason   string
	}{reportID, reason})
	return nil
}

func (f *fakeQueue) DeliveryCount(_ context.Context, messageID string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if n, ok := f.deliveryCounts[messageID]; ok {
		return n, nil
	}
	return 1, nil
}

func (f *fakeQueue) ackCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.acked)
}

func (f *fakeQueue) deadLetterCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.deadLetters)
}

type fakeSummarizer struct {
	mu sync.Mutex

	summarizeErr error
	calls        []uint64
	failed       []struct {
		ReportID uint64
		Reason   string
	}
}

func (f *fakeSummarizer) SummarizeReport(_ context.Context, reportID uint64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, reportID)
	return f.summarizeErr
}

func (f *fakeSummarizer) FailReport(_ context.Context, reportID uint64, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failed = append(f.failed, struct {
		ReportID uint64
		Reason   string
	}{reportID, reason})
	return nil
}

func (f *fakeSummarizer) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeSummarizer) failCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.failed)
}

func newTestWorker(q *fakeQueue, s *fakeSummarizer) *SummaryWorker {
	return NewSummaryWorker(q, s, zap.NewNop())
}

func msg(id string, reportID any) redis.XMessage {
	return redis.XMessage{ID: id, Values: map[string]any{"report_id": reportID}}
}

// --- handle：成功路径 ---

func TestHandle_SuccessAcks(t *testing.T) {
	q, s := newFakeQueue(), &fakeSummarizer{}
	w := newTestWorker(q, s)

	w.handle(msg("1-0", uint64(42)))

	assert.Equal(t, 1, s.callCount())
	assert.Equal(t, 1, q.ackCount(), "处理成功必须确认，否则消息会一直留在 pending")
	assert.Zero(t, q.deadLetterCount())
}

// 消息体损坏时重投也不会变好，必须确认丢弃，否则会永久堵住队列。
func TestHandle_MalformedMessageIsDropped(t *testing.T) {
	q, s := newFakeQueue(), &fakeSummarizer{}
	w := newTestWorker(q, s)

	w.handle(redis.XMessage{ID: "1-0", Values: map[string]any{"wrong_field": "x"}})

	assert.Zero(t, s.callCount(), "消息体损坏时不应调用业务逻辑")
	assert.Equal(t, 1, q.ackCount(), "必须确认并丢弃，避免堵塞队列")
}

func TestHandle_NonNumericReportIDIsDropped(t *testing.T) {
	q, s := newFakeQueue(), &fakeSummarizer{}
	w := newTestWorker(q, s)

	w.handle(msg("1-0", "not-a-number"))

	assert.Zero(t, s.callCount())
	assert.Equal(t, 1, q.ackCount())
}

// --- handle：失败与重试 ---

// 未超限的失败**不能确认**：留在 pending 里等待 XAUTOCLAIM 重新认领。
func TestHandle_FailureBelowMaxAttemptsIsNotAcked(t *testing.T) {
	q, s := newFakeQueue(), &fakeSummarizer{summarizeErr: errors.New("db down")}
	q.deliveryCounts["1-0"] = 1
	w := newTestWorker(q, s)

	w.handle(msg("1-0", uint64(42)))

	assert.Equal(t, 1, s.callCount())
	assert.Zero(t, q.ackCount(), "未超限的失败不能确认，否则任务就丢了")
	assert.Zero(t, q.deadLetterCount())
}

// 重试超限：转死信 + 标记报告失败 + 确认，三者缺一不可。
func TestHandle_ExceededMaxAttemptsGoesToDeadLetter(t *testing.T) {
	q, s := newFakeQueue(), &fakeSummarizer{summarizeErr: errors.New("db down")}
	q.deliveryCounts["1-0"] = maxAttempts
	w := newTestWorker(q, s)

	w.handle(msg("1-0", uint64(42)))

	assert.Equal(t, 1, q.deadLetterCount(), "超限任务应转入死信队列")
	assert.Equal(t, 1, q.ackCount(), "确认后消息不会永远卡在 pending")
	assert.Equal(t, 1, s.failCount(), "报告不能永远停在 processing，必须标记失败")

	q.mu.Lock()
	defer q.mu.Unlock()
	assert.Equal(t, uint64(42), q.deadLetters[0].ReportID)
	assert.NotEmpty(t, q.deadLetters[0].Reason)
}

// 取不到投递次数时保守按 1 次处理，避免误判超限而把任务直接丢进死信。
func TestHandle_UnknownDeliveryCountDoesNotDeadLetter(t *testing.T) {
	q, s := newFakeQueue(), &fakeSummarizer{summarizeErr: errors.New("db down")}
	// deliveryCounts 为空 -> DeliveryCount 返回 1
	w := newTestWorker(q, s)

	w.handle(msg("1-0", uint64(42)))

	assert.Zero(t, q.deadLetterCount())
	assert.Zero(t, q.ackCount())
}

// --- claimStale ---

// 启动时与周期性地认领未确认消息，是"至少一次投递"的关键。
// 没有它，崩溃时停在 pending 的任务永远没人处理。
func TestClaimStale_ProcessesClaimedMessages(t *testing.T) {
	q, s := newFakeQueue(), &fakeSummarizer{}
	q.claimResults = [][]redis.XMessage{
		{msg("9-0", uint64(7)), msg("9-1", uint64(8))},
	}
	w := newTestWorker(q, s)

	w.claimStale()

	assert.Equal(t, 2, s.callCount())
	assert.Equal(t, 2, q.ackCount())
}

func TestClaimStale_ErrorDoesNotPanic(t *testing.T) {
	q, s := newFakeQueue(), &fakeSummarizer{}
	q.claimErr = errors.New("redis down")
	w := newTestWorker(q, s)

	assert.NotPanics(t, w.claimStale)
	assert.Zero(t, s.callCount())
}

// --- Start / Stop ---

func TestStart_PropagatesGroupCreationError(t *testing.T) {
	q := newFakeQueue()
	q.groupErr = errors.New("redis down")
	w := newTestWorker(q, &fakeSummarizer{})

	assert.Error(t, w.Start())
}

func TestStartStop_IsCleanAndIdempotent(t *testing.T) {
	q := newFakeQueue()
	w := newTestWorker(q, &fakeSummarizer{})

	require.NoError(t, w.Start())

	// 给循环一点时间跑起来
	time.Sleep(20 * time.Millisecond)

	done := make(chan struct{})
	go func() {
		w.Stop()
		w.Stop() // 重复 Stop 不应 panic
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop 未能及时返回，可能存在 goroutine 泄漏")
	}
}

// 消费循环应当真正把队列里的任务处理掉，而不是只启动不干活。
func TestLoop_ConsumesQueuedMessages(t *testing.T) {
	q, s := newFakeQueue(), &fakeSummarizer{}
	q.readResults = [][]redis.XMessage{{msg("1-0", uint64(1))}}
	w := newTestWorker(q, s)

	require.NoError(t, w.Start())
	defer w.Stop()

	deadline := time.After(2 * time.Second)
	for {
		if s.callCount() > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("消费循环没有处理队列中的任务")
		case <-time.After(10 * time.Millisecond):
		}
	}
	assert.Equal(t, 1, q.ackCount())
}
