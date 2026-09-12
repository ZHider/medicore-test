package model

import (
	"time"

	"gorm.io/gorm"
)

// MedicalRecord 对应 medical_records 表。
//
// 医疗数据只软删不物理删：即使医生写错，也要保留可追溯的记录。
type MedicalRecord struct {
	ID              uint64         `gorm:"column:id;primaryKey"`
	AppointmentID   uint64         `gorm:"column:appointment_id"`
	PatientID       uint64         `gorm:"column:patient_id"`
	DoctorID        uint64         `gorm:"column:doctor_id"`
	ChiefComplaint  *string        `gorm:"column:chief_complaint"`
	PresentIllness  *string        `gorm:"column:present_illness"`
	Diagnosis       string         `gorm:"column:diagnosis"`
	TreatmentAdvice *string        `gorm:"column:treatment_advice"`
	DeletedAt       gorm.DeletedAt `gorm:"column:deleted_at;index"`
	CreatedAt       time.Time      `gorm:"column:created_at"`
	UpdatedAt       time.Time      `gorm:"column:updated_at"`
}

// TableName 指定表名。
func (MedicalRecord) TableName() string { return "medical_records" }

// PrescriptionItem 对应 prescription_items 表。
type PrescriptionItem struct {
	ID        uint64    `gorm:"column:id;primaryKey"`
	RecordID  uint64    `gorm:"column:record_id"`
	DrugName  string    `gorm:"column:drug_name"`
	Spec      *string   `gorm:"column:spec"`
	Quantity  int       `gorm:"column:quantity"`
	UsageDesc *string   `gorm:"column:usage_desc"`
	Days      *int      `gorm:"column:days"`
	SortOrder int       `gorm:"column:sort_order"`
	CreatedAt time.Time `gorm:"column:created_at"`
}

// TableName 指定表名。
func (PrescriptionItem) TableName() string { return "prescription_items" }

// ---------------------------------------------------------------------------
// DTO
// ---------------------------------------------------------------------------

// PrescriptionItemInput 是处方明细的写入项。
type PrescriptionItemInput struct {
	DrugName  string `json:"drug_name" binding:"required,max=128"`
	Spec      string `json:"spec" binding:"omitempty,max=64"`
	Quantity  int    `json:"quantity" binding:"required,min=1,max=999"`
	UsageDesc string `json:"usage_desc" binding:"omitempty,max=255"`
	Days      *int   `json:"days" binding:"omitempty,min=1,max=365"`
}

// WriteRecordRequest 是医生书写病历的请求。
//
// 处方明细与病历同属一次写入：分开提交会出现"病历已存但处方没存"的中间态，
// 而这两者在业务上是一个整体。
type WriteRecordRequest struct {
	ChiefComplaint  string                  `json:"chief_complaint" binding:"omitempty,max=512"`
	PresentIllness  string                  `json:"present_illness" binding:"omitempty,max=2000"`
	Diagnosis       string                  `json:"diagnosis" binding:"required,min=2,max=512"`
	TreatmentAdvice string                  `json:"treatment_advice" binding:"omitempty,max=2000"`
	Prescriptions   []PrescriptionItemInput `json:"prescriptions" binding:"omitempty,max=20,dive"`
}

// PrescriptionItemView 是处方明细的对外表示。
type PrescriptionItemView struct {
	DrugName  string `gorm:"column:drug_name" json:"drug_name"`
	Spec      string `gorm:"column:spec" json:"spec,omitempty"`
	Quantity  int    `gorm:"column:quantity" json:"quantity"`
	UsageDesc string `gorm:"column:usage_desc" json:"usage_desc,omitempty"`
	Days      *int   `gorm:"column:days" json:"days,omitempty"`
}

// MedicalRecordView 是病历的对外表示，含患者与医生姓名。
type MedicalRecordView struct {
	ID              uint64 `gorm:"column:id" json:"id"`
	AppointmentID   uint64 `gorm:"column:appointment_id" json:"appointment_id"`
	PatientID       uint64 `gorm:"column:patient_id" json:"patient_id"`
	PatientName     string `gorm:"column:patient_name" json:"patient_name"`
	DoctorID        uint64 `gorm:"column:doctor_id" json:"doctor_id"`
	DoctorName      string `gorm:"column:doctor_name" json:"doctor_name"`
	DepartmentName  string `gorm:"column:department_name" json:"department_name"`
	SchedDate       string `gorm:"column:sched_date" json:"sched_date"`
	ChiefComplaint  string `gorm:"column:chief_complaint" json:"chief_complaint,omitempty"`
	PresentIllness  string `gorm:"column:present_illness" json:"present_illness,omitempty"`
	Diagnosis       string `gorm:"column:diagnosis" json:"diagnosis"`
	TreatmentAdvice string `gorm:"column:treatment_advice" json:"treatment_advice,omitempty"`
	CreatedAt       string `gorm:"column:created_at" json:"created_at"`

	Prescriptions []PrescriptionItemView `gorm:"-" json:"prescriptions"`
}

// MedicalRecordListItem 是病历列表项（不含完整内容，避免列表接口过重）。
type MedicalRecordListItem struct {
	ID            uint64 `gorm:"column:id" json:"id"`
	AppointmentID uint64 `gorm:"column:appointment_id" json:"appointment_id"`
	PatientID     uint64 `gorm:"column:patient_id" json:"patient_id"`
	PatientName   string `gorm:"column:patient_name" json:"patient_name"`
	DoctorID      uint64 `gorm:"column:doctor_id" json:"doctor_id"`
	DoctorName    string `gorm:"column:doctor_name" json:"doctor_name"`
	Department    string `gorm:"column:department_name" json:"department_name"`
	SchedDate     string `gorm:"column:sched_date" json:"sched_date"`
	Diagnosis     string `gorm:"column:diagnosis" json:"diagnosis"`
	CreatedAt     string `gorm:"column:created_at" json:"created_at"`
}

// GetID 供游标分页取 ID。
func (m MedicalRecordListItem) GetID() uint64 { return m.ID }
