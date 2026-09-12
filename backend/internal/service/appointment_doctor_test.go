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
	"github.com/medicore/backend/internal/repository"
)

// --- 内存实现 ---

type fakeDoctorAppointmentStore struct {
	byDoctor []model.DoctorAppointmentItem
	all      []model.DoctorAppointmentItem
	err      error

	// 记录最后一次调用参数，用于断言服务层传对了什么
	lastDoctorID uint64
	lastFilter   model.DoctorAppointmentFilter
	lastCursor   uint64
	lastLimit    int
	doctorCalls  int
	allCalls     int
}

func (f *fakeDoctorAppointmentStore) ListByDoctorCursor(
	_ context.Context, doctorID uint64, filter model.DoctorAppointmentFilter, cursor uint64, limit int,
) ([]model.DoctorAppointmentItem, error) {
	f.doctorCalls++
	f.lastDoctorID, f.lastFilter, f.lastCursor, f.lastLimit = doctorID, filter, cursor, limit
	if f.err != nil {
		return nil, f.err
	}
	n := limit + 1
	if n > len(f.byDoctor) {
		n = len(f.byDoctor)
	}
	return f.byDoctor[:n], nil
}

func (f *fakeDoctorAppointmentStore) ListAllCursor(
	_ context.Context, filter model.DoctorAppointmentFilter, cursor uint64, limit int,
) ([]model.DoctorAppointmentItem, error) {
	f.allCalls++
	f.lastFilter, f.lastCursor, f.lastLimit = filter, cursor, limit
	if f.err != nil {
		return nil, f.err
	}
	n := limit + 1
	if n > len(f.all) {
		n = len(f.all)
	}
	return f.all[:n], nil
}

type fakeDoctorLookup struct {
	doctorID uint64
	byUser   map[uint64]uint64
	err      error

	// calls 记录被调用次数。断言"某分支不该查医生档案"时必须看它——
	// 只看返回值的写法是永远成立的（fake 不会因为被调用而改变字段）。
	calls int
}

// fakeDoctorLookup 支持两种用法：
//   - byUser 非空时按用户 ID 精确映射（"其他医生"这类多用户场景必须用它）
//   - 否则固定返回 doctorID，适合只有一个医生的简单场景
func (f *fakeDoctorLookup) FindIDByUserID(_ context.Context, userID uint64) (uint64, error) {
	f.calls++
	if f.err != nil {
		return 0, f.err
	}
	if f.byUser != nil {
		id, ok := f.byUser[userID]
		if !ok {
			return 0, repository.ErrNotFound
		}
		return id, nil
	}
	return f.doctorID, nil
}

// --- 组装 ---

type doctorListFixture struct {
	svc       *AppointmentService
	store     *fakeDoctorAppointmentStore
	appts     *fakeAppointmentStore
	schedules *fakeScheduleStore
	doctor    *fakeDoctorLookup
}

func newDoctorListFixture(t *testing.T) *doctorListFixture {
	t.Helper()

	store := &fakeDoctorAppointmentStore{}
	appts := &fakeAppointmentStore{}
	schedules := &fakeScheduleStore{}
	doctor := &fakeDoctorLookup{doctorID: 7}

	svc := NewAppointmentService(AppointmentDeps{
		Appointments:       appts,
		DoctorAppointments: store,
		Doctors:            doctor,
		Janitor:            &fakeJanitor{},
		Schedules:          schedules,
		Stock:              &fakeStockStore{current: map[uint64]int{}},
		Idempotency:        &fakeIdempotencyStore{},
	})
	return &doctorListFixture{
		svc: svc, store: store, appts: appts, schedules: schedules, doctor: doctor,
	}
}

// --- 医生接诊列表 ---

func TestListForDoctor_UsesCallersOwnDoctorID(t *testing.T) {
	f := newDoctorListFixture(t)
	f.store.byDoctor = []model.DoctorAppointmentItem{{ID: 1}, {ID: 2}}

	page, err := f.svc.ListForDoctor(context.Background(), doctorAccess(100), model.DoctorAppointmentFilter{}, "", 20)

	require.NoError(t, err)
	assert.Len(t, page.Items, 2)
	// 关键：必须用**本人**的 doctor_id 过滤，而不是客户端传什么就查什么
	assert.Equal(t, uint64(7), f.store.lastDoctorID)
	assert.Zero(t, f.store.allCalls, "医生不应走管理员的全量分支")
}

// 医生不能查全部——只有管理员可以（spec §2.1）。
func TestListForDoctor_DoctorDoesNotSeeAll(t *testing.T) {
	f := newDoctorListFixture(t)
	f.store.all = []model.DoctorAppointmentItem{{ID: 99}}

	_, err := f.svc.ListForDoctor(context.Background(), doctorAccess(100), model.DoctorAppointmentFilter{}, "", 20)

	require.NoError(t, err)
	assert.Zero(t, f.store.allCalls)
	assert.Equal(t, 1, f.store.doctorCalls)
}

func TestListForDoctor_AdminSeesAll(t *testing.T) {
	f := newDoctorListFixture(t)
	f.store.all = []model.DoctorAppointmentItem{{ID: 1}, {ID: 2}, {ID: 3}}

	page, err := f.svc.ListForDoctor(context.Background(), adminAccess(), model.DoctorAppointmentFilter{}, "", 2)

	require.NoError(t, err)
	assert.Len(t, page.Items, 2, "多取的探测行不返回给客户端")
	assert.True(t, page.HasMore)
	assert.Equal(t, 1, f.store.allCalls)
	assert.Zero(t, f.store.doctorCalls, "管理员不应走医生分支")
	// 管理员没有医生档案，也不该因此失败、更不该去查医生档案。
	// 这里必须断言**调用次数**：此前的写法是断言 fake 的字段组合，
	// 而 fake 不会因为被调用而改变字段，断言恒为真、测不出任何东西。
	assert.Zero(t, f.doctor.calls, "管理员分支不应查医生档案")
}

// 患者不该能访问医生工作台端点。
func TestListForDoctor_PatientIsForbidden(t *testing.T) {
	f := newDoctorListFixture(t)

	_, err := f.svc.ListForDoctor(context.Background(), patientAccess(100), model.DoctorAppointmentFilter{}, "", 20)

	assert.Equal(t, errcode.ErrForbidden, apperr.Code(err))
	assert.Zero(t, f.store.doctorCalls)
	assert.Zero(t, f.store.allCalls)
}

// --- 候诊队列的归属校验 ---

// 候诊队列返回的是**未脱敏**的患者姓名与 user_id，
// 因此必须校验"这个排班是不是你的"。
//
// 曾经的漏洞：Queue 只接受 scheduleID，归属校验完全依赖路由层的
// RequireRole(doctor)，任何医生改一下 URL 里的 ID 就能拿到全院患者的身份信息。
// 病历与预问诊报告都有这一层，队列没有理由例外。
func TestQueue_OtherDoctorsScheduleIsForbidden(t *testing.T) {
	f := newDoctorListFixture(t)
	f.schedules.scheduleBy = &model.Schedule{ID: 42, DoctorID: 999} // 别人的排班
	f.appts.queueRows = []model.QueueItem{{ID: 1, PatientName: "张三"}}

	items, err := f.svc.Queue(context.Background(), doctorAccess(100), 42)

	assert.Nil(t, items)
	assert.Equal(t, errcode.ErrNotOwner, apperr.Code(err))
	assert.Zero(t, f.appts.queueCalls, "越权时必须在校验阶段就拒绝，不能先读出来再判断")
}

func TestQueue_OwnScheduleIsAllowed(t *testing.T) {
	f := newDoctorListFixture(t)
	f.schedules.scheduleBy = &model.Schedule{ID: 42, DoctorID: 7}
	f.appts.queueRows = []model.QueueItem{{ID: 1, PatientName: "张三"}}

	items, err := f.svc.Queue(context.Background(), doctorAccess(100), 42)

	require.NoError(t, err)
	require.Len(t, items, 1)
	// 队列刻意不脱敏，前端要靠这个姓名叫号
	assert.Equal(t, "张三", items[0].PatientName)
	assert.Equal(t, 1, f.appts.queueCalls)
}

// 管理员拥有全部读权限（spec §2.1），且不需要有医生档案。
func TestQueue_AdminCanReadAnySchedule(t *testing.T) {
	f := newDoctorListFixture(t)
	f.schedules.scheduleBy = &model.Schedule{ID: 42, DoctorID: 999}
	f.appts.queueRows = []model.QueueItem{{ID: 1}}

	items, err := f.svc.Queue(context.Background(), adminAccess(), 42)

	require.NoError(t, err)
	assert.Len(t, items, 1)
	assert.Zero(t, f.doctor.calls, "管理员分支不应查医生档案")
}

func TestQueue_PatientIsForbidden(t *testing.T) {
	f := newDoctorListFixture(t)

	_, err := f.svc.Queue(context.Background(), patientAccess(100), 42)

	assert.Equal(t, errcode.ErrForbidden, apperr.Code(err))
	assert.Zero(t, f.appts.queueCalls)
}

// 排班不存在与"不是你的排班"要区分开：
// 前者是 404，后者是 403，前端才能给出不同提示。
func TestQueue_MissingScheduleIsNotFound(t *testing.T) {
	f := newDoctorListFixture(t)
	f.schedules.scheduleBy = nil // FindByID 返回 ErrNotFound

	_, err := f.svc.Queue(context.Background(), doctorAccess(100), 42)

	assert.Equal(t, errcode.ErrNotFound, apperr.Code(err))
	assert.Zero(t, f.appts.queueCalls)
}

// 没有医生档案的账号（例如误配角色）应得到明确拒绝，而不是空列表。
func TestListForDoctor_WithoutDoctorProfileIsForbidden(t *testing.T) {
	f := newDoctorListFixture(t)
	f.doctor.err = repository.ErrNotFound

	_, err := f.svc.ListForDoctor(context.Background(), doctorAccess(100), model.DoctorAppointmentFilter{}, "", 20)

	assert.Equal(t, errcode.ErrForbidden, apperr.Code(err))
}

// 筛选条件必须校验：拼错的状态会静默返回空列表，让人误以为"今天没有预约"。
func TestListForDoctor_RejectsInvalidFilter(t *testing.T) {
	tests := []struct {
		name   string
		filter model.DoctorAppointmentFilter
	}{
		{"日期格式错误", model.DoctorAppointmentFilter{Date: "2026/09/12"}},
		{"非法状态", model.DoctorAppointmentFilter{Status: "not-a-status"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newDoctorListFixture(t)

			_, err := f.svc.ListForDoctor(context.Background(), doctorAccess(100), tt.filter, "", 20)

			assert.Equal(t, errcode.ErrInvalidParam, apperr.Code(err))
			assert.Zero(t, f.store.doctorCalls, "校验失败不应触达仓储")
		})
	}
}

func TestListForDoctor_AcceptsValidFilter(t *testing.T) {
	f := newDoctorListFixture(t)

	_, err := f.svc.ListForDoctor(context.Background(), doctorAccess(100),
		model.DoctorAppointmentFilter{Date: "2026-09-12", Status: model.AppointmentStatusPending}, "", 20)

	require.NoError(t, err)
	assert.Equal(t, "2026-09-12", f.store.lastFilter.Date)
	assert.Equal(t, model.AppointmentStatusPending, f.store.lastFilter.Status)
}

func TestListForDoctor_ClampsPageSize(t *testing.T) {
	f := newDoctorListFixture(t)

	_, err := f.svc.ListForDoctor(context.Background(), doctorAccess(100), model.DoctorAppointmentFilter{}, "", 999_999)

	require.NoError(t, err)
	assert.Equal(t, model.MaxPageSize, f.store.lastLimit, "size 必须有上限，否则等价于全表扫描")
}

func TestListForDoctor_RejectsBadCursor(t *testing.T) {
	f := newDoctorListFixture(t)

	_, err := f.svc.ListForDoctor(context.Background(), doctorAccess(100), model.DoctorAppointmentFilter{}, "abc", 20)

	assert.Equal(t, errcode.ErrInvalidParam, apperr.Code(err))
}

func TestListForDoctor_StoreErrorBecomesInternal(t *testing.T) {
	f := newDoctorListFixture(t)
	f.store.err = errors.New("connection refused")

	_, err := f.svc.ListForDoctor(context.Background(), doctorAccess(100), model.DoctorAppointmentFilter{}, "", 20)

	assert.Equal(t, errcode.ErrInternal, apperr.Code(err))
	assert.NotContains(t, apperr.Message(err), "connection refused", "内部错误细节不得下发")
}
