package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// refreshKeyPrefix 是刷新令牌在 Redis 中的键前缀。
const refreshKeyPrefix = "auth:refresh:"

// TokenRepo 负责刷新令牌的存取（spec §2.2：refresh_token 存 Redis）。
//
// 用 Redis 而非 JWT 的根本原因：刷新令牌必须**可撤销**，
// 无状态令牌做不到这一点（除非再引入黑名单，等于绕回 Redis）。
type TokenRepo struct {
	rdb *redis.Client
}

// NewTokenRepo 构造 TokenRepo。
func NewTokenRepo(rdb *redis.Client) *TokenRepo {
	return &TokenRepo{rdb: rdb}
}

// SaveRefresh 保存刷新令牌，值为用户 ID。
func (r *TokenRepo) SaveRefresh(ctx context.Context, token string, userID uint64, ttl time.Duration) error {
	if err := r.rdb.Set(ctx, refreshKeyPrefix+token, userID, ttl).Err(); err != nil {
		return fmt.Errorf("save refresh token: %w", err)
	}
	return nil
}

// ConsumeRefresh 原子地取出并删除刷新令牌（GETDEL），返回所属用户 ID。
//
// 必须用 GETDEL 而非 GET+DEL：刷新令牌是**一次性**凭证，
// 分成两步会在并发刷新时让同一个令牌被消费两次（重放窗口）。
func (r *TokenRepo) ConsumeRefresh(ctx context.Context, token string) (uint64, error) {
	val, err := r.rdb.GetDel(ctx, refreshKeyPrefix+token).Uint64()
	if errors.Is(err, redis.Nil) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("consume refresh token: %w", err)
	}
	return val, nil
}

// DeleteRefresh 删除刷新令牌（登出）。令牌不存在不视为错误，保证登出幂等。
func (r *TokenRepo) DeleteRefresh(ctx context.Context, token string) error {
	if err := r.rdb.Del(ctx, refreshKeyPrefix+token).Err(); err != nil {
		return fmt.Errorf("delete refresh token: %w", err)
	}
	return nil
}
