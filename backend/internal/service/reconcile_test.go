package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/medicore/backend/internal/apperr"
	"github.com/medicore/backend/internal/errcode"
	"github.com/medicore/backend/internal/model"
)

// newReconcileFixture 构造对账场景：MySQL 侧有 3 个开放中的排班。
func newReconcileFixture(t *testing.T) *appointmentFixture {
	t.Helper()

	f := newAppointmentFixture(t)
	f.schedules.reconcilable = []model.Schedule{
		{ID: 1, Remaining: 10, Total: 20, Status: model.ScheduleStatusOpen},
		{ID: 2, Remaining: 5, Total: 20, Status: model.ScheduleStatusOpen},
		{ID: 3, Remaining: 0, Total: 20, Status: model.ScheduleStatusOpen},
	}
	f.stock.current = map[uint64]int{}
	return f
}

func TestReconcile_NoDrift(t *testing.T) {
	f := newReconcileFixture(t)
	// Redis 与 MySQL 完全一致
	f.stock.current[1] = 10
	f.stock.current[2] = 5
	f.stock.current[3] = 0

	report, err := f.svc.ReconcileStock(context.Background())

	require.NoError(t, err)
	assert.Equal(t, 3, report.Scanned)
	assert.Equal(t, 3, report.Matched)
	assert.Zero(t, report.Corrected)
	assert.Zero(t, f.stock.casCalls, "无偏差时不应发起写操作")
}

// 键不存在（尚未预热或已过期）不是偏差，必须跳过。
// 若把它当成"Redis=0"去修正，会把一个本可以从数据库重新加载的号源写成 0。
func TestReconcile_AbsentKeyIsNotDrift(t *testing.T) {
	f := newReconcileFixture(t)
	// 全部键都不存在

	report, err := f.svc.ReconcileStock(context.Background())

	require.NoError(t, err)
	assert.Equal(t, 3, report.Matched)
	assert.Zero(t, report.Corrected)
	assert.Zero(t, f.stock.casCalls)
}

// Redis 少于 MySQL：典型偏差（回补失败或进程崩溃），修正后应当把号源找回来。
func TestReconcile_CorrectsRedisLowerThanMySQL(t *testing.T) {
	f := newReconcileFixture(t)
	f.stock.current[1] = 7 // MySQL 是 10

	report, err := f.svc.ReconcileStock(context.Background())

	require.NoError(t, err)
	assert.Equal(t, 1, report.Corrected)
	require.Len(t, f.stock.casWrites, 1)
	assert.Equal(t, casWrite{ScheduleID: 1, Expected: 7, Desired: 10}, f.stock.casWrites[0])
}

// Redis 多于 MySQL：超卖风险方向，同样必须修正。
func TestReconcile_CorrectsRedisHigherThanMySQL(t *testing.T) {
	f := newReconcileFixture(t)
	f.stock.current[2] = 9 // MySQL 是 5

	report, err := f.svc.ReconcileStock(context.Background())

	require.NoError(t, err)
	assert.Equal(t, 1, report.Corrected)
	require.Len(t, f.stock.casWrites, 1)
	assert.Equal(t, 9, f.stock.casWrites[0].Expected)
	assert.Equal(t, 5, f.stock.casWrites[0].Desired)
}

// 值被并发修改时必须放弃本次修正，而不是强行覆盖。
// 覆盖会把并发扣减掉的号源还回去，反而制造超卖。
func TestReconcile_SkipsWhenValueChangedConcurrently(t *testing.T) {
	f := newReconcileFixture(t)
	f.stock.current[1] = 7
	f.stock.casReject = true

	report, err := f.svc.ReconcileStock(context.Background())

	require.NoError(t, err)
	assert.Equal(t, 1, report.Skipped)
	assert.Zero(t, report.Corrected, "比对失败不得计入已修正")
}

// 单个排班读取失败不应中断整轮扫描。
func TestReconcile_ContinuesAfterReadError(t *testing.T) {
	f := newReconcileFixture(t)
	f.stock.currentErr = errors.New("redis timeout")

	report, err := f.svc.ReconcileStock(context.Background())

	require.NoError(t, err)
	assert.Equal(t, 3, report.Scanned, "读取失败也要继续处理其余排班")
	assert.Equal(t, 3, report.Errors)
	assert.Zero(t, report.Corrected)
}

func TestReconcile_ScanErrorPropagates(t *testing.T) {
	f := newReconcileFixture(t)
	f.schedules.err = errors.New("db down")

	_, err := f.svc.ReconcileStock(context.Background())

	assert.Equal(t, errcode.ErrInternal, apperr.Code(err))
}

// 超过单批上限时必须继续扫描，否则排在后面的排班永远得不到对账。
func TestReconcile_ScansInBatches(t *testing.T) {
	f := newReconcileFixture(t)
	f.schedules.reconcilable = make([]model.Schedule, 0, reconcileBatchSize+1)
	for i := 1; i <= reconcileBatchSize+1; i++ {
		f.schedules.reconcilable = append(f.schedules.reconcilable, model.Schedule{
			ID: uint64(i), Remaining: 1, Total: 20, Status: model.ScheduleStatusOpen,
		})
	}
	f.stock.current = map[uint64]int{}

	report, err := f.svc.ReconcileStock(context.Background())

	require.NoError(t, err)
	assert.Equal(t, reconcileBatchSize+1, report.Scanned)
	assert.GreaterOrEqual(t, f.schedules.scanCalls, 2, "应至少扫描两批")
}

// 上下文取消时返回已完成的部分，不当作失败。
func TestReconcile_RespectsContextCancellation(t *testing.T) {
	f := newReconcileFixture(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	report, err := f.svc.ReconcileStock(ctx)

	require.NoError(t, err, "取消不是错误")
	assert.Zero(t, report.Scanned, "取消后不应继续扫描")
}
