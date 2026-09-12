package audit

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/medicore/backend/internal/model"
)

// --- Describe ---

func TestDescribe(t *testing.T) {
	tests := []struct {
		name         string
		fullPath     string
		method       string
		wantResource string
		wantAction   string
	}{
		{"列表查询", "/api/v1/appointments", "GET", "appointments", "view"},
		{"创建", "/api/v1/appointments", "POST", "appointments", "create"},
		{"按 ID 查询", "/api/v1/doctors/:id", "GET", "doctors", "view"},
		{"取消用末段作动作", "/api/v1/appointments/:id/cancel", "POST", "appointments", "cancel"},
		{"签到用末段作动作", "/api/v1/appointments/:id/checkin", "POST", "appointments", "checkin"},
		{"提交总结", "/api/v1/appointments/:id/preconsult/submit", "POST", "appointments", "submit"},
		{"对话", "/api/v1/appointments/:id/preconsult/chat", "POST", "appointments", "chat"},
		{"报告按查看计", "/api/v1/appointments/:id/preconsult/report", "GET", "appointments", "view"},
		{"队列按查看计", "/api/v1/schedules/:id/queue", "GET", "schedules", "view"},
		{"登录", "/api/v1/auth/login", "POST", "auth", "login"},
		{"注册", "/api/v1/auth/register", "POST", "auth", "register"},
		{"登出", "/api/v1/auth/logout", "POST", "auth", "logout"},
		{"审计查询", "/api/v1/admin/audit-logs", "GET", "admin", "view"},
		{"未知末段回退到方法语义", "/api/v1/foo/:id/whatever", "PUT", "foo", "update"},
		{"非 api 路径", "/healthz", "GET", "", "view"},
		{"空路径", "", "POST", "", "create"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resource, action := Describe(tt.fullPath, tt.method)
			assert.Equal(t, tt.wantResource, resource)
			assert.Equal(t, tt.wantAction, action)
		})
	}
}

// 用路由模板而非真实路径，是为了让同一资源的记录能被聚合查询。
// 若用真实路径，/appointments/1 与 /appointments/2 会变成两个不同的资源名。
func TestDescribe_UsesRouteTemplateNotActualPath(t *testing.T) {
	resource, _ := Describe("/api/v1/appointments/:id/cancel", "POST")

	assert.Equal(t, "appointments", resource)
	assert.NotContains(t, resource, ":id")
}

// --- Writer ---

type fakeStore struct {
	mu       sync.Mutex
	batches  [][]model.AuditLog
	err      error
	attempts int
}

func (f *fakeStore) InsertBatch(_ context.Context, entries []model.AuditLog) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attempts++
	if f.err != nil {
		return f.err
	}
	f.batches = append(f.batches, append([]model.AuditLog(nil), entries...))
	return nil
}

func (f *fakeStore) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, b := range f.batches {
		n += len(b)
	}
	return n
}

func (f *fakeStore) batchCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.batches)
}

func (f *fakeStore) setErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func newTestWriter(store Store) *Writer {
	return NewWriter(store, zap.NewNop())
}

func sampleEntry() Entry {
	return Entry{
		TraceID:    "trace-1",
		Method:     "POST",
		Path:       "/api/v1/appointments",
		Resource:   "appointments",
		Action:     "create",
		StatusCode: 200,
		LatencyMS:  3,
		ClientIP:   "127.0.0.1",
	}
}

func TestWriter_FlushesOnStop(t *testing.T) {
	store := &fakeStore{}
	w := newTestWriter(store)
	w.Start()

	for i := 0; i < 5; i++ {
		w.Record(sampleEntry())
	}
	w.Stop()

	assert.Equal(t, 5, store.total(), "Stop 必须把缓冲区里剩余记录全部落库")
}

// Stop 之后再记录必须被忽略：此时后台协程已退出，
// 入队的记录永远不会落库，却会让调用方以为已经记下了。
func TestWriter_IgnoresRecordAfterStop(t *testing.T) {
	store := &fakeStore{}
	w := newTestWriter(store)
	w.Start()

	w.Record(sampleEntry())
	w.Stop()

	w.Record(sampleEntry())
	time.Sleep(20 * time.Millisecond)

	assert.Equal(t, 1, store.total())
}

func TestWriter_BatchesBySize(t *testing.T) {
	store := &fakeStore{}
	w := newTestWriter(store)
	w.Start()
	defer w.Stop()

	for i := 0; i < BatchSize; i++ {
		w.Record(sampleEntry())
	}

	// 达到 BatchSize 应立即刷盘，不必等定时器
	require.Eventually(t, func() bool {
		return store.total() >= BatchSize
	}, time.Second, 10*time.Millisecond)
}

// 低流量时也要及时落库，不能一直等在缓冲区里。
func TestWriter_FlushesOnInterval(t *testing.T) {
	store := &fakeStore{}
	w := newTestWriter(store)
	w.Start()
	defer w.Stop()

	w.Record(sampleEntry())

	require.Eventually(t, func() bool {
		return store.total() == 1
	}, FlushInterval*5, 10*time.Millisecond)
}

// 关键背压保证：缓冲区满时必须丢弃而不是阻塞调用方。
// 否则一次数据库抖动会演变成全站请求堆积。
func TestWriter_NeverBlocksWhenBufferFull(t *testing.T) {
	store := &fakeStore{}
	w := newTestWriter(store)
	// 刻意不 Start：后台协程不消费，缓冲区必然填满

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < BufferSize*3; i++ {
			w.Record(sampleEntry())
		}
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Record 在缓冲区满时阻塞了，这会让数据库故障扩散成全站故障")
	}
}

// 落库失败不能让后台协程退出：审计写不进去是运维问题，
// 但一旦协程退出，后续所有记录都会堆积到缓冲区然后被丢弃。
func TestWriter_SurvivesStoreFailure(t *testing.T) {
	store := &fakeStore{err: errors.New("db down")}
	w := newTestWriter(store)
	w.Start()

	w.Record(sampleEntry())

	// 必须等第一次刷盘真的发生过（失败），否则两条记录会落进同一个成功批次，
	// 测试就变成"什么都验证不到"
	require.Eventually(t, func() bool { return store.attemptCount() >= 1 }, time.Second, 5*time.Millisecond)

	store.setErr(nil)
	w.Record(sampleEntry())
	w.Stop()

	assert.Equal(t, 1, store.total(), "数据库恢复后应能继续写入")
}

func TestWriter_StopIsIdempotent(t *testing.T) {
	store := &fakeStore{}
	w := newTestWriter(store)
	w.Start()

	assert.NotPanics(t, func() {
		w.Stop()
		w.Stop()
	})
}

func TestWriter_StopWithoutStart(t *testing.T) {
	w := newTestWriter(&fakeStore{})

	assert.NotPanics(t, func() { w.Stop() }, "未启动就 Stop 不应 panic")
}

func TestWriter_TruncatesLongUserAgent(t *testing.T) {
	store := &fakeStore{}
	w := newTestWriter(store)
	w.Start()

	entry := sampleEntry()
	entry.UserAgent = string(make([]byte, 1000))
	w.Record(entry)
	w.Stop()

	require.Len(t, store.batches, 1)
	require.Len(t, store.batches[0], 1)
	require.NotNil(t, store.batches[0][0].UserAgent)
	assert.Len(t, *store.batches[0][0].UserAgent, 255, "超长 UA 应被截断，否则会写入失败")
}

// 空字符串统一转成 NULL：空串与 NULL 在索引与查询上的行为不同。
func TestWriter_ConvertsEmptyStringsToNull(t *testing.T) {
	store := &fakeStore{}
	w := newTestWriter(store)
	w.Start()

	w.Record(Entry{Method: "GET", Path: "/api/v1/x"}) // 资源/动作/IP 全空
	w.Stop()

	require.Len(t, store.batches, 1)
	got := store.batches[0][0]
	assert.Nil(t, got.Resource)
	assert.Nil(t, got.Action)
	assert.Nil(t, got.ClientIP)
	assert.Nil(t, got.UserID)
}

func (f *fakeStore) attemptCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.attempts
}
