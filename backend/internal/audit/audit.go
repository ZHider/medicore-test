// Package audit 提供审计记录的异步批量写入。
//
// 设计权衡（这是一个必须明说的取舍）：
//
//	同步写：每条请求都等一次 INSERT 落库。审计永不丢失，但给每个请求
//	       增加一次数据库往返——写接口的延迟直接翻倍。
//	异步写：请求只做一次非阻塞入队。请求路径不受数据库影响，
//	       但进程崩溃时缓冲区中尚未落库的记录会丢失。
//
// 本项目选异步 + 有界缓冲，理由是：审计是事后追溯用途，不在业务关键路径上；
// 而给所有接口增加固定延迟的代价是持续的。丢失窗口被限制在
// 「batch size 或 flush interval 之内」，且丢弃与失败都有指标可告警。
//
// 如果合规要求更严（例如必须零丢失），改为同步写只需把 Record 换成直接落库。
package audit

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"

	"github.com/medicore/backend/internal/metrics"
	"github.com/medicore/backend/internal/model"
	"github.com/medicore/backend/internal/traceid"
)

// 写入器参数。
const (
	// BufferSize 是入队缓冲容量。满时丢弃并计数——
	// 绝不阻塞请求，否则数据库变慢会直接拖垮整个服务。
	BufferSize = 2048
	// BatchSize 达到该条数立即落库。
	BatchSize = 100
	// FlushInterval 是兜底刷盘间隔，保证低流量时记录也能及时落库。
	FlushInterval = 500 * time.Millisecond
	// writeTimeout 单次批量写入的超时。
	writeTimeout = 5 * time.Second
	// drainTimeout 是 Stop 时等待缓冲区排空的上限。
	drainTimeout = 5 * time.Second
)

// 指标标签。
const (
	resultWritten = "written"
	resultDropped = "dropped"
	resultError   = "error"
)

var auditEntriesTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "audit_entries_total",
	Help: "审计记录计数。dropped 非零说明写入速度跟不上请求量，需要扩容或降级审计范围。",
}, []string{"result"})

func init() {
	metrics.Register(auditEntriesTotal)
}

// Entry 是一条待写入的审计记录。
//
// 刻意不含请求体：请求体可能包含手机号、口令、病情描述等敏感信息，
// 把它们复制进审计表等于把脱敏工作全部作废。
type Entry struct {
	TraceID    string
	UserID     *uint64
	Role       string
	Method     string
	Path       string
	Resource   string
	ResourceID *uint64
	Action     string
	StatusCode int
	LatencyMS  int
	ClientIP   string
	UserAgent  string
}

// Recorder 是中间件依赖的最小接口，便于在测试中替换。
type Recorder interface {
	Record(Entry)
}

// Store 是审计记录的落库能力。
type Store interface {
	InsertBatch(ctx context.Context, entries []model.AuditLog) error
}

// Writer 是异步批量审计写入器。
type Writer struct {
	store Store
	log   *zap.Logger

	ch       chan model.AuditLog
	stop     chan struct{}
	done     chan struct{}
	started  bool
	stopOnce sync.Once
	// stopped 置位后 Record 直接返回：Stop 之后再入队记录只会丢失，
	// 而且会让调用方误以为已经记下了。
	stopped atomic.Bool
}

// NewWriter 构造 Writer（尚未启动）。
func NewWriter(store Store, log *zap.Logger) *Writer {
	if log == nil {
		log = zap.NewNop()
	}
	return &Writer{
		store: store,
		log:   log,
		ch:    make(chan model.AuditLog, BufferSize),
		stop:  make(chan struct{}),
		done:  make(chan struct{}),
	}
}

// Start 启动后台刷盘协程。
func (w *Writer) Start() {
	if w.started {
		return
	}
	w.started = true

	go w.loop()
	w.log.Info("audit writer started",
		zap.Int("buffer_size", BufferSize),
		zap.Int("batch_size", BatchSize),
		zap.Duration("flush_interval", FlushInterval))
}

// Record 非阻塞地投递一条审计记录。
//
// 缓冲满时丢弃并计数，绝不阻塞调用方：审计的价值再高，
// 也不值得让一次数据库抖动演变成全站请求堆积。
func (w *Writer) Record(e Entry) {
	if w.stopped.Load() {
		return
	}

	entry := model.AuditLog{
		// 第二道防线。信任边界已经在 middleware.TraceID 上把外部来源的值洗净，
		// 这里再走一次是为了兜住"以后有人绕过中间件直接调 Record"的情况：
		// 目标列是 CHAR(32) ascii，一个超长值会让**整批 100 条**一起插入失败。
		// 与下面 UserAgent 的 truncate 同一个失败模式，见本文件 truncate 的注释。
		TraceID:    traceid.Sanitize(e.TraceID),
		UserID:     e.UserID,
		Role:       optionalString(e.Role),
		Method:     e.Method,
		Path:       e.Path,
		Resource:   optionalString(e.Resource),
		ResourceID: e.ResourceID,
		Action:     optionalString(e.Action),
		StatusCode: e.StatusCode,
		LatencyMS:  e.LatencyMS,
		ClientIP:   optionalString(e.ClientIP),
		UserAgent:  optionalString(truncate(e.UserAgent, 255)),
		CreatedAt:  time.Now(),
	}

	select {
	case w.ch <- entry:
	default:
		auditEntriesTotal.WithLabelValues(resultDropped).Inc()
	}
}

// Stop 停止接收并等待缓冲区排空。
//
// 必须幂等：启动失败的回滚路径与 main 的 defer 都可能调用它，
// 重复调用会 close 已关闭的 channel 而 panic——恰恰在最需要优雅退出的时刻崩溃。
func (w *Writer) Stop() {
	w.stopOnce.Do(func() {
		if !w.started {
			return
		}
		w.stopped.Store(true)
		close(w.stop)

		select {
		case <-w.done:
		case <-time.After(drainTimeout):
			w.log.Warn("audit writer drain timed out; some entries may be lost")
		}
		w.log.Info("audit writer stopped")
	})
}

func (w *Writer) loop() {
	defer close(w.done)

	ticker := time.NewTicker(FlushInterval)
	defer ticker.Stop()

	batch := make([]model.AuditLog, 0, BatchSize)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
		defer cancel()

		if err := w.store.InsertBatch(ctx, batch); err != nil {
			auditEntriesTotal.WithLabelValues(resultError).Inc()
			w.log.Error("flush audit logs failed",
				zap.Int("count", len(batch)), zap.Error(err))
		} else {
			auditEntriesTotal.WithLabelValues(resultWritten).Add(float64(len(batch)))
		}
		batch = batch[:0]
	}

	for {
		select {
		case <-w.stop:
			// 退出前把缓冲区里剩下的记录全部取出并落库
			batch = append(batch, w.drain()...)
			flush()
			return

		case e := <-w.ch:
			batch = append(batch, e)
			if len(batch) >= BatchSize {
				flush()
			}

		case <-ticker.C:
			flush()
		}
	}
}

// drain 非阻塞地取走缓冲区中当前所有记录。
func (w *Writer) drain() []model.AuditLog {
	out := make([]model.AuditLog, 0, len(w.ch))
	for {
		select {
		case e := <-w.ch:
			out = append(out, e)
		default:
			return out
		}
	}
}

// ---------------------------------------------------------------------------
// 资源与动作推导
// ---------------------------------------------------------------------------

// methodAction 由 HTTP 方法推导默认动作。
func methodAction(method string) string {
	switch method {
	case "POST":
		return "create"
	case "PUT", "PATCH":
		return "update"
	case "DELETE":
		return "delete"
	default:
		return "view"
	}
}

// trailingActions 覆盖那些"末段不是资源名而是动作"的路由。
// 没有它，POST /appointments/:id/cancel 会被记成笼统的 create，
// 审计日志就失去了区分能力。
var trailingActions = map[string]string{
	"cancel":   "cancel",
	"checkin":  "checkin",
	"chat":     "chat",
	"submit":   "submit",
	"history":  "view",
	"report":   "view",
	"queue":    "view",
	"login":    "login",
	"logout":   "logout",
	"register": "register",
	"refresh":  "refresh",
}

// scopeSegments 是"权限域"而非资源名的路径段。
//
// /api/v1/admin/* 下的段结构是 api/v1/admin/<resource>，
// 若直接取第 3 段，三个管理端点（audit-logs / sync / sync-logs）会全部被记成
// resource=admin，按资源聚合时它们混成一条，审计查询就失去了区分能力。
var scopeSegments = map[string]struct{}{
	"admin": {},
}

// Describe 从路由模板推导审计用的资源与动作。
//
// 输入是 gin 的 FullPath（形如 /api/v1/appointments/:id/cancel）而非真实路径：
// 用真实路径会让每个 ID 产生不同的 "资源名"，审计查询将无法按资源聚合。
func Describe(fullPath, method string) (resource, action string) {
	segments := strings.Split(strings.Trim(fullPath, "/"), "/")
	if len(segments) < 3 || segments[0] != "api" {
		return "", methodAction(method)
	}

	// 取版本段之后的第一个非权限域段作为资源名；若整条路径只有权限域，
	// 就退回到该域本身（例如未来出现 /api/v1/admin 这样的端点）。
	idx := 2
	if _, isScope := scopeSegments[segments[idx]]; isScope && len(segments) > idx+1 {
		idx++
	}
	resource = segments[idx]

	if idx < len(segments)-1 {
		if a, ok := trailingActions[segments[len(segments)-1]]; ok {
			return resource, a
		}
	}
	return resource, methodAction(method)
}

// optionalString 把空串转成 nil，避免数据库里存一堆空字符串。
// 空串和 NULL 在查询与索引上的行为不同，统一为 NULL 更干净。
func optionalString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// truncate 把字符串按**字节**截断到 max，并保证结果是**可安全写入 utf8mb4 列**的。
//
// 两条约束都来自同一个失败模式：写不进这一列，会让**整批 100 条审计一起失败**。
//
//  1. 不能切断多字节字符。UA 里出现非 ASCII 很常见（不少国产 App 会把中文写进 UA），
//     按字节硬切会切出非法 UTF-8，MySQL 严格模式下直接拒绝。
//  2. **输入本身可能就是非法 UTF-8**。这条是 fuzz（FuzzTruncate）发现的：
//     HTTP 头是字节串，客户端可以发 `User-Agent: \xf0`，Go 的 net/http
//     不做 UTF-8 校验，原样递给我们 —— 一个构造出来的请求就能带走一批审计记录。
//     所以这里不能假设输入是合法的。
//
// max <= 0 时返回空串，而不是让 s[:max] 抛 slice bounds panic（同样由 fuzz 发现）。
func truncate(s string, max int) string {
	if max <= 0 {
		return ""
	}

	if len(s) > max {
		// 回退到最近的一个字符起始字节，避免切断多字节字符
		cut := max
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut]
	}

	// 输入可能本来就是非法 UTF-8（见上），必须修掉。
	// 用空串替换非法字节：结果只会变短，不会突破 max。
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "")
	}
	return s
}
