package repository

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	idempotencyKeyPrefix = "idem:grab:"
	// 幂等键保留 24 小时：足够覆盖客户端重试窗口，又不会无限占用内存。
	idempotencyTTL = 24 * time.Hour
	// idempotencyInFlight 是"已有请求正在处理"的占位值。
	// 用哨兵字符串而非空值，才能与"键不存在"区分开。
	idempotencyInFlight = "pending"
)

// ErrInFlight 表示同一幂等键的请求正在处理中。
var ErrInFlight = errors.New("request in flight")

// idempotencyReleaseScript 仅在值仍为占位值时删除键。
//
// 若直接 DEL，会把"已完成"的记录一并删掉：
// 设想请求 A 成功写入预约 ID 后，请求 B 因超时执行清理，
// 一次无条件 DEL 就让后续重放再也查不到结果，幂等保护形同虚设。
// 比对删除保证只清理仍然处于处理中的键。
//
// KEYS[1] 幂等键   ARGV[1] 期望的占位值
const idempotencyReleaseScript = `
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0
`

var idempotencyRelease = redis.NewScript(idempotencyReleaseScript)

// IdempotencyRepo 用 Redis 记录抢号幂等键。
//
// 与数据库的 uk_user_idem 唯一索引形成两层保护：
// Redis 挡住绝大部分重复提交（快速失败），唯一索引兜住 Redis 失效的情况。
type IdempotencyRepo struct {
	rdb *redis.Client
}

// NewIdempotencyRepo 构造 IdempotencyRepo。
func NewIdempotencyRepo(rdb *redis.Client) *IdempotencyRepo {
	return &IdempotencyRepo{rdb: rdb}
}

// Acquire 尝试占用幂等键。
//
// 返回 (是否本次占用, 已存在的预约ID, 错误)：
//   - (true, 0, nil)     首次请求，可以继续处理
//   - (false, id, nil)   该键已完成，id 为上次创建的预约
//   - (false, 0, ErrInFlight) 同一键的请求正在处理中
func (r *IdempotencyRepo) Acquire(ctx context.Context, userID uint64, key string) (bool, uint64, error) {
	fullKey := idempotencyKey(userID, key)

	acquired, err := r.rdb.SetNX(ctx, fullKey, idempotencyInFlight, idempotencyTTL).Result()
	if err != nil {
		return false, 0, fmt.Errorf("acquire idempotency key: %w", err)
	}
	if acquired {
		return true, 0, nil
	}

	value, err := r.rdb.Get(ctx, fullKey).Result()
	if errors.Is(err, redis.Nil) {
		// 极窄的竞态：读取前刚好过期。告知上层重试即可。
		return false, 0, ErrInFlight
	}
	if err != nil {
		return false, 0, fmt.Errorf("read idempotency key: %w", err)
	}
	if value == idempotencyInFlight {
		return false, 0, ErrInFlight
	}

	appointmentID, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		// 值被外部写坏：当作处理中，避免错误地返回一个不存在的预约
		return false, 0, ErrInFlight
	}
	return false, appointmentID, nil
}

// Complete 记录该幂等键最终创建的预约。
//
// 使用 SET ... XX：只在键仍存在时写入。若键已过期消失，
// 不应凭空创建一个新键——那会让一个已过期的幂等窗口重新生效。
func (r *IdempotencyRepo) Complete(ctx context.Context, userID uint64, key string, appointmentID uint64) error {
	fullKey := idempotencyKey(userID, key)

	ok, err := r.rdb.SetXX(ctx, fullKey, appointmentID, idempotencyTTL).Result()
	if err != nil {
		return fmt.Errorf("complete idempotency key: %w", err)
	}
	if !ok {
		return fmt.Errorf("idempotency key %s vanished before completion", fullKey)
	}
	return nil
}

// Release 放弃幂等键，使客户端可以重试。
// 仅在当前值仍是"处理中"占位值时才删除。
func (r *IdempotencyRepo) Release(ctx context.Context, userID uint64, key string) error {
	_, err := idempotencyRelease.Run(
		ctx, r.rdb, []string{idempotencyKey(userID, key)}, idempotencyInFlight,
	).Int()
	if err != nil {
		return fmt.Errorf("release idempotency key: %w", err)
	}
	return nil
}

func idempotencyKey(userID uint64, key string) string {
	return fmt.Sprintf("%s%d:%s", idempotencyKeyPrefix, userID, key)
}
