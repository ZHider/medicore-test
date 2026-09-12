package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/medicore/backend/internal/model"
)

// ErrNotFound 表示目标记录不存在。service 层据此转换为业务错误，
// 避免 gorm.ErrRecordNotFound 向上泄漏到非 repository 层。
var ErrNotFound = errors.New("record not found")

// ErrDuplicateKey 表示唯一键冲突。
// 由 GORM 的 ErrDuplicatedKey 转译而来（需在 gorm.Config 中开启 TranslateError），
// 而不是解析驱动返回的错误文本——后者会随驱动版本或语言环境失效。
var ErrDuplicateKey = errors.New("duplicate key")

// UserRepo 是 users 表的数据访问。
type UserRepo struct {
	db *gorm.DB
}

// NewUserRepo 构造 UserRepo。
func NewUserRepo(db *gorm.DB) *UserRepo {
	return &UserRepo{db: db}
}

// Create 新增用户。唯一键冲突转译为 ErrDuplicateKey，由调用方决定业务语义。
func (r *UserRepo) Create(ctx context.Context, u *model.User) error {
	if err := r.db.WithContext(ctx).Create(u).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return fmt.Errorf("create user: %w", ErrDuplicateKey)
		}
		return fmt.Errorf("create user: %w", err)
	}
	return nil
}

// FindByPhoneHash 按手机号摘要查找用户，供登录使用。
// 摘要为确定性 HMAC，因此可命中 uk_phone_hash 唯一索引；软删除行由 GORM 自动排除。
func (r *UserRepo) FindByPhoneHash(ctx context.Context, phoneHash string) (*model.User, error) {
	var u model.User
	err := r.db.WithContext(ctx).Where("phone_hash = ?", phoneHash).First(&u).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("find user by phone hash: %w", err)
	}
	return &u, nil
}

// FindByID 按主键查找用户。
func (r *UserRepo) FindByID(ctx context.Context, id uint64) (*model.User, error) {
	var u model.User
	err := r.db.WithContext(ctx).First(&u, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("find user by id: %w", err)
	}
	return &u, nil
}

// TouchLastLogin 更新最近登录时间。
// 只更新单列，避免整行写放大。
func (r *UserRepo) TouchLastLogin(ctx context.Context, id uint64, at time.Time) error {
	err := r.db.WithContext(ctx).
		Model(&model.User{}).
		Where("id = ?", id).
		UpdateColumn("last_login_at", at).Error
	if err != nil {
		return fmt.Errorf("touch last login: %w", err)
	}
	return nil
}
