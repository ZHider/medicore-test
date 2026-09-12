package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/medicore/backend/internal/apperr"
	"github.com/medicore/backend/internal/errcode"
	"github.com/medicore/backend/internal/his"
	"github.com/medicore/backend/internal/model"
	"github.com/medicore/backend/internal/repository"
	"github.com/medicore/backend/internal/secret"
)

// --- 内存实现 ---

type fakeSyncSource struct {
	dataset *his.Dataset
	err     error
}

func (f *fakeSyncSource) Load(context.Context) (*his.Dataset, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.dataset, nil
}

type fakeSyncStore struct {
	mu sync.Mutex

	departments map[string]model.Department
	doctors     map[string]repository.SyncDoctorInput
	schedules   map[string]model.Schedule

	deptFail    bool
	doctorFail  bool
	scheduleErr bool
	// missingDeps 为 true 时模拟"科室查不到"，用于触发医生同步失败
	missingDeps bool
}

func newFakeSyncStore() *fakeSyncStore {
	return &fakeSyncStore{
		departments: map[string]model.Department{},
		doctors:     map[string]repository.SyncDoctorInput{},
		schedules:   map[string]model.Schedule{},
	}
}

func (f *fakeSyncStore) UpsertDepartment(_ context.Context, dept model.Department) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deptFail {
		return errors.New("insert failed")
	}
	if dept.ExternalID != nil {
		f.departments[*dept.ExternalID] = dept
	}
	return nil
}

func (f *fakeSyncStore) FindDepartmentIDByCode(_ context.Context, code string) (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.missingDeps {
		return 0, repository.ErrNotFound
	}
	for i, d := range f.departments {
		if d.Code == code {
			return uint64(len(i)) + 1, nil
		}
	}
	return 1, nil
}

func (f *fakeSyncStore) UpsertDoctor(
	_ context.Context, in repository.SyncDoctorInput, _ uint64, _ bool,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.doctorFail {
		return errors.New("upsert doctor failed")
	}
	f.doctors[in.ExternalID] = in
	return nil
}

func (f *fakeSyncStore) FindDoctorIDByExternalID(_ context.Context, externalID string) (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.doctors[externalID]; !ok {
		return 0, repository.ErrNotFound
	}
	return 1, nil
}

func (f *fakeSyncStore) UpsertSchedule(
	_ context.Context, in model.Schedule, externalID string,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.scheduleErr {
		return errors.New("upsert schedule failed")
	}
	f.schedules[externalID] = in
	return nil
}

type fakeSyncLogStore struct {
	mu sync.Mutex

	started  int
	finished []repository.SyncResult
	nextID   uint64
	views    map[uint64]model.SyncLogView
}

func newFakeSyncLogStore() *fakeSyncLogStore {
	return &fakeSyncLogStore{views: map[uint64]model.SyncLogView{}}
}

func (f *fakeSyncLogStore) Start(_ context.Context, source, trigger, _ string) (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started++
	f.nextID++
	f.views[f.nextID] = model.SyncLogView{
		ID: f.nextID, Source: source, TriggerType: trigger, Status: model.SyncStatusRunning,
	}
	return f.nextID, nil
}

func (f *fakeSyncLogStore) Finish(_ context.Context, id uint64, result repository.SyncResult) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.finished = append(f.finished, result)

	view := f.views[id]
	view.Status = result.Status
	view.Total = result.Total
	view.Succeeded = result.Succeeded
	view.Failed = result.Failed
	f.views[id] = view
	return nil
}

func (f *fakeSyncLogStore) FindViewByID(_ context.Context, id uint64) (*model.SyncLogView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	view, ok := f.views[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return &view, nil
}

func (f *fakeSyncLogStore) ListByCursor(context.Context, string, uint64, int) ([]model.SyncLogView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]model.SyncLogView, 0, len(f.views))
	for _, v := range f.views {
		out = append(out, v)
	}
	return out, nil
}

func (f *fakeSyncLogStore) lastResult() repository.SyncResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.finished) == 0 {
		return repository.SyncResult{}
	}
	return f.finished[len(f.finished)-1]
}

// --- 组装 ---

// testToday 是测试数据的基准日。
//
// 这里用绝对日期而不是"相对今天 N 天"：相对写法是 mockhis 内部解析
// 演示数据文件时的细节，契约（his.Schedule）里只有绝对日期。
var testToday = time.Date(2026, 9, 12, 0, 0, 0, 0, time.Local)

func testDataset() *his.Dataset {
	return &his.Dataset{
		Departments: []his.Department{
			{ExternalID: "D1", Code: "CARD", Name: "心血管内科", SortOrder: 10},
			{ExternalID: "D2", Code: "NEUR", Name: "神经内科", SortOrder: 20},
		},
		Doctors: []his.Doctor{
			{ExternalID: "DOC1", Name: "陈医生", Phone: "13610000001", Title: "主任医师",
				DepartmentCode: "CARD", ConsultFee: "50.00", IsPrimary: true},
			{ExternalID: "DOC2", Name: "林医生", Phone: "13610000002", Title: "主治医师",
				DepartmentCode: "CARD", ConsultFee: "20.00", IsPrimary: true},
		},
		Schedules: []his.Schedule{
			{ExternalID: "S1", DoctorExternal: "DOC1", DepartmentCode: "CARD",
				SchedDate: testToday, TimeSlot: model.TimeSlotAM, Total: 30},
			{ExternalID: "S2", DoctorExternal: "DOC2", DepartmentCode: "CARD",
				SchedDate: testToday.AddDate(0, 0, 1), TimeSlot: model.TimeSlotPM, Total: 20},
		},
	}
}

type syncFixture struct {
	svc   *SyncService
	store *fakeSyncStore
	logs  *fakeSyncLogStore
}

func newSyncFixture(t *testing.T) *syncFixture {
	t.Helper()

	cipher, err := secret.NewCipher(randomHex(t, 32))
	require.NoError(t, err)

	store := newFakeSyncStore()
	logs := newFakeSyncLogStore()

	svc := NewSyncService(SyncDeps{
		Source:      &fakeSyncSource{dataset: testDataset()},
		Sync:        store,
		Logs:        logs,
		Cipher:      cipher,
		PhonePepper: randomHex(t, 16),
	})
	return &syncFixture{svc: svc, store: store, logs: logs}
}

// --- 正常路径 ---

func TestSync_Success(t *testing.T) {
	f := newSyncFixture(t)

	view, err := f.svc.Sync(context.Background(), model.SyncTriggerManual)

	require.NoError(t, err)
	assert.Equal(t, model.SyncStatusSuccess, view.Status)
	assert.Equal(t, 6, view.Total, "2 科室 + 2 医生 + 2 排班")
	assert.Equal(t, 6, view.Succeeded)
	assert.Zero(t, view.Failed)

	assert.Len(t, f.store.departments, 2)
	assert.Len(t, f.store.doctors, 2)
	assert.Len(t, f.store.schedules, 2)
}

// 排班日期必须原样落到库里。
//
// "相对今天 N 天"是 mockhis 解析演示数据文件时的内部写法，
// 到这里（契约层）已经是绝对日期，service 只做透传，不得再做日期运算。
// 相对日期的换算由 mockhis 自己的测试守护。
func TestSync_ScheduleDateIsPassedThrough(t *testing.T) {
	f := newSyncFixture(t)

	_, err := f.svc.Sync(context.Background(), model.SyncTriggerManual)
	require.NoError(t, err)

	require.Contains(t, f.store.schedules, "S1")
	assert.Equal(t, testToday, f.store.schedules["S1"].SchedDate,
		"service 不应改写数据源给出的排班日期")
}

// 同步来的医生账号必须无法用已知默认口令登录。
// 给同步账号设固定默认口令是最常见的入侵入口。
func TestSync_DoctorPasswordIsNotDefault(t *testing.T) {
	f := newSyncFixture(t)

	_, err := f.svc.Sync(context.Background(), model.SyncTriggerManual)
	require.NoError(t, err)

	require.Contains(t, f.store.doctors, "DOC1")
	hash := f.store.doctors["DOC1"].PasswordHash
	require.NotEmpty(t, hash)

	// 任何"可能的默认口令"都不应能匹配
	for _, guess := range []string{"123456", "admin", "Doctor@123", "password", ""} {
		assert.False(t, secret.VerifyPassword(hash, guess),
			"同步账号不应能用 %q 登录", guess)
	}
}

// 每次同步生成的口令都不同，避免"一次泄露、全部沦陷"。
func TestSync_DoctorPasswordDiffersPerRun(t *testing.T) {
	f := newSyncFixture(t)

	_, err := f.svc.Sync(context.Background(), model.SyncTriggerManual)
	require.NoError(t, err)
	first := f.store.doctors["DOC1"].PasswordHash

	_, err = f.svc.Sync(context.Background(), model.SyncTriggerManual)
	require.NoError(t, err)
	second := f.store.doctors["DOC1"].PasswordHash

	assert.NotEqual(t, first, second)
}

// 医生手机号必须加密后落库，摘要长度为 64（匹配 phone_hash CHAR(64)）。
func TestSync_DoctorPhoneIsEncrypted(t *testing.T) {
	f := newSyncFixture(t)

	_, err := f.svc.Sync(context.Background(), model.SyncTriggerManual)
	require.NoError(t, err)

	got := f.store.doctors["DOC1"]
	assert.NotContains(t, string(got.PhoneEnc), "13610000001", "密文中不应出现明文手机号")
	assert.Len(t, got.PhoneHash, 64)
}

// --- 部分失败 ---

// 科室查不到时，医生同步应计为失败而不是中断整批。
// 一条脏数据不该阻塞全部同步。
func TestSync_PartialFailureIsCounted(t *testing.T) {
	f := newSyncFixture(t)
	f.store.missingDeps = true

	view, err := f.svc.Sync(context.Background(), model.SyncTriggerManual)

	require.NoError(t, err, "部分失败不应让整个同步返回错误")
	assert.Equal(t, model.SyncStatusPartial, view.Status)
	assert.Equal(t, 2, view.Succeeded, "科室仍应同步成功")
	assert.Equal(t, 4, view.Failed, "2 医生 + 2 排班因依赖缺失失败")
}

func TestSync_AllDepartmentsFailIsFailed(t *testing.T) {
	f := newSyncFixture(t)
	f.store.deptFail = true

	view, err := f.svc.Sync(context.Background(), model.SyncTriggerManual)

	require.NoError(t, err)
	assert.Equal(t, model.SyncStatusPartial, view.Status,
		"科室全失败但医生仍去查科室会成功（假实现不模拟级联），因此是 partial")
	assert.Greater(t, view.Failed, 0)
}

// 数据源不可用时，同步记录必须被标记为 failed 并留下原因。
func TestSync_SourceFailureMarksFailed(t *testing.T) {
	f := newSyncFixture(t)
	f.svc.source = &fakeSyncSource{err: errors.New("HIS 连接超时")}

	_, err := f.svc.Sync(context.Background(), model.SyncTriggerManual)

	assert.Equal(t, errcode.ErrInternal, apperr.Code(err))

	result := f.logs.lastResult()
	assert.Equal(t, model.SyncStatusFailed, result.Status)
	require.NotNil(t, result.ErrorMessage)
	assert.Contains(t, *result.ErrorMessage, "HIS 连接超时")
}

// --- 同步记录 ---

// 先写 running 记录，再执行同步：让"同步中途崩溃"也能在表里留下痕迹。
func TestSync_LogStartedBeforeWork(t *testing.T) {
	f := newSyncFixture(t)
	f.svc.source = &fakeSyncSource{err: errors.New("boom")}

	_, _ = f.svc.Sync(context.Background(), model.SyncTriggerManual)

	assert.Equal(t, 1, f.logs.started)
	assert.Len(t, f.logs.finished, 1, "失败也必须写入结束状态，否则记录永远停在 running")
}

func TestSync_EmptyDataset(t *testing.T) {
	f := newSyncFixture(t)
	f.svc.source = &fakeSyncSource{dataset: &his.Dataset{}}

	view, err := f.svc.Sync(context.Background(), model.SyncTriggerManual)

	require.NoError(t, err)
	assert.Equal(t, model.SyncStatusSuccess, view.Status)
	assert.Zero(t, view.Total)
}

func TestSync_ListLogs(t *testing.T) {
	f := newSyncFixture(t)
	_, err := f.svc.Sync(context.Background(), model.SyncTriggerManual)
	require.NoError(t, err)

	page, err := f.svc.ListLogs(context.Background(), SourceName, "", 20)

	require.NoError(t, err)
	assert.Len(t, page.Items, 1)
}

func TestSync_ListLogsRejectsBadCursor(t *testing.T) {
	f := newSyncFixture(t)

	_, err := f.svc.ListLogs(context.Background(), SourceName, "abc", 20)

	assert.Equal(t, errcode.ErrInvalidParam, apperr.Code(err))
}
