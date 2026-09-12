// Package model 存放实体(PO)、请求/响应 DTO 与枚举常量。
package model

import (
	"time"

	"gorm.io/gorm"
)

// Role 是用户角色（spec §2.1）。
type Role string

// 角色常量。
const (
	RoleAdmin   Role = "admin"
	RoleDoctor  Role = "doctor"
	RolePatient Role = "patient"
)

// Valid 判断角色是否为已知取值。
func (r Role) Valid() bool {
	switch r {
	case RoleAdmin, RoleDoctor, RolePatient:
		return true
	default:
		return false
	}
}

// 用户状态。
const (
	UserStatusDisabled int8 = 0
	UserStatusActive   int8 = 1
)

// User 对应 users 表。
// PhoneEnc/IDCardEnc 为 AES-GCM 密文；PhoneHash/IDCardHash 为确定性摘要，
// 分别用于唯一约束与登录查找（见 pkg/crypto 的说明）。
type User struct {
	ID           uint64         `gorm:"column:id;primaryKey"`
	Role         Role           `gorm:"column:role"`
	Name         string         `gorm:"column:name"`
	PhoneEnc     []byte         `gorm:"column:phone_enc"`
	PhoneHash    string         `gorm:"column:phone_hash"`
	IDCardEnc    []byte         `gorm:"column:id_card_enc"`
	IDCardHash   *string        `gorm:"column:id_card_hash"`
	PasswordHash string         `gorm:"column:password_hash"`
	Status       int8           `gorm:"column:status"`
	LastLoginAt  *time.Time     `gorm:"column:last_login_at"`
	DeletedAt    gorm.DeletedAt `gorm:"column:deleted_at;index"`
	CreatedAt    time.Time      `gorm:"column:created_at"`
	UpdatedAt    time.Time      `gorm:"column:updated_at"`
}

// TableName 指定表名。
func (User) TableName() string { return "users" }

// IsActive 判断账号是否可登录。
func (u *User) IsActive() bool { return u.Status == UserStatusActive }
