package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"golang.org/x/sync/singleflight"
)

// Cache 是 Redis 上的 JSON 缓存。
//
// 降级原则：缓存不可用不得导致请求失败——读失败视作未命中、写失败只影响性能。
// 但**不能静默降级**：全部失败路径都会记日志，否则缓存长期失效也无人察觉。
type Cache struct {
	rdb   *redis.Client
	group singleflight.Group
	log   *zap.Logger
}

// NewCache 构造 Cache。
func NewCache(rdb *redis.Client, log *zap.Logger) *Cache {
	if log == nil {
		log = zap.NewNop()
	}
	return &Cache{rdb: rdb, log: log}
}

// GetJSON 读取并反序列化缓存，返回是否命中。
// 内容损坏时按未命中处理并删除脏键，避免反序列化错误反复出现。
func (c *Cache) GetJSON(ctx context.Context, key string, dest any) (bool, error) {
	raw, err := c.rdb.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("cache get %s: %w", key, err)
	}

	if err := json.Unmarshal(raw, dest); err != nil {
		c.log.Warn("cache payload corrupted, evicting",
			zap.String("key", key), zap.Error(err))
		if delErr := c.rdb.Del(ctx, key).Err(); delErr != nil {
			c.log.Warn("cache evict failed", zap.String("key", key), zap.Error(delErr))
		}
		return false, nil
	}
	return true, nil
}

// SetJSON 序列化并写入缓存。
func (c *Cache) SetJSON(ctx context.Context, key string, value any, ttl time.Duration) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("cache marshal %s: %w", key, err)
	}
	if err := c.rdb.Set(ctx, key, raw, ttl).Err(); err != nil {
		return fmt.Errorf("cache set %s: %w", key, err)
	}
	return nil
}

// Del 删除若干键，用于写操作后的精确失效。
func (c *Cache) Del(ctx context.Context, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	if err := c.rdb.Del(ctx, keys...).Err(); err != nil {
		return fmt.Errorf("cache del: %w", err)
	}
	return nil
}

// Load 实现 cache-aside 读模式，并用 singleflight 合并并发回源。
//
// loadTimeout 限制回源本身的最长耗时。
// 回源用的是脱离调用方取消信号的 context，因此必须有独立上界，
// 否则一次卡住的上游会永久占住这个键。
const loadTimeout = 5 * time.Second

// 为什么需要 singleflight：热点键失效（或进程刚启动缓存为空）的瞬间，
// 可能有成百上千个并发请求同时未命中，若各自回源会把压力全部打到 MySQL，
// 即所谓的「缓存击穿」。合并后同一键同时只有一个请求回源，其余等待并共享结果。
func Load[T any](
	ctx context.Context,
	c *Cache,
	key string,
	ttl time.Duration,
	loader func(context.Context) (T, error),
) (T, error) {
	var cached T
	if hit, err := c.GetJSON(ctx, key, &cached); err != nil {
		// 缓存故障不阻断请求，只是退化为每次都回源
		c.log.Warn("cache read failed, falling back to source",
			zap.String("key", key), zap.Error(err))
	} else if hit {
		return cached, nil
	}

	value, err, _ := c.group.Do(key, func() (any, error) {
		// 二次检查：等待期间可能已有其他请求把缓存填好了
		var fresh T
		if hit, err := c.GetJSON(ctx, key, &fresh); err == nil && hit {
			return fresh, nil
		}

		// 用**脱离调用方取消信号**的 context 回源。
		//
		// singleflight 的跟随者共享 leader 的 context：若 leader 的请求被取消
		// （用户关掉页面、连接超时），所有健康的跟随者会一起收到
		// context.Canceled，拿到一个本不该属于它们的错误。
		//
		// detach 之后只保留 context 里的值（trace_id 等）并加一个独立超时上界：
		// 单个请求的取消不再波及他人；即便所有调用方都提前离开，
		// 这次回源也算顺带把缓存预热好了，并不浪费。
		loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), loadTimeout)
		defer cancel()

		loaded, err := loader(loadCtx)
		if err != nil {
			return nil, err
		}
		if err := c.SetJSON(loadCtx, key, loaded, ttl); err != nil {
			c.log.Warn("cache write failed", zap.String("key", key), zap.Error(err))
		}
		return loaded, nil
	})
	if err != nil {
		var zero T
		return zero, err
	}

	out, ok := value.(T)
	if !ok {
		var zero T
		return zero, fmt.Errorf("cache: unexpected value type %T", value)
	}
	return out, nil
}
