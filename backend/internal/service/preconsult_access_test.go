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

// fakeReportStore 是 reportStore 的内存实现。
// 归属校验测试只需要"存在/不存在"两种状态，因此实现保持最小
// —— 这正是"接口由使用方定义"的好处。
type fakeReportStore struct {
	exists bool
}

func (f *fakeReportStore) CreatePending(
	context.Context, uint64, uint64, string, string, string,
) (*model.AIReport, bool, error) {
	return &model.AIReport{ID: 1}, true, nil
}

func (f *fakeReportStore) FindByAppointment(context.Context, uint64) (*model.AIReport, error) {
	if !f.exists {
		return nil, repository.ErrNotFound
	}
	return &model.AIReport{
		ID:            1,
		AppointmentID: 55,
		UserID:        100,
		Status:        model.AIReportStatusDone,
		Conversation:  "[]",
	}, nil
}

func (f *fakeReportStore) FindByID(context.Context, uint64) (*model.AIReport, error) {
	return nil, repository.ErrNotFound
}

func (f *fakeReportStore) MarkProcessing(context.Context, uint64) (bool, error) {
	return true, nil
}

func (f *fakeReportStore) SaveOutcome(context.Context, uint64, repository.SummaryOutcomeRow) error {
	return nil
}

// reportAccessFixture 组装一个"接诊医生是 doctorID=7、患者是 userID=100"的场景。
type reportAccessFixture struct {
	svc    *PreconsultService
	appts  *fakeAppointmentStore
	doctor *fakeDoctorLookup
	conv   *fakeConversationStore
}

func newReportAccessFixture(t *testing.T, reportExists bool) *reportAccessFixture {
	t.Helper()

	appts := &fakeAppointmentStore{
		appointmentByID: &model.Appointment{ID: 55, UserID: 100, DoctorID: 7},
	}
	doctor := &fakeDoctorLookup{doctorID: 7}
	conv := newFakeConversationStore()

	reports := &fakeReportStore{exists: reportExists}

	svc := NewPreconsultService(PreconsultDeps{
		Appointments: appts,
		Conversation: conv,
		Reports:      reports,
		Doctors:      doctor,
	})
	return &reportAccessFixture{svc: svc, appts: appts, doctor: doctor, conv: conv}
}

// --- 报告读取的归属校验（spec §2.2 双层校验的第二层）---

// 患者只能读自己的报告。少了这一条，任何人改一下 URL 就能读到别人的病情描述。
func TestReport_PatientOwnershipEnforced(t *testing.T) {
	t.Run("本人可读", func(t *testing.T) {
		f := newReportAccessFixture(t, true)

		_, err := f.svc.Report(context.Background(), patientAccess(100), 55)

		require.NoError(t, err)
	})

	t.Run("他人被拒", func(t *testing.T) {
		f := newReportAccessFixture(t, true)

		_, err := f.svc.Report(context.Background(), patientAccess(999), 55)

		assert.Equal(t, errcode.ErrNotOwner, apperr.Code(err))
	})
}

// 医生只能读**本人接诊**患者的报告 —— 不是任意患者的。
// 这是本次补的最关键一条：仅凭"是医生"这个角色无法判断资源归属。
func TestReport_DoctorSeesOnlyOwnPatients(t *testing.T) {
	t.Run("接诊医生可读", func(t *testing.T) {
		f := newReportAccessFixture(t, true)
		f.doctor.doctorID = 7 // 与预约的 doctor_id 一致

		_, err := f.svc.Report(context.Background(), doctorAccess(200), 55)

		require.NoError(t, err)
	})

	t.Run("非接诊医生被拒", func(t *testing.T) {
		f := newReportAccessFixture(t, true)
		f.doctor.doctorID = 8 // 与预约的 doctor_id 不一致

		_, err := f.svc.Report(context.Background(), doctorAccess(200), 55)

		assert.Equal(t, errcode.ErrNotOwner, apperr.Code(err),
			"非接诊医生不得查看他人患者的预问诊报告")
	})
}

// 管理员拥有全部读权限（spec §2.1），且不需要医生档案。
func TestReport_AdminCanReadAny(t *testing.T) {
	f := newReportAccessFixture(t, true)
	f.doctor.err = repository.ErrNotFound // 管理员没有医生档案也不该受影响

	_, err := f.svc.Report(context.Background(), adminAccess(), 55)

	require.NoError(t, err)
}

// 没有医生档案的账号应明确拒绝，而不是继续往下走。
func TestReport_DoctorWithoutProfileIsForbidden(t *testing.T) {
	f := newReportAccessFixture(t, true)
	f.doctor.err = repository.ErrNotFound

	_, err := f.svc.Report(context.Background(), doctorAccess(200), 55)

	assert.Equal(t, errcode.ErrForbidden, apperr.Code(err))
}

func TestReport_UnknownRoleIsForbidden(t *testing.T) {
	f := newReportAccessFixture(t, true)

	_, err := f.svc.Report(context.Background(), Access{UserID: 1, Role: "guest"}, 55)

	assert.Equal(t, errcode.ErrForbidden, apperr.Code(err))
}

func TestReport_UnknownAppointmentIsNotFound(t *testing.T) {
	f := newReportAccessFixture(t, true)
	f.appts.appointmentByID = nil
	f.appts.findByIDErr = repository.ErrNotFound

	_, err := f.svc.Report(context.Background(), patientAccess(100), 999)

	assert.Equal(t, errcode.ErrNotFound, apperr.Code(err))
}

// 报告不存在时给出可理解的提示，而不是空对象。
func TestReport_MissingReportIsNotFound(t *testing.T) {
	f := newReportAccessFixture(t, false)

	_, err := f.svc.Report(context.Background(), patientAccess(100), 55)

	assert.Equal(t, errcode.ErrNotFound, apperr.Code(err))
}

// --- 病历归属校验回归（Access 重命名后确认行为未变）---

func TestRecordAccess_AdminAndPatientPaths(t *testing.T) {
	f := newRecordFixture(t)

	t.Run("患者读自己的病历", func(t *testing.T) {
		f.store.view = &model.MedicalRecordView{
			ID: 1, PatientID: 100, DoctorID: 7, Diagnosis: "胃炎",
		}
		_, err := f.svc.Get(context.Background(), patientAccess(100), 55)
		require.NoError(t, err)
	})

	t.Run("患者读他人病历被拒", func(t *testing.T) {
		f.store.view = &model.MedicalRecordView{
			ID: 1, PatientID: 100, DoctorID: 7, Diagnosis: "胃炎",
		}
		_, err := f.svc.Get(context.Background(), patientAccess(999), 55)
		assert.Equal(t, errcode.ErrNotOwner, apperr.Code(err))
	})

	t.Run("管理员可读任意病历", func(t *testing.T) {
		f.store.view = &model.MedicalRecordView{
			ID: 1, PatientID: 100, DoctorID: 7, Diagnosis: "胃炎",
		}
		_, err := f.svc.Get(context.Background(), adminAccess(), 55)
		require.NoError(t, err)
	})
}
