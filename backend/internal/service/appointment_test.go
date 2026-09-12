package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/medicore/backend/internal/apperr"
	"github.com/medicore/backend/internal/errcode"
	"github.com/medicore/backend/internal/model"
	"github.com/medicore/backend/internal/repository"
)

// --- 内存实现 ---

type fakeAppointmentStore struct {
	created      *model.Appointment
	createErr    error
	createCalls  int
	existing     *model.Appointment
	existingErr  error
	item         *model.AppointmentItem
	itemErr      error
	listRows     []model.AppointmentItem
	listErr      error
	cancelResult *repository.StateChangeResult
	cancelErr    error
	cancelReason string
	checkInAppt  *model.Appointment
	checkInErr   error
	queueRows    []model.QueueItem
	queueErr     error
	// 供预问诊归属校验
	appointmentByID *model.Appointment
	findByIDErr     error
}

func (f *fakeAppointmentStore) Create(_ context.Context, in repository.CreateAppointmentInput) (*model.Appointment, error) {
	f.createCalls++
	if f.createErr != nil {
		return nil, f.createErr
	}
	if f.created == nil {
		f.created = &model.Appointment{
			ID:             1,
			UserID:         in.UserID,
			ScheduleID:     in.ScheduleID,
			Status:         model.AppointmentStatusPending,
			IdempotencyKey: in.IdempotencyKey,
		}
	}
	return f.created, nil
}

// FindByID 供预问诊做资源层归属校验。
func (f *fakeAppointmentStore) FindByID(_ context.Context, id uint64) (*model.Appointment, error) {
	if f.findByIDErr != nil {
		return nil, f.findByIDErr
	}
	if f.appointmentByID == nil {
		return &model.Appointment{ID: id, DoctorID: 7}, nil
	}
	return f.appointmentByID, nil
}

func (f *fakeAppointmentStore) FindByUserAndIdempotencyKey(context.Context, uint64, string) (*model.Appointment, error) {
	if f.existingErr != nil {
		return nil, f.existingErr
	}
	if f.existing == nil {
		return nil, repository.ErrNotFound
	}
	return f.existing, nil
}

func (f *fakeAppointmentStore) FindItemByID(context.Context, uint64, uint64) (*model.AppointmentItem, error) {
	if f.itemErr != nil {
		return nil, f.itemErr
	}
	if f.item == nil {
		return &model.AppointmentItem{ID: 1, Status: model.AppointmentStatusPending}, nil
	}
	return f.item, nil
}

func (f *fakeAppointmentStore) ListByUserCursor(context.Context, uint64, uint64, int) ([]model.AppointmentItem, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.listRows, nil
}

func (f *fakeAppointmentStore) Cancel(_ context.Context, _, _ uint64, reason string) (*repository.StateChangeResult, error) {
	f.cancelReason = reason
	if f.cancelErr != nil {
		return nil, f.cancelErr
	}
	if f.cancelResult == nil {
		return &repository.StateChangeResult{
			Appointment:   &model.Appointment{ID: 1, ScheduleID: 7},
			ScheduleTotal: 20,
			Changed:       true,
		}, nil
	}
	return f.cancelResult, nil
}

func (f *fakeAppointmentStore) CheckIn(context.Context, uint64, uint64) (*model.Appointment, error) {
	if f.checkInErr != nil {
		return nil, f.checkInErr
	}
	if f.checkInAppt == nil {
		no := 3
		return &model.Appointment{ID: 1, QueueNo: &no, Status: model.AppointmentStatusConfirmed}, nil
	}
	return f.checkInAppt, nil
}

func (f *fakeAppointmentStore) ListQueue(context.Context, uint64) ([]model.QueueItem, error) {
	if f.queueErr != nil {
		return nil, f.queueErr
	}
	return f.queueRows, nil
}

type fakeJanitor struct {
	expiredRows []model.Appointment
	listErr     error
	expireFn    func(id uint64) (*repository.StateChangeResult, error)
	expireCalls []uint64
}

func (f *fakeJanitor) ListExpiredPending(context.Context, time.Time, int) ([]model.Appointment, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.expiredRows, nil
}

func (f *fakeJanitor) Expire(_ context.Context, id uint64) (*repository.StateChangeResult, error) {
	f.expireCalls = append(f.expireCalls, id)
	if f.expireFn != nil {
		return f.expireFn(id)
	}
	return nil, nil
}

type fakeStockStore struct {
	// decrementResults 按顺序返回，用于模拟"键不存在 → 预热后成功"
	decrementResults []repository.StockResult
	decrementCalls   int
	ensureCalls      int
	releaseCalls     int
	lastCapacity     int
	err              error

	// 对账相关
	current    map[uint64]int
	currentErr error
	casErr     error
	// casReject 为 true 时模拟"值已被并发修改"，修正被放弃
	casReject bool
	casCalls  int
	casWrites []casWrite
}

type casWrite struct {
	ScheduleID uint64
	Expected   int
	Desired    int
}

func (f *fakeStockStore) EnsureLoaded(context.Context, *model.Schedule) error {
	f.ensureCalls++
	return f.err
}

func (f *fakeStockStore) Decrement(context.Context, uint64, int) (repository.StockResult, error) {
	if f.err != nil {
		return 0, f.err
	}
	idx := f.decrementCalls
	f.decrementCalls++
	if idx < len(f.decrementResults) {
		return f.decrementResults[idx], nil
	}
	return repository.StockOK, nil
}

func (f *fakeStockStore) Release(_ context.Context, _ uint64, _, capacity int) (int, error) {
	f.releaseCalls++
	f.lastCapacity = capacity
	if f.err != nil {
		return 0, f.err
	}
	return 1, nil
}

func (f *fakeStockStore) Current(_ context.Context, scheduleID uint64) (int, bool, error) {
	if f.currentErr != nil {
		return 0, false, f.currentErr
	}
	v, ok := f.current[scheduleID]
	return v, ok, nil
}

func (f *fakeStockStore) CompareAndSet(_ context.Context, scheduleID uint64, expected, desired int) (bool, error) {
	f.casCalls++
	if f.casErr != nil {
		return false, f.casErr
	}
	if f.casReject {
		return false, nil
	}
	f.casWrites = append(f.casWrites, casWrite{scheduleID, expected, desired})
	return true, nil
}

type fakeIdempotencyStore struct {
	acquired    bool
	existingID  uint64
	acquireErr  error
	completeErr error
	completed   bool
	released    int
}

func (f *fakeIdempotencyStore) Acquire(context.Context, uint64, string) (bool, uint64, error) {
	if f.acquireErr != nil {
		return false, 0, f.acquireErr
	}
	return f.acquired, f.existingID, nil
}

func (f *fakeIdempotencyStore) Complete(context.Context, uint64, string, uint64) error {
	f.completed = true
	return f.completeErr
}

func (f *fakeIdempotencyStore) Release(context.Context, uint64, string) error {
	f.released++
	return nil
}

// --- 组装 ---

type appointmentFixture struct {
	svc         *AppointmentService
	appts       *fakeAppointmentStore
	janitor     *fakeJanitor
	stock       *fakeStockStore
	idempotency *fakeIdempotencyStore
	schedules   *fakeScheduleStore
}

func newAppointmentFixture(t *testing.T) *appointmentFixture {
	t.Helper()

	appts := &fakeAppointmentStore{}
	janitor := &fakeJanitor{}
	stock := &fakeStockStore{current: map[uint64]int{}}
	idempotency := &fakeIdempotencyStore{acquired: true}
	schedules := &fakeScheduleStore{
		scheduleBy: &model.Schedule{
			ID:           7,
			DoctorID:     1,
			DepartmentID: 2,
			SchedDate:    time.Now(),
			TimeSlot:     model.TimeSlotAM,
			Total:        20,
			Remaining:    20,
			Status:       model.ScheduleStatusOpen,
		},
	}

	svc := NewAppointmentService(AppointmentDeps{
		Appointments:  appts,
		Janitor:       janitor,
		Schedules:     schedules,
		Stock:         stock,
		Idempotency:   idempotency,
		ConfirmWindow: 15 * time.Minute,
	})

	return &appointmentFixture{
		svc: svc, appts: appts, janitor: janitor,
		stock: stock, idempotency: idempotency, schedules: schedules,
	}
}

func grabRequest() model.GrabRequest {
	return model.GrabRequest{ScheduleID: 7, IdempotencyKey: "idem-key-0001"}
}

// --- 抢号：正常路径 ---

func TestGrab_Success(t *testing.T) {
	f := newAppointmentFixture(t)

	item, err := f.svc.Grab(context.Background(), 100, grabRequest())

	require.NoError(t, err)
	assert.Equal(t, model.AppointmentStatusPending, item.Status)
	assert.Equal(t, 1, f.stock.decrementCalls)
	assert.Equal(t, 1, f.stock.ensureCalls)
	assert.True(t, f.idempotency.completed, "成功后必须记录幂等结果")
	assert.Zero(t, f.stock.releaseCalls, "成功路径不应回补号源")
	assert.Zero(t, f.idempotency.released)
}

// --- 抢号：幂等 ---

// 同一幂等键重放：直接返回上次结果，不得再扣一次号源。
func TestGrab_IdempotentReplay(t *testing.T) {
	f := newAppointmentFixture(t)
	f.idempotency.acquired = false
	f.idempotency.existingID = 42

	_, err := f.svc.Grab(context.Background(), 100, grabRequest())

	require.NoError(t, err)
	assert.Zero(t, f.stock.decrementCalls, "重放不得再次扣减号源")
	assert.Zero(t, f.appts.createCalls, "重放不得再次创建预约")
}

func TestGrab_InFlightIsRejected(t *testing.T) {
	f := newAppointmentFixture(t)
	f.idempotency.acquireErr = repository.ErrInFlight

	_, err := f.svc.Grab(context.Background(), 100, grabRequest())

	assert.Equal(t, errcode.ErrAlreadyDone, apperr.Code(err))
	assert.Zero(t, f.stock.decrementCalls)
}

// --- 抢号：号源不足 ---

func TestGrab_NoStockReleasesIdempotencyKey(t *testing.T) {
	f := newAppointmentFixture(t)
	f.stock.decrementResults = []repository.StockResult{repository.StockInsufficient}

	_, err := f.svc.Grab(context.Background(), 100, grabRequest())

	assert.Equal(t, errcode.ErrNoStock, apperr.Code(err))
	assert.Equal(t, 1, f.idempotency.released, "失败后必须释放幂等键，否则客户端无法重试")
	assert.Zero(t, f.appts.createCalls, "号源不足时不应进入数据库写入")
}

// 键不存在时必须"预热后重试"，而不是当作号源不足。
// 这是对 spec 原设计的修正：否则 Redis 冷启动会让所有抢号全部失败。
func TestGrab_NotLoadedTriggersWarmupAndRetry(t *testing.T) {
	f := newAppointmentFixture(t)
	f.stock.decrementResults = []repository.StockResult{
		repository.StockNotLoaded,
		repository.StockOK,
	}

	_, err := f.svc.Grab(context.Background(), 100, grabRequest())

	require.NoError(t, err)
	assert.Equal(t, 2, f.stock.decrementCalls, "应重试一次")
	assert.Equal(t, 2, f.stock.ensureCalls, "重试前必须重新预热")
}

func TestGrab_StillNotLoadedAfterRetryIsInternalError(t *testing.T) {
	f := newAppointmentFixture(t)
	f.stock.decrementResults = []repository.StockResult{
		repository.StockNotLoaded,
		repository.StockNotLoaded,
	}

	_, err := f.svc.Grab(context.Background(), 100, grabRequest())

	assert.Equal(t, errcode.ErrInternal, apperr.Code(err))
	assert.Zero(t, f.appts.createCalls)
}

// --- 抢号：数据库失败必须回补 Redis 号源 ---

// 这是最关键的不变量：Redis 已扣减、MySQL 事务回滚时，
// 若不回补，号源会凭空蒸发，出现"总号源 20 却越来越难抢"的诡异现象。
func TestGrab_DatabaseFailureReleasesRedisStock(t *testing.T) {
	tests := []struct {
		name      string
		createErr error
		wantCode  errcode.Code
	}{
		{"号源已尽", repository.ErrNoStock, errcode.ErrNoStock},
		{"排班不存在", repository.ErrNotFound, errcode.ErrNotFound},
		{"其他数据库错误", errors.New("connection reset"), errcode.ErrInternal},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newAppointmentFixture(t)
			f.appts.createErr = tt.createErr

			_, err := f.svc.Grab(context.Background(), 100, grabRequest())

			assert.Equal(t, tt.wantCode, apperr.Code(err))
			assert.Equal(t, 1, f.stock.releaseCalls, "数据库写入失败必须回补 Redis 号源")
			assert.Equal(t, 20, f.stock.lastCapacity, "回补需带上容量上限")
			assert.Equal(t, 1, f.idempotency.released)
		})
	}
}

// 数据库唯一键冲突且幂等键能查到记录：视为幂等重放，返回已有预约。
func TestGrab_DuplicateKeyRecoversExistingAppointment(t *testing.T) {
	f := newAppointmentFixture(t)
	f.appts.createErr = repository.ErrDuplicateKey
	f.appts.existing = &model.Appointment{ID: 99, ScheduleID: 7}

	_, err := f.svc.Grab(context.Background(), 100, grabRequest())

	require.NoError(t, err)
	assert.Equal(t, 1, f.stock.releaseCalls, "重复抢号时多扣的号源必须回补")
}

// 唯一键冲突但查不到同一幂等键：命中 uk_active，说明用户已占用该号源。
func TestGrab_DuplicateActiveAppointmentIsConflict(t *testing.T) {
	f := newAppointmentFixture(t)
	f.appts.createErr = repository.ErrDuplicateKey
	f.appts.existingErr = repository.ErrNotFound

	_, err := f.svc.Grab(context.Background(), 100, grabRequest())

	assert.Equal(t, errcode.ErrConflict, apperr.Code(err))
	assert.Equal(t, 1, f.stock.releaseCalls)
}

// --- 抢号：前置校验 ---

func TestGrab_ScheduleNotGrabbable(t *testing.T) {
	t.Run("排班不存在", func(t *testing.T) {
		f := newAppointmentFixture(t)
		f.schedules.scheduleBy = nil

		_, err := f.svc.Grab(context.Background(), 100, grabRequest())
		assert.Equal(t, errcode.ErrNotFound, apperr.Code(err))
		assert.Zero(t, f.stock.decrementCalls, "排班不存在时不应触碰号源")
	})

	t.Run("排班已关闭", func(t *testing.T) {
		f := newAppointmentFixture(t)
		f.schedules.scheduleBy.Status = model.ScheduleStatusClosed

		_, err := f.svc.Grab(context.Background(), 100, grabRequest())
		assert.Equal(t, errcode.ErrConflict, apperr.Code(err))
		assert.Zero(t, f.stock.decrementCalls)
	})
}

// 客户端未提供幂等键时，服务端必须补一个，否则幂等保护形同虚设。
func TestGrab_GeneratesIdempotencyKeyWhenAbsent(t *testing.T) {
	f := newAppointmentFixture(t)

	_, err := f.svc.Grab(context.Background(), 100, model.GrabRequest{ScheduleID: 7})

	require.NoError(t, err)
	require.NotNil(t, f.appts.created)
	assert.NotEmpty(t, f.appts.created.IdempotencyKey)
}

// 幂等结果写入失败时，应释放键让重试走数据库唯一索引兜底，
// 而不是留下"处理中"把用户挡在门外 24 小时。
func TestGrab_CompleteFailureFallsBackToRelease(t *testing.T) {
	f := newAppointmentFixture(t)
	f.idempotency.completeErr = errors.New("redis down")

	_, err := f.svc.Grab(context.Background(), 100, grabRequest())

	require.NoError(t, err, "预约已创建成功，不应因幂等记录失败而报错")
	assert.Equal(t, 1, f.idempotency.released)
}

// --- 取消 ---

func TestCancel_ReleasesRedisStock(t *testing.T) {
	f := newAppointmentFixture(t)

	err := f.svc.Cancel(context.Background(), 100, 1, "临时有事")

	require.NoError(t, err)
	assert.Equal(t, 1, f.stock.releaseCalls, "取消必须回补 Redis 号源")
	assert.Equal(t, 20, f.stock.lastCapacity)
	assert.Equal(t, "临时有事", f.appts.cancelReason)
}

func TestCancel_ErrorMapping(t *testing.T) {
	tests := []struct {
		name     string
		storeErr error
		wantCode errcode.Code
	}{
		{"预约不存在", repository.ErrNotFound, errcode.ErrNotFound},
		{"状态不允许", repository.ErrInvalidState, errcode.ErrConflict},
		{"其他错误", errors.New("db down"), errcode.ErrInternal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newAppointmentFixture(t)
			f.appts.cancelErr = tt.storeErr

			err := f.svc.Cancel(context.Background(), 100, 1, "")

			assert.Equal(t, tt.wantCode, apperr.Code(err))
			assert.Zero(t, f.stock.releaseCalls, "取消失败时不得回补号源")
		})
	}
}

// --- 签到 ---

func TestCheckIn_Success(t *testing.T) {
	f := newAppointmentFixture(t)

	appt, err := f.svc.CheckIn(context.Background(), 100, 1)

	require.NoError(t, err)
	require.NotNil(t, appt.QueueNo)
	assert.Equal(t, 3, *appt.QueueNo)
}

func TestCheckIn_ErrorMapping(t *testing.T) {
	tests := []struct {
		name     string
		storeErr error
		wantCode errcode.Code
	}{
		{"预约不存在", repository.ErrNotFound, errcode.ErrNotFound},
		{"状态不允许", repository.ErrInvalidState, errcode.ErrConflict},
		{"其他错误", errors.New("db down"), errcode.ErrInternal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newAppointmentFixture(t)
			f.appts.checkInErr = tt.storeErr

			_, err := f.svc.CheckIn(context.Background(), 100, 1)
			assert.Equal(t, tt.wantCode, apperr.Code(err))
		})
	}
}

// --- 超时释放 ---

func TestReleaseExpired_OnlyReleasesChangedAppointments(t *testing.T) {
	f := newAppointmentFixture(t)
	f.janitor.expiredRows = []model.Appointment{{ID: 1}, {ID: 2}, {ID: 3}}
	f.janitor.expireFn = func(id uint64) (*repository.StateChangeResult, error) {
		// 2 号已被其它实例处理
		if id == 2 {
			return &repository.StateChangeResult{Changed: false}, nil
		}
		return &repository.StateChangeResult{
			Appointment:   &model.Appointment{ID: id, ScheduleID: 7},
			ScheduleTotal: 20,
			Changed:       true,
		}, nil
	}

	released, err := f.svc.ReleaseExpired(context.Background())

	require.NoError(t, err)
	assert.Equal(t, 2, released)
	assert.Len(t, f.janitor.expireCalls, 3)
	assert.Equal(t, 2, f.stock.releaseCalls, "只有真正发生状态跃迁的才回补号源")
}

// 单条失败不能中断整批，否则一条坏数据会永久阻塞清理任务。
func TestReleaseExpired_ContinuesAfterSingleFailure(t *testing.T) {
	f := newAppointmentFixture(t)
	f.janitor.expiredRows = []model.Appointment{{ID: 1}, {ID: 2}}
	f.janitor.expireFn = func(id uint64) (*repository.StateChangeResult, error) {
		if id == 1 {
			return nil, errors.New("deadlock")
		}
		return &repository.StateChangeResult{
			Appointment:   &model.Appointment{ID: id, ScheduleID: 7},
			ScheduleTotal: 20,
			Changed:       true,
		}, nil
	}

	released, err := f.svc.ReleaseExpired(context.Background())

	require.NoError(t, err)
	assert.Equal(t, 1, released)
	assert.Len(t, f.janitor.expireCalls, 2, "第一条失败后仍应处理第二条")
}

// --- 我的预约 ---

func TestListMine_Pagination(t *testing.T) {
	f := newAppointmentFixture(t)
	f.appts.listRows = []model.AppointmentItem{{ID: 3}, {ID: 2}, {ID: 1}}

	page, err := f.svc.ListMine(context.Background(), 100, "", 2)

	require.NoError(t, err)
	assert.Len(t, page.Items, 2, "多取的探测行不返回给客户端")
	assert.True(t, page.HasMore)
	assert.Equal(t, uint64(2), page.NextCursor)
}

func TestListMine_RejectsBadCursor(t *testing.T) {
	f := newAppointmentFixture(t)

	_, err := f.svc.ListMine(context.Background(), 100, "not-a-number", 20)

	assert.Equal(t, errcode.ErrInvalidParam, apperr.Code(err))
}
