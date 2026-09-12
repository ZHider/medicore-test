package repository

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"github.com/medicore/backend/internal/model"
)

// DepartmentRepo 是 departments 表的数据访问。
type DepartmentRepo struct {
	db *gorm.DB
}

// NewDepartmentRepo 构造 DepartmentRepo。
func NewDepartmentRepo(db *gorm.DB) *DepartmentRepo {
	return &DepartmentRepo{db: db}
}

// ListActive 返回全部启用中的科室，按运营配置的顺序排列。
// 科室数量有限（通常几十个），无需分页。
func (r *DepartmentRepo) ListActive(ctx context.Context) ([]model.DepartmentBrief, error) {
	var items []model.DepartmentBrief

	err := r.db.WithContext(ctx).
		Model(&model.Department{}).
		Select("id, code, name, COALESCE(description, '') AS description, sort_order").
		Where("status = ?", model.StatusActive).
		Order("sort_order ASC, id ASC").
		Scan(&items).Error
	if err != nil {
		return nil, fmt.Errorf("list departments: %w", err)
	}
	return items, nil
}

// FindByID 按主键查找科室。
func (r *DepartmentRepo) FindByID(ctx context.Context, id uint64) (*model.Department, error) {
	var d model.Department

	err := r.db.WithContext(ctx).First(&d, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("find department: %w", err)
	}
	return &d, nil
}

// Exists 判断科室是否存在且启用，用于在查询关联数据前给出 404 而不是空列表。
func (r *DepartmentRepo) Exists(ctx context.Context, id uint64) (bool, error) {
	var count int64

	err := r.db.WithContext(ctx).
		Model(&model.Department{}).
		Where("id = ? AND status = ?", id, model.StatusActive).
		Count(&count).Error
	if err != nil {
		return false, fmt.Errorf("check department exists: %w", err)
	}
	return count > 0, nil
}
