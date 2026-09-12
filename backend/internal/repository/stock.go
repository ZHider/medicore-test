package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/medicore/backend/internal/model"
)

const stockKeyPrefix = "stock:sched:"

// StockResult 是扣减结果。
type StockResult int

const (
	// StockInsufficient 号源不足。
	StockInsufficient StockResult = 0
	// StockOK 扣减成功。
	StockOK StockResult = 1
	// StockNotLoaded 键不存在，需要先预热再重试。
	//
	// spec 的原始脚本在键不存在时直接返回 0（当作"号源不足"），
	// 这会让 Redis 冷启动或键过期后的**所有**抢号请求全部失败，
	// 而且错误信息还是"号源不足"，排查时极具误导性。
	// 用独立的返回值把"没有号源"与"没有预热"区分开。
	StockNotLoaded StockResult = -1
)

// stockDecrementScript 原子扣减号源。
//
// 必须在 Redis 侧原子执行：若拆成"读余量 → 判断 → 扣减"三次往返，
// 并发请求会读到同一个旧值，直接导致超卖。
//
// KEYS[1] 号源键   ARGV[1] 扣减数量
// 返回 1 / 0 / -1，语义见 StockResult。
const stockDecrementScript = `
local key = KEYS[1]
local qty = tonumber(ARGV[1])

local current = redis.call('GET', key)
if current == false then
  return -1
end

current = tonumber(current)
if current < qty then
  return 0
end

redis.call('DECRBY', key, qty)
return 1
`

// stockReleaseScript 回补号源。
//
// 两个保护：
//  1. 键不存在时不创建 —— 否则 INCRBY 会凭空造出一个值为 qty 的键，
//     让号源看起来比真实值多，等于制造超卖。
//  2. 以容量为上限 —— 防止重复回补（例如补偿与 cron 释放同时命中）把余量推高。
//
// KEYS[1] 号源键   ARGV[1] 回补数量   ARGV[2] 容量上限
// 返回回补后的余量；键不存在返回 -1。
const stockReleaseScript = `
local key = KEYS[1]
local qty = tonumber(ARGV[1])
local cap = tonumber(ARGV[2])

if redis.call('EXISTS', key) == 0 then
  return -1
end

local current = redis.call('INCRBY', key, qty)
if current > cap then
  -- 必须 KEEPTTL：裸 SET 会清掉 EnsureLoaded 用 SETNX 设置的过期时间，
  -- 号源键就此变成永不过期的键，而 SETNX 也不会再刷新它。
  -- 对历史排班而言更糟——ListReconcilable 只扫 sched_date >= today，
  -- 这个被永久钉住的错值再也不会被对账修正。
  redis.call('SET', key, cap, 'KEEPTTL')
  current = cap
end
return current
`

var (
	stockDecrement   = redis.NewScript(stockDecrementScript)
	stockRelease     = redis.NewScript(stockReleaseScript)
	stockCompareSwap = redis.NewScript(stockCompareAndSetScript)
)

// stockCompareAndSetScript 仅当当前值与期望值一致时才更新，并保留原 TTL。
//
// 对账必须用它而不是无条件 SET：对账读取 MySQL 与写入 Redis 之间存在时间窗，
// 若期间有并发扣减，无条件 SET 会把已扣掉的号源"还回去"，反而制造超卖。
// 比对失败就放弃本次修正，交给下一轮——偏差会一直存在，但绝不会因此多放号。
//
// KEYS[1] 号源键   ARGV[1] 期望值   ARGV[2] 目标值
// 返回 1 已修正 / 0 值已变化，放弃
const stockCompareAndSetScript = `
local key = KEYS[1]
if redis.call('GET', key) == ARGV[1] then
  redis.call('SET', key, ARGV[2], 'KEEPTTL')
  return 1
end
return 0
`

// StockRepo 负责 Redis 上的号源计数。
//
// 它只做"快速闸门"：MySQL 的 schedules.remaining 始终是唯一事实来源，
// Redis 用于在并发下以单线程原子性挡住超卖。两者由预热与对账保持一致。
type StockRepo struct {
	rdb *redis.Client
}

// NewStockRepo 构造 StockRepo。
func NewStockRepo(rdb *redis.Client) *StockRepo {
	return &StockRepo{rdb: rdb}
}

// StockKey 返回排班对应的号源键。
func StockKey(scheduleID uint64) string {
	return fmt.Sprintf("%s%d", stockKeyPrefix, scheduleID)
}

// EnsureLoaded 预热号源。
//
// 使用 SETNX 而非 SET 是关键：若键已存在（说明已有并发请求在扣减），
// 用数据库里的旧值覆盖会把已经扣掉的号源"还回去"，直接造成超卖。
func (r *StockRepo) EnsureLoaded(ctx context.Context, s *model.Schedule) error {
	err := r.rdb.SetNX(ctx, StockKey(s.ID), s.Remaining, stockTTL(s.SchedDate)).Err()
	if err != nil {
		return fmt.Errorf("ensure stock loaded: %w", err)
	}
	return nil
}

// Decrement 原子扣减号源。
func (r *StockRepo) Decrement(ctx context.Context, scheduleID uint64, qty int) (StockResult, error) {
	res, err := stockDecrement.Run(ctx, r.rdb, []string{StockKey(scheduleID)}, qty).Int()
	if err != nil {
		return 0, fmt.Errorf("decrement stock: %w", err)
	}
	return StockResult(res), nil
}

// Release 回补号源，返回回补后的余量。键不存在时返回 -1。
func (r *StockRepo) Release(ctx context.Context, scheduleID uint64, qty, capacity int) (int, error) {
	res, err := stockRelease.Run(ctx, r.rdb, []string{StockKey(scheduleID)}, qty, capacity).Int()
	if err != nil {
		return 0, fmt.Errorf("release stock: %w", err)
	}
	return res, nil
}

// Current 读取当前余量，第二个返回值表示键是否存在。供对账使用。
func (r *StockRepo) Current(ctx context.Context, scheduleID uint64) (int, bool, error) {
	val, err := r.rdb.Get(ctx, StockKey(scheduleID)).Int()
	if errors.Is(err, redis.Nil) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("read stock: %w", err)
	}
	return val, true, nil
}

// CompareAndSet 仅当当前值等于 expected 时把它改成 desired。
// 返回 false 表示值已被并发修改，本次修正被放弃。
func (r *StockRepo) CompareAndSet(
	ctx context.Context, scheduleID uint64, expected, desired int,
) (bool, error) {
	res, err := stockCompareSwap.Run(
		ctx, r.rdb, []string{StockKey(scheduleID)}, expected, desired,
	).Int()
	if err != nil {
		return false, fmt.Errorf("compare and set stock: %w", err)
	}
	return res == 1, nil
}

// stockTTL 让号源键在就诊日结束后自然过期，避免长期占用内存。
// 下限 1 小时，防止对当天稍晚的排班算出过短的 TTL。
func stockTTL(schedDate time.Time) time.Duration {
	ttl := time.Until(schedDate.AddDate(0, 0, 2))
	if ttl < time.Hour {
		return time.Hour
	}
	return ttl
}
