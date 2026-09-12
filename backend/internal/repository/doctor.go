package repository

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/medicore/backend/internal/model"
)

// 科室医生列表的缓存键与有效期。
// TTL 取 5 分钟：科室与医生档案变更频率极低，允许短暂脏读以换取读性能。
const (
	deptDoctorsKeyPrefix = "cache:dept_doctors:"
	deptDoctorsTTL       = 5 * time.Minute
)

// DoctorRepo 是 doctors / doctor_departments 的数据访问。
type DoctorRepo struct {
	db    *gorm.DB
	cache *Cache
}

// NewDoctorRepo 构造 DoctorRepo。
func NewDoctorRepo(db *gorm.DB, cache *Cache) *DoctorRepo {
	return &DoctorRepo{db: db, cache: cache}
}

// ListByDepartment 返回科室下的医生，走 Redis 缓存。
//
// spec §6.1 明确要求该接口使用 Redis 缓存：挂号首屏的科室医生列表
// 是全站最高频的查询之一，且数据几乎不变，是最典型的缓存对象。
func (r *DoctorRepo) ListByDepartment(ctx context.Context, departmentID uint64) ([]model.DoctorListItem, error) {
	key := deptDoctorsKey(departmentID)

	items, err := Load(ctx, r.cache, key, deptDoctorsTTL,
		func(ctx context.Context) ([]model.DoctorListItem, error) {
			return r.queryDoctorsByDepartment(ctx, departmentID)
		})
	if err != nil {
		return nil, fmt.Errorf("list doctors by department: %w", err)
	}
	return items, nil
}

func (r *DoctorRepo) queryDoctorsByDepartment(ctx context.Context, departmentID uint64) ([]model.DoctorListItem, error) {
	var items []model.DoctorListItem

	err := r.db.WithContext(ctx).
		Table("doctors AS d").
		Select(`d.id AS id,
		        u.name AS name,
		        d.title AS title,
		        COALESCE(d.specialty, '') AS specialty,
		        d.consult_fee AS consult_fee,
		        dd.department_id AS department_id,
		        dd.is_primary AS is_primary`).
		Joins("JOIN users AS u ON u.id = d.user_id").
		Joins("JOIN doctor_departments AS dd ON dd.doctor_id = d.id").
		Where("dd.department_id = ?", departmentID).
		// 手写 JOIN 不会自动带上软删除条件，必须显式过滤，
		// 否则已软删除的医生/用户/关联关系仍会出现在列表里。
		Where("d.deleted_at IS NULL AND u.deleted_at IS NULL AND dd.deleted_at IS NULL").
		Where("d.status = ? AND u.status = ?", model.StatusActive, model.StatusActive).
		// 主诊科室优先，其次按运营配置顺序
		Order("dd.is_primary DESC, dd.sort_order ASC, d.id ASC").
		Scan(&items).Error
	if err != nil {
		return nil, fmt.Errorf("query doctors by department: %w", err)
	}
	return items, nil
}

// FindDetail 返回医生详情（含姓名），不存在的医生返回 ErrNotFound。
func (r *DoctorRepo) FindDetail(ctx context.Context, doctorID uint64) (*model.DoctorDetail, error) {
	var detail model.DoctorDetail

	err := r.db.WithContext(ctx).
		Table("doctors AS d").
		Select(`d.id AS id,
		        u.name AS name,
		        d.title AS title,
		        COALESCE(d.specialty, '') AS specialty,
		        COALESCE(d.intro, '') AS intro,
		        d.consult_fee AS consult_fee`).
		Joins("JOIN users AS u ON u.id = d.user_id").
		Where("d.id = ?", doctorID).
		Where("d.deleted_at IS NULL AND u.deleted_at IS NULL").
		Where("d.status = ? AND u.status = ?", model.StatusActive, model.StatusActive).
		Scan(&detail).Error
	if err != nil {
		return nil, fmt.Errorf("find doctor detail: %w", err)
	}
	if detail.ID == 0 {
		return nil, ErrNotFound
	}
	return &detail, nil
}

// ListDepartmentsOfDoctor 返回医生所属的全部科室（多对多），主诊科室排在最前。
func (r *DoctorRepo) ListDepartmentsOfDoctor(ctx context.Context, doctorID uint64) ([]model.DepartmentBrief, error) {
	var items []model.DepartmentBrief

	err := r.db.WithContext(ctx).
		Table("doctor_departments AS dd").
		Select("dp.id AS id, dp.code AS code, dp.name AS name, COALESCE(dp.description, '') AS description, dp.sort_order AS sort_order").
		Joins("JOIN departments AS dp ON dp.id = dd.department_id").
		Where("dd.doctor_id = ?", doctorID).
		Where("dd.deleted_at IS NULL AND dp.deleted_at IS NULL").
		Where("dp.status = ?", model.StatusActive).
		Order("dd.is_primary DESC, dd.sort_order ASC, dp.sort_order ASC").
		Scan(&items).Error
	if err != nil {
		return nil, fmt.Errorf("list departments of doctor: %w", err)
	}
	return items, nil
}

// FindIDByUserID 由用户 ID 解析医生档案 ID。
//
// 病历、预约、预问诊三张表存的都是 doctors.id，直接拿 users.id 比较会永远不相等。
// 这个方法放在 DoctorRepo 而不是某个业务仓储里：它是医生档案的基本查询，
// 三个 service 都要用，挂在任一业务模块下都会造成奇怪的依赖方向。
func (r *DoctorRepo) FindIDByUserID(ctx context.Context, userID uint64) (uint64, error) {
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

// DepartmentDoctorsCacheKeys 返回科室医生列表的缓存键。
// 导出是为了让写路径（如数据初始化工具）也能精确失效，而不必复制键前缀。
func DepartmentDoctorsCacheKeys(departmentIDs ...uint64) []string {
	keys := make([]string, 0, len(departmentIDs))
	for _, id := range departmentIDs {
		keys = append(keys, deptDoctorsKey(id))
	}
	return keys
}

func deptDoctorsKey(departmentID uint64) string {
	return fmt.Sprintf("%s%d", deptDoctorsKeyPrefix, departmentID)
}
