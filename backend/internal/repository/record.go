package repository

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"github.com/medicore/backend/internal/model"
)

// ErrRecordExists 表示该预约已有病历。
var ErrRecordExists = errors.New("medical record already exists")

// MedicalRecordRepo 是 medical_records / prescription_items 的数据访问。
type MedicalRecordRepo struct {
	db *gorm.DB
}

// NewMedicalRecordRepo 构造 MedicalRecordRepo。
func NewMedicalRecordRepo(db *gorm.DB) *MedicalRecordRepo {
	return &MedicalRecordRepo{db: db}
}

// CreateWithPrescriptions 在一个事务中写入病历与处方明细。
//
// 必须同事务：分开提交会出现"病历已存但处方没存"的中间态，
// 而医生看到的是一份缺了用药的诊疗记录——这在临床上是危险的。
func (r *MedicalRecordRepo) CreateWithPrescriptions(
	ctx context.Context, record *model.MedicalRecord, items []model.PrescriptionItem,
) error {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(record).Error; err != nil {
			if errors.Is(err, gorm.ErrDuplicatedKey) {
				return fmt.Errorf("create medical record: %w", ErrRecordExists)
			}
			return fmt.Errorf("create medical record: %w", err)
		}

		if len(items) == 0 {
			return nil
		}
		for i := range items {
			items[i].RecordID = record.ID
			items[i].SortOrder = i
		}
		if err := tx.Create(&items).Error; err != nil {
			return fmt.Errorf("create prescription items: %w", err)
		}
		return nil
	})
	return err
}

// recordViewSelect 统一病历视图的列，避免多处出现不一致的别名。
const recordViewSelect = `r.id, r.appointment_id, r.patient_id,
	COALESCE(p.name, '') AS patient_name,
	r.doctor_id,
	COALESCE(doc.name, '') AS doctor_name,
	COALESCE(dp.name, '') AS department_name,
	DATE_FORMAT(a.sched_date, '%Y-%m-%d') AS sched_date,
	COALESCE(r.chief_complaint, '') AS chief_complaint,
	COALESCE(r.present_illness, '') AS present_illness,
	r.diagnosis,
	COALESCE(r.treatment_advice, '') AS treatment_advice,
	DATE_FORMAT(r.created_at, '%Y-%m-%d %H:%i:%s') AS created_at`

// recordViewJoins 是病历视图所需的关联。
//
// 用 LEFT JOIN 取姓名：医生或患者被停用/软删后，
// 历史病历仍应显示当时的姓名，而不是空白。
const recordViewJoins = `medical_records AS r
	JOIN appointments AS a ON a.id = r.appointment_id
	LEFT JOIN users AS p ON p.id = r.patient_id
	LEFT JOIN doctors AS d ON d.id = r.doctor_id
	LEFT JOIN users AS doc ON doc.id = d.user_id
	LEFT JOIN departments AS dp ON dp.id = a.department_id`

// FindViewByAppointment 返回某预约的病历视图。
func (r *MedicalRecordRepo) FindViewByAppointment(
	ctx context.Context, appointmentID uint64,
) (*model.MedicalRecordView, error) {
	var view model.MedicalRecordView

	err := r.db.WithContext(ctx).
		Table(recordViewJoins).
		Select(recordViewSelect).
		Where("r.appointment_id = ? AND r.deleted_at IS NULL", appointmentID).
		Scan(&view).Error
	if err != nil {
		return nil, fmt.Errorf("find medical record: %w", err)
	}
	if view.ID == 0 {
		return nil, ErrNotFound
	}
	return &view, nil
}

// FindByID 按主键查询病历实体。
func (r *MedicalRecordRepo) FindByID(ctx context.Context, id uint64) (*model.MedicalRecord, error) {
	var record model.MedicalRecord

	err := r.db.WithContext(ctx).First(&record, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("find medical record: %w", err)
	}
	return &record, nil
}

// ListPrescriptions 返回病历的处方明细，按书写顺序排列。
func (r *MedicalRecordRepo) ListPrescriptions(
	ctx context.Context, recordID uint64,
) ([]model.PrescriptionItemView, error) {
	var items []model.PrescriptionItemView

	err := r.db.WithContext(ctx).
		Table("prescription_items").
		Select(`drug_name, COALESCE(spec, '') AS spec, quantity,
		        COALESCE(usage_desc, '') AS usage_desc, days`).
		Where("record_id = ?", recordID).
		Order("sort_order ASC").
		Scan(&items).Error
	if err != nil {
		return nil, fmt.Errorf("list prescription items: %w", err)
	}
	return items, nil
}

// ListByPatientCursor 游标分页返回某患者的病历列表。
func (r *MedicalRecordRepo) ListByPatientCursor(
	ctx context.Context, patientID, cursor uint64, limit int,
) ([]model.MedicalRecordListItem, error) {
	return r.listByCursor(ctx, "r.patient_id = ?", []any{patientID}, cursor, limit)
}

// ListByDoctorCursor 游标分页返回某医生书写的病历列表。
func (r *MedicalRecordRepo) ListByDoctorCursor(
	ctx context.Context, doctorID, cursor uint64, limit int,
) ([]model.MedicalRecordListItem, error) {
	return r.listByCursor(ctx, "r.doctor_id = ?", []any{doctorID}, cursor, limit)
}

// ListAllCursor 游标分页返回全部病历，供管理员使用（spec §2.1 全部读权限）。
func (r *MedicalRecordRepo) ListAllCursor(
	ctx context.Context, cursor uint64, limit int,
) ([]model.MedicalRecordListItem, error) {
	return r.listByCursor(ctx, "", nil, cursor, limit)
}

func (r *MedicalRecordRepo) listByCursor(
	ctx context.Context, where string, args []any, cursor uint64, limit int,
) ([]model.MedicalRecordListItem, error) {
	q := r.db.WithContext(ctx).
		Table(recordViewJoins).
		Select(`r.id, r.appointment_id, r.patient_id,
		        COALESCE(p.name, '') AS patient_name,
		        r.doctor_id,
		        COALESCE(doc.name, '') AS doctor_name,
		        COALESCE(dp.name, '') AS department_name,
		        DATE_FORMAT(a.sched_date, '%Y-%m-%d') AS sched_date,
		        r.diagnosis,
		        DATE_FORMAT(r.created_at, '%Y-%m-%d %H:%i:%s') AS created_at`).
		Where("r.deleted_at IS NULL")

	if where != "" {
		q = q.Where(where, args...)
	}
	if cursor > 0 {
		q = q.Where("r.id < ?", cursor)
	}

	var items []model.MedicalRecordListItem
	if err := q.Order("r.id DESC").Limit(limit + 1).Scan(&items).Error; err != nil {
		return nil, fmt.Errorf("list medical records: %w", err)
	}
	return items, nil
}

// FindDoctorIDByUserID 返回医生档案 ID。
//
// 病历表存的是 doctors.id 而非 users.id，因此资源归属校验必须先做这次转换。
func (r *MedicalRecordRepo) FindDoctorIDByUserID(ctx context.Context, userID uint64) (uint64, error) {
	var doctorID uint64

	err := r.db.WithContext(ctx).
		Model(&model.Doctor{}).
		Where("user_id = ? AND status = ?", userID, model.StatusActive).
		Limit(1).
		Pluck("id", &doctorID).Error
	if err != nil {
		return 0, fmt.Errorf("find doctor by user: %w", err)
	}
	if doctorID == 0 {
		return 0, ErrNotFound
	}
	return doctorID, nil
}
