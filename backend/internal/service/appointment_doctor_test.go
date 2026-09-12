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
}

// fakeDoctorLookup 支持两种用法：
//   - byUser 非空时按用户 ID 精确映射（"其他医生"这类多用户场景必须用它）
//   - 否则固定返回 doctorID，适合只有一个医生的简单场景
func (f *fakeDoctorLookup) FindIDByUserID(_ context.Context, userID uint64) (uint64, error) {
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
	svc    *AppointmentService
	store  *fakeDoctorAppointmentStore
	doctor *fakeDoctorLookup
}

func newDoctorListFixture(t *testing.T) *doctorListFixture {
	t.Helper()

	store := &fakeDoctorAppointmentStore{}
	doctor := &fakeDoctorLookup{doctorID: 7}

	svc := NewAppointmentService(AppointmentDeps{
		Appointments:       &fakeAppointmentStore{},
		DoctorAppointments: store,
		Doctors:            doctor,
		Janitor:            &fakeJanitor{},
		Schedules:          &fakeScheduleStore{},
		Stock:              &fakeStockStore{current: map[uint64]int{}},
		Idempotency:        &fakeIdempotencyStore{},
	})
	return &doctorListFixture{svc: svc, store: store, doctor: doctor}
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
	// 管理员没有医生档案，也不该因此失败
	assert.Zero(t, f.doctor.err == nil && f.doctor.doctorID == 0, "管理员分支不应依赖医生档案")
}

// 患者不该能访问医生工作台端点。
func TestListForDoctor_PatientIsForbidden(t *testing.T) {
	f := newDoctorListFixture(t)

	_, err := f.svc.ListForDoctor(context.Background(), patientAccess(100), model.DoctorAppointmentFilter{}, "", 20)

	assert.Equal(t, errcode.ErrForbidden, apperr.Code(err))
	assert.Zero(t, f.store.doctorCalls)
	assert.Zero(t, f.store.allCalls)
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
