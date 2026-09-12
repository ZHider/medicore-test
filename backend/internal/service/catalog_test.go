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

type fakeDepartmentStore struct {
	items    []model.DepartmentBrief
	existing map[uint64]bool
	err      error
}

func (f *fakeDepartmentStore) ListActive(context.Context) ([]model.DepartmentBrief, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.items, nil
}

func (f *fakeDepartmentStore) Exists(_ context.Context, id uint64) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	return f.existing[id], nil
}

type fakeDoctorStore struct {
	list        []model.DoctorListItem
	detail      *model.DoctorDetail
	departments []model.DepartmentBrief
	err         error
}

func (f *fakeDoctorStore) ListByDepartment(context.Context, uint64) ([]model.DoctorListItem, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.list, nil
}

func (f *fakeDoctorStore) FindDetail(context.Context, uint64) (*model.DoctorDetail, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.detail == nil {
		return nil, repository.ErrNotFound
	}
	return f.detail, nil
}

func (f *fakeDoctorStore) ListDepartmentsOfDoctor(context.Context, uint64) ([]model.DepartmentBrief, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.departments, nil
}

type fakeScheduleStore struct {
	rows []model.ScheduleListItem
	err  error

	// 记录最后一次调用的参数，供断言服务层如何传参
	lastDeptID uint64
	lastDate   time.Time
	lastFrom   time.Time
	lastTo     time.Time
	lastCursor uint64
	lastLimit  int
}

// simulate 复刻仓储层"最多返回 limit+1 条"的约定。
func (f *fakeScheduleStore) simulate(limit int) []model.ScheduleListItem {
	n := limit + 1
	if n > len(f.rows) {
		n = len(f.rows)
	}
	return f.rows[:n]
}

func (f *fakeScheduleStore) ListByDepartmentDate(
	_ context.Context, departmentID uint64, date time.Time, cursor uint64, limit int,
) ([]model.ScheduleListItem, error) {
	f.lastDeptID, f.lastDate, f.lastCursor, f.lastLimit = departmentID, date, cursor, limit
	if f.err != nil {
		return nil, f.err
	}
	return f.simulate(limit), nil
}

func (f *fakeScheduleStore) ListByDoctorRange(
	_ context.Context, doctorID uint64, from, to time.Time, cursor uint64, limit int,
) ([]model.ScheduleListItem, error) {
	f.lastDeptID, f.lastFrom, f.lastTo, f.lastCursor, f.lastLimit = doctorID, from, to, cursor, limit
	if f.err != nil {
		return nil, f.err
	}
	return f.simulate(limit), nil
}

func newTestCatalogService(t *testing.T) (*CatalogService, *fakeDepartmentStore, *fakeDoctorStore, *fakeScheduleStore) {
	t.Helper()

	departments := &fakeDepartmentStore{
		items:    []model.DepartmentBrief{{ID: 1, Code: "IM", Name: "内科"}},
		existing: map[uint64]bool{1: true},
	}
	doctors := &fakeDoctorStore{}
	schedules := &fakeScheduleStore{}

	svc := NewCatalogService(CatalogDeps{
		Departments: departments,
		Doctors:     doctors,
		Schedules:   schedules,
	})
	return svc, departments, doctors, schedules
}

func scheduleRows(n int) []model.ScheduleListItem {
	rows := make([]model.ScheduleListItem, 0, n)
	for i := n; i >= 1; i-- {
		rows = append(rows, model.ScheduleListItem{ID: uint64(i), DoctorID: 1, Status: model.ScheduleStatusOpen})
	}
	return rows
}

// --- 科室 ---

func TestCatalog_ListDepartments(t *testing.T) {
	svc, _, _, _ := newTestCatalogService(t)

	items, err := svc.ListDepartments(context.Background())

	require.NoError(t, err)
	assert.Len(t, items, 1)
	assert.Equal(t, "内科", items[0].Name)
}

// --- 科室医生 ---

// 不存在的科室必须返回 404，而不是空列表：
// 否则客户端无法区分"科室不存在"与"该科室暂无医生"。
func TestCatalog_ListDoctors_UnknownDepartmentIsNotFound(t *testing.T) {
	svc, _, _, _ := newTestCatalogService(t)

	_, err := svc.ListDoctorsByDepartment(context.Background(), 999)

	assert.Equal(t, errcode.ErrNotFound, apperr.Code(err))
}

func TestCatalog_ListDoctors_Success(t *testing.T) {
	svc, _, doctors, _ := newTestCatalogService(t)
	doctors.list = []model.DoctorListItem{{ID: 1, Name: "内科医生01", ConsultFee: "15.00"}}

	items, err := svc.ListDoctorsByDepartment(context.Background(), 1)

	require.NoError(t, err)
	assert.Len(t, items, 1)
	assert.Equal(t, "15.00", items[0].ConsultFee)
}

// --- 医生详情 ---

func TestCatalog_DoctorDetail_NotFound(t *testing.T) {
	svc, _, _, _ := newTestCatalogService(t)

	_, err := svc.DoctorDetail(context.Background(), 999)

	assert.Equal(t, errcode.ErrNotFound, apperr.Code(err))
}

func TestCatalog_DoctorDetail_IncludesDepartments(t *testing.T) {
	svc, _, doctors, _ := newTestCatalogService(t)
	doctors.detail = &model.DoctorDetail{ID: 1, Name: "内科医生01"}
	doctors.departments = []model.DepartmentBrief{{ID: 1, Name: "内科"}, {ID: 3, Name: "儿科"}}

	detail, err := svc.DoctorDetail(context.Background(), 1)

	require.NoError(t, err)
	assert.Equal(t, "内科医生01", detail.Name)
	// 多对多：详情必须带出全部所属科室
	assert.Len(t, detail.Departments, 2)
	assert.Equal(t, "儿科", detail.Departments[1].Name)
}

// --- 排班查询与游标分页 ---

func TestCatalog_SchedulesByDepartment_DefaultsToToday(t *testing.T) {
	svc, _, _, schedules := newTestCatalogService(t)

	_, err := svc.ListSchedulesByDepartmentDate(context.Background(), 1, "", "", 20)

	require.NoError(t, err)
	assert.Equal(t, time.Now().Format(time.DateOnly), schedules.lastDate.Format(time.DateOnly))
}

func TestCatalog_SchedulesByDepartment_RejectsBadInput(t *testing.T) {
	tests := []struct {
		name     string
		date     string
		cursor   string
		wantCode errcode.Code
	}{
		{"日期格式错误", "2026/09/12", "", errcode.ErrInvalidParam},
		{"游标非数字", "", "abc", errcode.ErrInvalidParam},
		{"游标为负数", "", "-1", errcode.ErrInvalidParam},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, _, _, _ := newTestCatalogService(t)

			_, err := svc.ListSchedulesByDepartmentDate(context.Background(), 1, tt.date, tt.cursor, 20)

			assert.Equal(t, tt.wantCode, apperr.Code(err))
		})
	}
}

// size 必须有上限，否则 size=999999 会被当作合法请求。
func TestCatalog_SchedulesByDepartment_ClampsPageSize(t *testing.T) {
	svc, _, _, schedules := newTestCatalogService(t)
	schedules.rows = scheduleRows(200)

	_, err := svc.ListSchedulesByDepartmentDate(context.Background(), 1, "", "", 999_999)

	require.NoError(t, err)
	assert.Equal(t, model.MaxPageSize, schedules.lastLimit)
}

func TestCatalog_SchedulesByDepartment_PaginationFlags(t *testing.T) {
	svc, _, _, schedules := newTestCatalogService(t)
	schedules.rows = scheduleRows(5) // id 5,4,3,2,1

	page, err := svc.ListSchedulesByDepartmentDate(context.Background(), 1, "", "", 2)

	require.NoError(t, err)
	assert.Len(t, page.Items, 2, "多取的探测行不能返回给客户端")
	assert.True(t, page.HasMore)
	assert.Equal(t, uint64(4), page.NextCursor, "游标应是本页最后一条的 id")

	// 用游标取下一页：仓储应收到该游标
	_, err = svc.ListSchedulesByDepartmentDate(context.Background(), 1, "", "4", 2)
	require.NoError(t, err)
	assert.Equal(t, uint64(4), schedules.lastCursor)
}

func TestCatalog_SchedulesByDepartment_LastPage(t *testing.T) {
	svc, _, _, schedules := newTestCatalogService(t)
	schedules.rows = scheduleRows(3)

	page, err := svc.ListSchedulesByDepartmentDate(context.Background(), 1, "", "", 5)

	require.NoError(t, err)
	assert.Len(t, page.Items, 3)
	assert.False(t, page.HasMore)
	assert.Zero(t, page.NextCursor, "没有下一页时不应给出游标")
}

func TestCatalog_SchedulesByDoctor_DefaultRangeIsSevenDays(t *testing.T) {
	svc, _, _, schedules := newTestCatalogService(t)

	_, err := svc.ListSchedulesByDoctor(context.Background(), 1, "", "", "", 20)

	require.NoError(t, err)
	from := schedules.lastFrom.Format(time.DateOnly)
	to := schedules.lastTo.Format(time.DateOnly)
	assert.Equal(t, time.Now().Format(time.DateOnly), from)

	expectedTo := time.Now().AddDate(0, 0, defaultScheduleRangeDays).Format(time.DateOnly)
	assert.Equal(t, expectedTo, to)
}

// 区间反向必须报错，否则会静默返回空列表，让人误以为"没有排班"。
func TestCatalog_SchedulesByDoctor_RejectsReversedRange(t *testing.T) {
	svc, _, _, _ := newTestCatalogService(t)

	_, err := svc.ListSchedulesByDoctor(context.Background(), 1, "2026-10-01", "2026-09-01", "", 20)

	assert.Equal(t, errcode.ErrInvalidParam, apperr.Code(err))
}

// --- 仓储错误必须被包装成 500，不得泄漏内部细节 ---

func TestCatalog_StoreErrorsBecomeInternal(t *testing.T) {
	storeErr := errors.New("connection refused")

	t.Run("科室列表", func(t *testing.T) {
		svc, departments, _, _ := newTestCatalogService(t)
		departments.err = storeErr

		_, err := svc.ListDepartments(context.Background())
		assert.Equal(t, errcode.ErrInternal, apperr.Code(err))
		assert.NotContains(t, apperr.Message(err), "connection refused", "内部错误细节不得下发给客户端")
	})

	t.Run("医生列表", func(t *testing.T) {
		svc, _, doctors, _ := newTestCatalogService(t)
		doctors.err = storeErr

		_, err := svc.ListDoctorsByDepartment(context.Background(), 1)
		assert.Equal(t, errcode.ErrInternal, apperr.Code(err))
	})

	t.Run("排班列表", func(t *testing.T) {
		svc, _, _, schedules := newTestCatalogService(t)
		schedules.err = storeErr

		_, err := svc.ListSchedulesByDepartmentDate(context.Background(), 1, "", "", 20)
		assert.Equal(t, errcode.ErrInternal, apperr.Code(err))
	})
}
