package service

import (
	"context"
	"errors"
	"strings"

	"go.uber.org/zap"

	"github.com/medicore/backend/internal/apperr"
	"github.com/medicore/backend/internal/errcode"
	"github.com/medicore/backend/internal/model"
	"github.com/medicore/backend/internal/repository"
)

// RecordDeps 是 RecordService 的依赖集合。
type RecordDeps struct {
	Records      recordStore
	Appointments appointmentReader
	Doctors      doctorLookup
	Log          *zap.Logger
}

// RecordService 承载病历书写与查询。
type RecordService struct {
	records      recordStore
	appointments appointmentReader
	doctors      doctorLookup
	log          *zap.Logger
}

// NewRecordService 构造 RecordService。
func NewRecordService(d RecordDeps) *RecordService {
	log := d.Log
	if log == nil {
		log = zap.NewNop()
	}
	return &RecordService{
		records:      d.Records,
		appointments: d.Appointments,
		doctors:      d.Doctors,
		log:          log,
	}
}

// Write 由接诊医生书写病历与处方。
func (s *RecordService) Write(
	ctx context.Context, access Access, appointmentID uint64, req model.WriteRecordRequest,
) (*model.MedicalRecordView, error) {
	if access.Role != model.RoleDoctor {
		return nil, apperr.New(errcode.ErrForbidden, "只有医生可以书写病历")
	}

	doctorID, err := s.doctorIDOf(ctx, access.UserID)
	if err != nil {
		return nil, err
	}

	appt, err := s.loadAppointment(ctx, appointmentID)
	if err != nil {
		return nil, err
	}

	// 资源层校验：只有该预约的接诊医生本人能写病历。
	// 少了这一条，任何医生都能给别人的患者下诊断。
	if appt.DoctorID != doctorID {
		return nil, apperr.New(errcode.ErrNotOwner, "只能为自己接诊的预约书写病历")
	}
	if !appt.IsActive() {
		return nil, apperr.New(errcode.ErrConflict, "当前预约状态不允许书写病历")
	}

	record := &model.MedicalRecord{
		AppointmentID: appointmentID,
		PatientID:     appt.UserID,
		DoctorID:      doctorID,
		Diagnosis:     strings.TrimSpace(req.Diagnosis),
	}
	record.ChiefComplaint = optionalStringPtr(req.ChiefComplaint)
	record.PresentIllness = optionalStringPtr(req.PresentIllness)
	record.TreatmentAdvice = optionalStringPtr(req.TreatmentAdvice)

	items := make([]model.PrescriptionItem, 0, len(req.Prescriptions))
	for _, p := range req.Prescriptions {
		items = append(items, model.PrescriptionItem{
			DrugName:  strings.TrimSpace(p.DrugName),
			Spec:      optionalStringPtr(p.Spec),
			Quantity:  p.Quantity,
			UsageDesc: optionalStringPtr(p.UsageDesc),
			Days:      p.Days,
		})
	}

	if err := s.records.CreateWithPrescriptions(ctx, record, items); err != nil {
		if errors.Is(err, repository.ErrRecordExists) {
			return nil, apperr.New(errcode.ErrConflict, "该预约已书写过病历，不可重复创建")
		}
		return nil, apperr.Wrap(errcode.ErrInternal, "保存病历失败", err)
	}

	return s.Get(ctx, access, appointmentID)
}

// Get 返回某预约的病历，并做资源层归属校验。
func (s *RecordService) Get(
	ctx context.Context, access Access, appointmentID uint64,
) (*model.MedicalRecordView, error) {
	view, err := s.records.FindViewByAppointment(ctx, appointmentID)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, apperr.New(errcode.ErrNotFound, "病历不存在")
	}
	if err != nil {
		return nil, apperr.Wrap(errcode.ErrInternal, "查询病历失败", err)
	}

	if err := s.authorize(ctx, access, view); err != nil {
		return nil, err
	}

	items, err := s.records.ListPrescriptions(ctx, view.ID)
	if err != nil {
		return nil, apperr.Wrap(errcode.ErrInternal, "查询处方失败", err)
	}
	view.Prescriptions = items
	return view, nil
}

// ListMine 游标分页返回当前身份可见的病历列表。
func (s *RecordService) ListMine(
	ctx context.Context, access Access, cursorRaw string, size int,
) (model.CursorPage[model.MedicalRecordListItem], error) {
	var empty model.CursorPage[model.MedicalRecordListItem]

	cursor, err := parseCursor(cursorRaw)
	if err != nil {
		return empty, err
	}
	limit := model.NormalizePageSize(size)

	var rows []model.MedicalRecordListItem
	switch access.Role {
	case model.RolePatient:
		rows, err = s.records.ListByPatientCursor(ctx, access.UserID, cursor, limit)
	case model.RoleDoctor:
		doctorID, dErr := s.doctorIDOf(ctx, access.UserID)
		if dErr != nil {
			return empty, dErr
		}
		rows, err = s.records.ListByDoctorCursor(ctx, doctorID, cursor, limit)
	case model.RoleAdmin:
		rows, err = s.records.ListAllCursor(ctx, cursor, limit)
	default:
		return empty, apperr.New(errcode.ErrForbidden, "当前角色无权查看病历")
	}

	if err != nil {
		return empty, apperr.Wrap(errcode.ErrInternal, "查询病历列表失败", err)
	}
	return model.NewCursorPage(rows, limit, model.MedicalRecordListItem.GetID), nil
}

// authorize 实现资源层归属校验。
func (s *RecordService) authorize(
	ctx context.Context, access Access, view *model.MedicalRecordView,
) error {
	switch access.Role {
	case model.RoleAdmin:
		// 管理员拥有全部读权限（spec §2.1）
		return nil

	case model.RolePatient:
		if view.PatientID != access.UserID {
			return apperr.New(errcode.ErrNotOwner, "无权查看他人病历")
		}
		return nil

	case model.RoleDoctor:
		doctorID, err := s.doctorIDOf(ctx, access.UserID)
		if err != nil {
			return err
		}
		if view.DoctorID != doctorID {
			return apperr.New(errcode.ErrNotOwner, "无权查看非本人接诊的病历")
		}
		return nil

	default:
		return apperr.New(errcode.ErrForbidden, "当前角色无权查看病历")
	}
}

// doctorIDOf 由用户 ID 解析医生档案 ID。
// 病历表存的是 doctors.id，直接拿 users.id 比较会永远不相等。
func (s *RecordService) doctorIDOf(ctx context.Context, userID uint64) (uint64, error) {
	doctorID, err := s.doctors.FindIDByUserID(ctx, userID)
	if errors.Is(err, repository.ErrNotFound) {
		return 0, apperr.New(errcode.ErrForbidden, "当前账号没有医生档案")
	}
	if err != nil {
		return 0, apperr.Wrap(errcode.ErrInternal, "查询医生档案失败", err)
	}
	return doctorID, nil
}

func (s *RecordService) loadAppointment(ctx context.Context, appointmentID uint64) (*model.Appointment, error) {
	appt, err := s.appointments.FindByID(ctx, appointmentID)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, apperr.New(errcode.ErrNotFound, "预约不存在")
	}
	if err != nil {
		return nil, apperr.Wrap(errcode.ErrInternal, "查询预约失败", err)
	}
	return appt, nil
}

// optionalStringPtr 把空串转成 nil，避免数据库里存一堆空字符串。
func optionalStringPtr(s string) *string {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}
