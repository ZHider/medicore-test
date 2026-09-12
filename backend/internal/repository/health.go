// Package repository 是唯一允许出现 GORM / 数据库客户端的层（spec §3.2）。
package repository

import (
	"context"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// HealthRepo 提供依赖健康探测。
// 即使只是 Ping，也放在 repository 层：handler 与 service 都不应直接持有数据库句柄。
type HealthRepo struct {
	db  *gorm.DB
	rdb *redis.Client
}

// NewHealthRepo 构造 HealthRepo。
func NewHealthRepo(db *gorm.DB, rdb *redis.Client) *HealthRepo {
	return &HealthRepo{db: db, rdb: rdb}
}

// PingMySQL 探测 MySQL 连通性。
func (r *HealthRepo) PingMySQL(ctx context.Context) error {
	sqlDB, err := r.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.PingContext(ctx)
}

// PingRedis 探测 Redis 连通性。
func (r *HealthRepo) PingRedis(ctx context.Context) error {
	return r.rdb.Ping(ctx).Err()
}
