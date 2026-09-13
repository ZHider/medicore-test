package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/medicore/backend/internal/apperr"
	"github.com/medicore/backend/internal/errcode"
	"github.com/medicore/backend/internal/model"
	"github.com/medicore/backend/internal/repository"
)

// fakeAuditStore 记录收到的过滤条件，供断言 service 传对了什么。
type fakeAuditStore struct {
	lastFilter repository.AuditFilter
	lastLimit  int
	rows       []model.AuditLogView
	err        error
	calls      int
}

func (f *fakeAuditStore) ListByCursor(
	_ context.Context, filter repository.AuditFilter, _ uint64, limit int,
) ([]model.AuditLogView, error) {
	f.calls++
	f.lastFilter = filter
	f.lastLimit = limit
	if f.err != nil {
		return nil, f.err
	}
	return f.rows, nil
}

func newAuditFixture() (*AuditService, *fakeAuditStore) {
	store := &fakeAuditStore{}
	return NewAuditService(AuditDeps{Logs: store}), store
}

// 审计查询的时间参数允许两种写法，且空串表示不限。
func TestParseOptionalTime(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    time.Time
		wantErr bool
	}{
		{"空串表示不限", "", time.Time{}, false},
		{
			"只给日期",
			"2026-09-12",
			time.Date(2026, 9, 12, 0, 0, 0, 0, time.Local),
			false,
		},
		{
			"完整时间",
			"2026-09-12 10:30:00",
			time.Date(2026, 9, 12, 10, 30, 0, 0, time.Local),
			false,
		},
		{"格式不对", "2026/09/12", time.Time{}, true},
		{"只有时间没有日期", "10:30:00", time.Time{}, true},
		{"非法日期", "2026-02-30", time.Time{}, true},
		{"随便一段文字", "昨天", time.Time{}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseOptionalTime(tt.raw)

			if tt.wantErr {
				require.Error(t, err, "格式错误必须报错，而不是静默当成'不限'")
				assert.Equal(t, errcode.ErrInvalidParam, apperr.Code(err))
				return
			}
			require.NoError(t, err)
			assert.True(t, got.Equal(tt.want), "want %v, got %v", tt.want, got)
		})
	}
}

// 本机时区解析：审计时间必须按本地时区理解，
// 否则 UTC+8 的用户查"今天"会得到昨天 08:00 到 今天 08:00 的数据。
func TestParseOptionalTime_UsesLocalTimezone(t *testing.T) {
	got, err := parseOptionalTime("2026-09-12")
	require.NoError(t, err)

	_, offset := got.Zone()
	_, wantOffset := time.Date(2026, 9, 12, 0, 0, 0, 0, time.Local).Zone()
	assert.Equal(t, wantOffset, offset, "应按本地时区解析")
}

// 起止时间颠倒必须被拒绝。
//
// 若不校验，MySQL 会老老实实返回空集，用户看到的是"这段时间没有审计记录"，
// 而真实原因是参数写反了——一个静默的、有误导性的空结果。
func TestAuditList_RejectsInvertedTimeRange(t *testing.T) {
	svc, store := newAuditFixture()

	_, err := svc.List(context.Background(), AuditQuery{
		From: "2026-09-12",
		To:   "2026-09-01",
	}, "", 20)

	assert.Equal(t, errcode.ErrInvalidParam, apperr.Code(err))
	assert.Zero(t, store.calls, "参数非法时不应发起查询")
}

// 合法的过滤条件必须原样传到仓储层，不能被吞掉。
func TestAuditList_PassesFiltersThrough(t *testing.T) {
	svc, store := newAuditFixture()
	userID := uint64(42)

	_, err := svc.List(context.Background(), AuditQuery{
		UserID:   &userID,
		Resource: "appointments",
		Action:   "cancel",
		From:     "2026-09-01",
		To:       "2026-09-12 23:59:59",
	}, "", 20)
	require.NoError(t, err)

	require.NotNil(t, store.lastFilter.UserID)
	assert.Equal(t, userID, *store.lastFilter.UserID)
	assert.Equal(t, "appointments", store.lastFilter.Resource)
	assert.Equal(t, "cancel", store.lastFilter.Action)
	assert.Equal(t, time.Date(2026, 9, 1, 0, 0, 0, 0, time.Local), store.lastFilter.From)
	assert.Equal(t, time.Date(2026, 9, 12, 23, 59, 59, 0, time.Local), store.lastFilter.To)
}

// 追踪号查询是"用户截图报错 → 定位到具体请求"的入口，必须真的传到仓储层。
func TestAuditList_PassesTraceIDThrough(t *testing.T) {
	const traceID = "ee33b8045a4cfb7a6f0ba98062ab25c6"

	svc, store := newAuditFixture()

	_, err := svc.List(context.Background(), AuditQuery{TraceID: traceID}, "", 20)
	require.NoError(t, err)

	assert.Equal(t, traceID, store.lastFilter.TraceID)
}

// 从界面上复制追踪号时常带上空白或被工具改成大写，
// 而列排序规则是 ascii_bin（逐字节比较）—— 不归一就查不到，用户会以为"记录不存在"。
func TestAuditList_NormalizesTraceID(t *testing.T) {
	const canonical = "ee33b8045a4cfb7a6f0ba98062ab25c6"

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"原样小写", canonical, canonical},
		{"转成大写", "EE33B8045A4CFB7A6F0BA98062AB25C6", canonical},
		{"首尾有空白", "  " + canonical + "\n", canonical},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, store := newAuditFixture()

			_, err := svc.List(context.Background(), AuditQuery{TraceID: tt.in}, "", 20)
			require.NoError(t, err)

			assert.Equal(t, tt.want, store.lastFilter.TraceID)
		})
	}
}

// 格式不对必须报 40001，**不能**静默忽略。
//
// 这一点与 user_id 的取舍刻意相反：忽略之后返回的是"全部日志"，
// 管理员会以为这个追踪号关联了成百上千条记录 —— 一个静默的、有误导性的结果。
// 与 from/to 的处理保持一致。
func TestAuditList_RejectsMalformedTraceID(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{"31 位少一个字符", "0123456789abcdef0123456789abcde"},
		{"33 位多一个字符", "0123456789abcdef0123456789abcdef0"},
		{"UUID 形式", "550e8400-e29b-41d4-a716-446655440000"},
		{"含非十六进制字符", "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"},
		{"非 ASCII", "中文中文中文中文中文中文中文中文中文中文中文中文中文中文中文中"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, store := newAuditFixture()

			_, err := svc.List(context.Background(), AuditQuery{TraceID: tt.in}, "", 20)

			require.Error(t, err, "格式错误的追踪号必须报错，不能当成'不限'")
			assert.Equal(t, errcode.ErrInvalidParam, apperr.Code(err))
			assert.Zero(t, store.calls, "参数非法时不应发起查询")
		})
	}
}

// 空串与纯空白等同于"不限"，此时不得给仓储层加条件。
func TestAuditList_EmptyTraceIDMeansNoFilter(t *testing.T) {
	for _, in := range []string{"", "   "} {
		svc, store := newAuditFixture()

		_, err := svc.List(context.Background(), AuditQuery{TraceID: in}, "", 20)
		require.NoError(t, err)

		assert.Empty(t, store.lastFilter.TraceID, "输入 %q 应表示不限", in)
	}
}

// 非法游标要被拒绝，而不是被当成"第一页"。
func TestAuditList_RejectsBadCursor(t *testing.T) {
	svc, store := newAuditFixture()

	_, err := svc.List(context.Background(), AuditQuery{}, "not-a-number", 20)

	assert.Equal(t, errcode.ErrInvalidParam, apperr.Code(err))
	assert.Zero(t, store.calls)
}

// page size 必须被收敛到上限，防止一次拉爆审计表。
func TestAuditList_ClampsPageSize(t *testing.T) {
	svc, store := newAuditFixture()

	_, err := svc.List(context.Background(), AuditQuery{}, "", 10_000)
	require.NoError(t, err)

	assert.Equal(t, model.MaxPageSize, store.lastLimit)
}
