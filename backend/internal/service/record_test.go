package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/medicore/backend/internal/apperr"
	"github.com/medicore/backend/internal/errcode"
	"github.com/medicore/backend/internal/model"
	"github.com/medicore/backend/internal/repository"
)

// --- 内存实现 ---

type fakeRecordStore struct {
	created *model.MedicalRecord
	items   []model.PrescriptionItem
	view    *model.MedicalRecordView
	lists   map[uint64][]model.MedicalRecordListItem // key: patientID
	dList   []model.MedicalRecordListItem
	allList []model.MedicalRecordListItem

	createErr    error
	viewErr      error
	doctorIDByU  map[uint64]uint64
	doctorIDErr  error
	lastListKind string
}

func newFakeRecordStore() *fakeRecordStore {
	return &fakeRecordStore{doctorIDByU: map[uint64]uint64{}}
}

func (f *fakeRecordStore) CreateWithPrescriptions(
	_ context.Context, record *model.MedicalRecord, items []model.PrescriptionItem,
) error {
	if f.createErr != nil {
		return f.createErr
	}
	record.ID = 1
	f.created = record
	f.items = items
	return nil
}

func (f *fakeRecordStore) FindViewByAppointment(context.Context, uint64) (*model.MedicalRecordView, error) {
	if f.viewErr != nil {
		return nil, f.viewErr
	}
	if f.view == nil {
		return nil, repository.ErrNotFound
	}
	clone := *f.view
	return &clone, nil
}

func (f *fakeRecordStore) ListPrescriptions(context.Context, uint64) ([]model.PrescriptionItemView, error) {
	out := make([]model.PrescriptionItemView, 0, len(f.items))
	for _, it := range f.items {
		out = append(out, model.PrescriptionItemView{DrugName: it.DrugName, Quantity: it.Quantity})
	}
	return out, nil
}

func (f *fakeRecordStore) ListByPatientCursor(
	_ context.Context, patientID, _ uint64, _ int,
) ([]model.MedicalRecordListItem, error) {
	f.lastListKind = "patient"
	return f.lists[patientID], nil
}

func (f *fakeRecordStore) ListByDoctorCursor(context.Context, uint64, uint64, int) ([]model.MedicalRecordListItem, error) {
	f.lastListKind = "doctor"
	return f.dList, nil
}

func (f *fakeRecordStore) ListAllCursor(context.Context, uint64, int) ([]model.MedicalRecordListItem, error) {
	f.lastListKind = "all"
	return f.allList, nil
}

type fakeAppointmentReader struct {
	appt *model.Appointment
	err  error
}

func (f *fakeAppointmentReader) FindByID(context.Context, uint64) (*model.Appointment, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.appt == nil {
		return nil, repository.ErrNotFound
	}
	clone := *f.appt
	return &clone, nil
}

// --- 组装 ---

const (
	doctorUserID = uint64(10)
	doctorID     = uint64(100)
	otherDoctor  = uint64(200)
	patientID    = uint64(20)
	otherPatient = uint64(21)
)

type recordFixture struct {
	svc   *RecordService
	store *fakeRecordStore
	appts *fakeAppointmentReader
}

func newRecordFixture(t *testing.T) *recordFixture {
	t.Helper()

	store := newFakeRecordStore()
	store.doctorIDByU[doctorUserID] = doctorID
	store.doctorIDByU[77] = otherDoctor

	appts := &fakeAppointmentReader{appt: &model.Appointment{
		ID:       1,
		UserID:   patientID,
		DoctorID: doctorID,
		Status:   model.AppointmentStatusConfirmed,
	}}

	// doctorLookup 现在由 DoctorRepo 提供，不再是 recordStore 的方法。
	// 用 byUser 映射而非单值：测试里有"其他医生"的场景，单值无法表达。
	svc := NewRecordService(RecordDeps{
		Records:      store,
		Appointments: appts,
		Doctors:      &fakeDoctorLookup{byUser: store.doctorIDByU},
	})
	return &recordFixture{svc: svc, store: store, appts: appts}
}

func doctorAccess(userID uint64) Access {
	return Access{UserID: userID, Role: model.RoleDoctor}
}

func patientAccess(userID uint64) Access {
	return Access{UserID: userID, Role: model.RolePatient}
}

func adminAccess() Access {
	return Access{UserID: 1, Role: model.RoleAdmin}
}

func writeRequest() model.WriteRecordRequest {
	return model.WriteRecordRequest{
		ChiefComplaint: "上腹隐痛 3 天",
		Diagnosis:      "慢性胃炎",
		Prescriptions: []model.PrescriptionItemInput{
			{DrugName: "奥美拉唑", Quantity: 1, UsageDesc: "每日一次"},
		},
	}
}

// --- 书写病历 ---

func TestRecord_Write_Success(t *testing.T) {
	f := newRecordFixture(t)
	f.store.view = &model.MedicalRecordView{ID: 1, PatientID: patientID, DoctorID: doctorID}

	view, err := f.svc.Write(context.Background(), doctorAccess(doctorUserID), 1, writeRequest())

	require.NoError(t, err)
	assert.Equal(t, uint64(1), view.ID)
	require.NotNil(t, f.store.created)
	assert.Equal(t, patientID, f.store.created.PatientID, "患者应取自预约，而不是请求体")
	assert.Len(t, f.store.items, 1)
	assert.Equal(t, 0, f.store.items[0].SortOrder, "处方明细序号应按请求顺序生成")
}

// 患者不能写病历——这是"路由层 RBAC + service 二次确认"的体现。
func TestRecord_Write_OnlyDoctor(t *testing.T) {
	f := newRecordFixture(t)

	_, err := f.svc.Write(context.Background(), patientAccess(patientID), 1, writeRequest())

	assert.Equal(t, errcode.ErrForbidden, apperr.Code(err))
	assert.Nil(t, f.store.created)
}

// 最关键的一条：医生只能给自己接诊的预约写病历。
// 缺了它，任何医生都能给别人的患者下诊断——这是医疗系统里最严重的越权类型。
func TestRecord_Write_RejectsOtherDoctorsPatient(t *testing.T) {
	f := newRecordFixture(t)
	f.store.doctorIDByU[77] = otherDoctor

	_, err := f.svc.Write(context.Background(), doctorAccess(77), 1, writeRequest())

	assert.Equal(t, errcode.ErrNotOwner, apperr.Code(err))
	assert.Nil(t, f.store.created, "越权时绝不能落库")
}

func TestRecord_Write_RequiresDoctorProfile(t *testing.T) {
	f := newRecordFixture(t)
	// 用户 999 没有医生档案
	_, err := f.svc.Write(context.Background(), doctorAccess(999), 1, writeRequest())

	assert.Equal(t, errcode.ErrForbidden, apperr.Code(err))
}

func TestRecord_Write_AppointmentNotFound(t *testing.T) {
	f := newRecordFixture(t)
	f.appts.appt = nil

	_, err := f.svc.Write(context.Background(), doctorAccess(doctorUserID), 1, writeRequest())

	assert.Equal(t, errcode.ErrNotFound, apperr.Code(err))
}

func TestRecord_Write_RejectsInactiveAppointment(t *testing.T) {
	for _, status := range []string{
		model.AppointmentStatusCancelled,
		model.AppointmentStatusExpired,
	} {
		t.Run(status, func(t *testing.T) {
			f := newRecordFixture(t)
			f.appts.appt.Status = status

			_, err := f.svc.Write(context.Background(), doctorAccess(doctorUserID), 1, writeRequest())

			assert.Equal(t, errcode.ErrConflict, apperr.Code(err))
			assert.Nil(t, f.store.created)
		})
	}
}

func TestRecord_Write_DuplicateIsConflict(t *testing.T) {
	f := newRecordFixture(t)
	f.store.createErr = repository.ErrRecordExists

	_, err := f.svc.Write(context.Background(), doctorAccess(doctorUserID), 1, writeRequest())

	assert.Equal(t, errcode.ErrConflict, apperr.Code(err))
}

// --- 查看病历（资源层归属校验）---

func TestRecord_Get_OwnershipMatrix(t *testing.T) {
	tests := []struct {
		name     string
		access   Access
		wantCode errcode.Code // 0 表示应当成功
	}{
		{"患者看自己的病历", patientAccess(patientID), 0},
		{"患者看别人的病历", patientAccess(otherPatient), errcode.ErrNotOwner},
		{"接诊医生看自己患者的病历", doctorAccess(doctorUserID), 0},
		{"非接诊医生看他人病历", doctorAccess(77), errcode.ErrNotOwner},
		{"管理员看任意病历", adminAccess(), 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newRecordFixture(t)
			f.store.view = &model.MedicalRecordView{
				ID: 1, PatientID: patientID, DoctorID: doctorID,
			}

			view, err := f.svc.Get(context.Background(), tt.access, 1)

			if tt.wantCode != 0 {
				assert.Equal(t, tt.wantCode, apperr.Code(err))
				assert.Nil(t, view)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, uint64(1), view.ID)
		})
	}
}

// 处方明细必须随病历一起返回，否则医生看到的是不完整的诊疗记录。
func TestRecord_Get_IncludesPrescriptions(t *testing.T) {
	f := newRecordFixture(t)
	f.store.view = &model.MedicalRecordView{ID: 1, PatientID: patientID, DoctorID: doctorID}
	f.store.items = []model.PrescriptionItem{{DrugName: "奥美拉唑", Quantity: 2}}

	view, err := f.svc.Get(context.Background(), adminAccess(), 1)

	require.NoError(t, err)
	require.Len(t, view.Prescriptions, 1)
	assert.Equal(t, "奥美拉唑", view.Prescriptions[0].DrugName)
}

func TestRecord_Get_NotFound(t *testing.T) {
	f := newRecordFixture(t)
	f.store.view = nil

	_, err := f.svc.Get(context.Background(), adminAccess(), 1)

	assert.Equal(t, errcode.ErrNotFound, apperr.Code(err))
}

// 权限校验必须在读取处方之前完成：否则会先做一次无谓的查询，
// 更重要的是让"越权也能触发副作用"成为可能。
func TestRecord_Get_AuthorizesBeforeLoadingPrescriptions(t *testing.T) {
	f := newRecordFixture(t)
	f.store.view = &model.MedicalRecordView{ID: 1, PatientID: patientID, DoctorID: doctorID}
	f.store.items = []model.PrescriptionItem{{DrugName: "不该被读到"}}

	view, err := f.svc.Get(context.Background(), patientAccess(otherPatient), 1)

	assert.Error(t, err)
	assert.Nil(t, view)
}

// --- 列表 ---

func TestRecord_ListMine_RoutesByRole(t *testing.T) {
	tests := []struct {
		name     string
		access   Access
		wantKind string
	}{
		{"患者查自己的", patientAccess(patientID), "patient"},
		{"医生查自己书写的", doctorAccess(doctorUserID), "doctor"},
		{"管理员查全部", adminAccess(), "all"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newRecordFixture(t)

			_, err := f.svc.ListMine(context.Background(), tt.access, "", 20)

			require.NoError(t, err)
			assert.Equal(t, tt.wantKind, f.store.lastListKind)
		})
	}
}

func TestRecord_ListMine_UnknownRoleIsForbidden(t *testing.T) {
	f := newRecordFixture(t)

	_, err := f.svc.ListMine(context.Background(), Access{UserID: 1, Role: "guest"}, "", 20)

	assert.Equal(t, errcode.ErrForbidden, apperr.Code(err))
}

func TestRecord_ListMine_DoctorWithoutProfile(t *testing.T) {
	f := newRecordFixture(t)

	_, err := f.svc.ListMine(context.Background(), doctorAccess(999), "", 20)

	assert.Equal(t, errcode.ErrForbidden, apperr.Code(err))
}

func TestRecord_ListMine_RejectsBadCursor(t *testing.T) {
	f := newRecordFixture(t)

	_, err := f.svc.ListMine(context.Background(), adminAccess(), "abc", 20)

	assert.Equal(t, errcode.ErrInvalidParam, apperr.Code(err))
}

// 空串字段应写成 NULL 而不是空字符串：两者在索引与查询上行为不同。
func TestRecord_Write_BlankFieldsBecomeNull(t *testing.T) {
	f := newRecordFixture(t)
	f.store.view = &model.MedicalRecordView{ID: 1, PatientID: patientID, DoctorID: doctorID}

	req := model.WriteRecordRequest{Diagnosis: "慢性胃炎", ChiefComplaint: "   "}
	_, err := f.svc.Write(context.Background(), doctorAccess(doctorUserID), 1, req)

	require.NoError(t, err)
	require.NotNil(t, f.store.created)
	assert.Nil(t, f.store.created.ChiefComplaint, "空白主诉应存为 NULL")
}
